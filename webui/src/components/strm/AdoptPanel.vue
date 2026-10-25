<script setup lang="ts">
import { computed, onUnmounted, ref } from 'vue'
import { ChevronDown, ChevronRight, FolderInput, ScanSearch } from '@lucide/vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HCheckbox from '@/components/hero/HCheckbox.vue'
import HInput from '@/components/hero/HInput.vue'
import HSpinner from '@/components/hero/HSpinner.vue'
import { syncApi, tasksApi } from '@/api'
import type { AdoptBucketKey, AdoptReport } from '@/api/sync'
import type { TaskJob } from '@/api/tasks'
import { toastError, useFeedback } from '@/composables/useFeedback'

/**
 * 接管已有 STRM（ADOPT-STRM-PLAN.md）：从别的项目换过来时，让本站认下本地已有的 STRM，
 * Emby 里的条目、观看记录一样不动。先预演（只读）看报告，结论是「可以接管」时再执行。
 * 预演与执行都是队列任务：入队后按 job_id 轮询任务详情，结束后读 result 里的报告（执行的带 applied）。
 * 执行时后端会重新扫一遍、对一遍号，不拿这里看到的预演结果
 */
const { dialog } = useFeedback()

const stopped = ref(false)
const scanDir = ref('')
const thenFull = ref(true)
const starting = ref(false)
const job = ref<TaskJob | null>(null)
/** 上一次预演的报告：执行任务跑着时还留着给用户看 */
const previewReport = ref<AdoptReport | null>(null)
let timer: ReturnType<typeof setTimeout> | undefined

const FINISHED = ['success', 'partial', 'failed', 'canceled', 'interrupted']
const running = computed(() => !!job.value && !FINISHED.includes(job.value.status))
const jobReport = computed(() => {
  const r = job.value?.result as unknown as AdoptReport | undefined
  return r && Array.isArray(r.buckets) ? r : null
})
const applying = computed(() => job.value?.title?.startsWith('接管已有 STRM') && !job.value.title.includes('预演'))
const report = computed(() => jobReport.value ?? previewReport.value)
const applied = computed(() => jobReport.value?.applied ?? null)
const failed = computed(() => !!job.value && FINISHED.includes(job.value.status) && !jobReport.value)

async function enqueue(apply: boolean) {
  starting.value = true
  try {
    const body = { scan_dir: scanDir.value.trim() || undefined }
    const d = apply
      ? await syncApi.adoptApply({ ...body, then_full: thenFull.value })
      : await syncApi.adoptPreview(body)
    job.value = {
      id: d.job_id,
      status: 'queued',
      message: d.message,
      title: apply ? '接管已有 STRM' : '接管已有 STRM：预演',
    } as TaskJob
    poll()
  } catch (e) {
    toastError(e, apply ? '接管没能开始' : '预演没能开始')
  } finally {
    starting.value = false
  }
}

async function apply() {
  const ok = await dialog.confirm({
    title: `接管 ${usable.value} 个 STRM`,
    content:
      '本站会重新扫一遍、对一遍号，把能接管的 STRM 内容改写成本站直链并登记台账（路径、文件名、修改时间都不动）。' +
      '之后本站的全量 / 增量会管这些文件：网盘上删了，它们会被标成失效 STRM。原项目确认已经停了？',
    actions: [
      { label: '取消', value: false, variant: 'tertiary' },
      { label: '执行接管', value: true, variant: 'primary' },
    ],
  })
  if (!ok) return
  previewReport.value = jobReport.value
  await enqueue(true)
}

async function poll() {
  clearTimeout(timer)
  if (!job.value) return
  try {
    const { data } = await tasksApi.detail(job.value.id)
    job.value = data
  } catch {
    // 偶发网络错误：下一轮再读
  }
  if (running.value) timer = setTimeout(poll, 2000)
}

function reset() {
  clearTimeout(timer)
  job.value = null
  previewReport.value = null
  expanded.value = {}
}

onUnmounted(() => clearTimeout(timer))

const BUCKETS: Record<AdoptBucketKey, { label: string; desc: string; tone: 'ok' | 'warn' | 'muted' }> = {
  adopt: { label: '可原样接管', desc: '位置和文件名都是本站会写的，接管后只换直链', tone: 'ok' },
  rename: { label: '名字不同', desc: '目录对，文件名不是本站的写法（文件名模板、Windows 替换过字符），接管后沿用现在的名字', tone: 'ok' },
  already: { label: '已在台账', desc: '本站已经认得它，不用再接管', tone: 'muted' },
  moved: { label: '位置不同', desc: '指向的网盘文件对上了，但本站会写在别的目录。先按下面的换算改挂载或本地目录，再预演一次', tone: 'warn' },
  dup: { label: '重复', desc: '和另一个本地 STRM 指向同一个网盘文件，Emby 里多半是两条。接管时不动它', tone: 'warn' },
  missing: { label: '网盘上没有', desc: '指向的文件不在配置的媒体库里（已删除、在别的目录，或是另一个账号的）', tone: 'warn' },
  share: { label: '分享 STRM', desc: '指向别人的分享，不是本账号的文件，不接管', tone: 'muted' },
  unknown: { label: '认不出', desc: '内容里找不到 pickcode、文件 id 或网盘路径', tone: 'muted' },
  notlocal: { label: '网盘上有、本地没有', desc: '接管后跑一次普通全量同步补写', tone: 'muted' },
}

const FORMATS: Record<string, string> = {
  station: '本站 / STRMhub',
  p115strmhelper: 'p115strmhelper',
  qmediasync: 'qmediasync',
  mediasync115: 'MediaSync115',
  openlist: 'OpenList 路径',
  mount: '挂载路径',
  other: '其他',
}

const formatList = computed(() =>
  Object.entries(report.value?.formats ?? {})
    .sort((a, b) => b[1] - a[1])
    .map(([k, n]) => `${FORMATS[k] ?? k} ${n}`)
    .join(' · '),
)

const expanded = ref<Record<string, boolean>>({})
function toggle(key: string) {
  expanded.value = { ...expanded.value, [key]: !expanded.value[key] }
}

const usable = computed(() => {
  const r = report.value
  if (!r) return 0
  return r.buckets.filter((b) => b.key === 'adopt' || b.key === 'rename').reduce((s, b) => s + b.count, 0)
})
const notLocal = computed(() => report.value?.buckets.find((b) => b.key === 'notlocal')?.count ?? 0)
</script>

<template>
  <div class="adopt">
    <template v-if="!job">
      <p class="lead">
        从别的项目（p115strmhelper、qmediasync、MediaSync115 等）换过来时，用它让本站认下本地已有的 STRM：
        路径和文件名一个都不动，只把内容换成本站直链并登记进台账，Emby 里的条目、观看记录、元数据都保留。
        只是 IP / 端口变了、还想继续用原项目的，用「替换地址」就够了。
      </p>
      <p class="lead">
        先<strong>预演</strong>：扫一遍本地 STRM、取一次网盘清单，逐个对号后出报告，不改任何文件。看过报告再决定要不要执行。
      </p>
      <HCheckbox v-model:checked="stopped">
        我已经停掉了原项目的同步与监控（不停的话，它会把直链改回去，或在旁边另写文件）
      </HCheckbox>
      <div class="field">
        <label class="field-label">扫描目录</label>
        <HInput v-model="scanDir" mono placeholder="留空 = 本地媒体库目录" />
        <span class="field-tip">旧 STRM 还没挂到本地媒体库目录下时，填它在容器里的路径，报告会算出该怎么挂（这种情况只能预演）。</span>
      </div>
      <div class="actions end">
        <HButton variant="primary" :disabled="!stopped" :loading="starting" @click="enqueue(false)">
          <template #icon><ScanSearch :size="14" /></template>
          开始预演
        </HButton>
      </div>
    </template>

    <div v-else-if="running" class="state">
      <HSpinner />
      <span>
        {{ job.progress?.phase || job.message || '排队中…' }}
        <template v-if="job.progress?.total">（{{ job.progress.done }} / {{ job.progress.total }}）</template>
      </span>
    </div>

    <HAlert v-else-if="failed" status="danger" :title="applying ? '接管没有完成' : '预演没有完成'">{{ job.message }}</HAlert>

    <template v-if="job && !running && applied">
      <HAlert :status="applied.failed ? 'warning' : 'success'" :title="applied.failed ? '接管完成，有一部分没成' : '接管完成'">
        {{ job.message }}
      </HAlert>
      <p class="lead">
        这些 STRM 现在由本站管理：路径和修改时间没变，Emby 不用刷新。
        <template v-if="applied.full_job_id">已排一次全量同步补写本地没有的，进度在任务中心。</template>
        <template v-else-if="notLocal">网盘上还有 {{ notLocal }} 个视频本地没有 STRM，到「Strm 管理 → 全量」跑一次全量补上。</template>
      </p>
    </template>

    <template v-else-if="report && !running">
      <HAlert v-if="report.verdict === 'ok'" status="success" title="可以接管">
        {{ usable }} 个本地 STRM 能原地接管。看过下面各项没问题，就可以执行。
      </HAlert>
      <HAlert v-else-if="report.verdict === 'move'" status="warning" title="多数 STRM 的位置和本站对不上">
        本站的本地路径是「本地媒体库目录 / 库的本地目录 / 库内路径」。按下面的前缀换算调整挂载或库的本地目录，
        让本站会写的位置正好是现有文件的位置，再预演一次。搬动本地文件会让 Emby 当成新文件，别那样做。
      </HAlert>
      <HAlert v-else status="warning" title="一个都没对上">
        检查扫描目录是不是旧 STRM 所在的位置、媒体库是不是同一个 115 目录、115 账号是不是同一个。
      </HAlert>

      <div class="summary">
        <span>扫描 <code>{{ report.scan_dir }}</code>：本地 STRM {{ report.local_strm }} 个</span>
        <span>网盘视频 {{ report.remote_videos }} 个</span>
        <span v-if="formatList">写法：{{ formatList }}</span>
      </div>
      <div v-for="lib in report.libs" :key="lib.cid" class="lib" :class="{ 'is-error': lib.error }">
        媒体库 {{ lib.name || lib.cid }}：{{ lib.error ? lib.error : `${lib.videos} 个视频` }}
      </div>

      <div v-if="report.shifts.length" class="shifts">
        <div class="shifts-title">位置换算（本地这里 → 本站会写在那里）</div>
        <div v-for="s in report.shifts" :key="s.from + s.to" class="shift">
          <code>{{ s.from }}</code><span class="arrow">→</span><code>{{ s.to }}</code>
          <span class="count">{{ s.count }} 个</span>
        </div>
      </div>
      <HAlert v-if="report.other_account" status="warning">
        有 {{ report.other_account }} 个 STRM 的 pickcode 不是当前 115 账号的。换了账号的话 pickcode 对不上，
        只能靠直链里的网盘路径对号。
      </HAlert>

      <div class="buckets">
        <template v-for="b in report.buckets" :key="b.key">
          <div v-if="b.count" class="bucket" :data-tone="BUCKETS[b.key]?.tone">
            <button type="button" class="bucket-head" @click="toggle(b.key)">
              <component :is="expanded[b.key] ? ChevronDown : ChevronRight" :size="14" />
              <span class="bucket-label">{{ BUCKETS[b.key]?.label ?? b.key }}</span>
              <span class="bucket-count">{{ b.count }}</span>
              <span class="bucket-desc">{{ BUCKETS[b.key]?.desc }}</span>
            </button>
            <div v-if="expanded[b.key]" class="samples">
              <div v-for="(s, i) in b.samples" :key="i" class="sample">
                <div v-if="s.local" class="path">{{ s.local }}</div>
                <div v-if="s.expect && s.expect !== s.local" class="path sub">本站：{{ s.expect }}</div>
                <div v-if="!s.local && s.remote" class="path sub">网盘：{{ s.remote }}</div>
                <div v-if="s.note" class="note">{{ s.note }}</div>
              </div>
              <div v-if="b.count > b.samples.length" class="more">只列前 {{ b.samples.length }} 条</div>
            </div>
          </div>
        </template>
      </div>

      <div v-if="report.verdict === 'ok' && usable" class="apply">
        <HCheckbox v-model:checked="thenFull">接管完排一次全量同步，补写网盘上有、本地没有的 STRM</HCheckbox>
        <p class="field-tip">
          执行时写的是「Strm 管理 → 配置」里已保存的直连域名与格式，还没填直连域名的先去填好。
          「重复」「位置不同」「网盘上没有」的不动，接管完可以自己处理。
        </p>
      </div>
    </template>

    <div v-if="job && !running" class="actions end">
      <HButton variant="secondary" @click="reset">重新预演</HButton>
      <HButton
        v-if="!applied && report?.verdict === 'ok' && usable"
        variant="primary"
        :loading="starting"
        @click="apply"
      >
        <template #icon><FolderInput :size="14" /></template>
        执行接管 {{ usable }} 个
      </HButton>
    </div>
  </div>
</template>

<style scoped>
.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.actions.end {
  justify-content: flex-end;
}
.apply {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 10px 12px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.adopt {
  display: flex;
  flex-direction: column;
  gap: 12px;
  font-size: 13px;
}
.lead {
  margin: 0;
  color: var(--muted);
  line-height: 1.6;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.field-label {
  font-weight: 500;
}
.field-tip {
  font-size: 12px;
  color: var(--muted);
}
.state {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 24px 0;
  color: var(--muted);
}
.summary {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 16px;
  color: var(--muted);
}
.summary code,
.shift code {
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--foreground);
  word-break: break-all;
}
.lib {
  font-size: 12px;
  color: var(--muted);
}
.lib.is-error {
  color: var(--danger);
}
.shifts {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 10px 12px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.shifts-title {
  font-weight: 500;
}
.shift {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.arrow,
.count {
  color: var(--muted);
}
.buckets {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.bucket {
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.bucket-head {
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: 4px 10px;
  width: 100%;
  padding: 8px 12px;
  border: 0;
  background: none;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}
.bucket-head > :first-child {
  align-self: center;
  flex-shrink: 0;
}
.bucket-label {
  font-weight: 500;
}
.bucket-count {
  font-variant-numeric: tabular-nums;
}
.bucket[data-tone='ok'] .bucket-count {
  color: var(--success);
}
.bucket[data-tone='warn'] .bucket-count {
  color: var(--warning);
}
.bucket-desc {
  flex-basis: 100%;
  padding-left: 24px;
  font-size: 12px;
  color: var(--muted);
}
.samples {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 0 12px 10px 36px;
}
.path {
  font-family: var(--font-mono);
  font-size: 12px;
  word-break: break-all;
}
.path.sub,
.note,
.more {
  font-size: 12px;
  color: var(--muted);
}
</style>
