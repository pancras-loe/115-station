<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HCheckbox from '@/components/hero/HCheckbox.vue'
import HModal from '@/components/hero/HModal.vue'
import HSpinner from '@/components/hero/HSpinner.vue'
import { syncApi, tasksApi } from '@/api'
import type { StrmRebaseReport } from '@/api/sync'
import type { TaskJob } from '@/api/tasks'
import { toastError } from '@/composables/useFeedback'

/**
 * 批量改写已有 STRM 的直链地址（后端 strmrebase.go）：换了「STRM 直连域名」之后，
 * 把已经生成的 STRM 里的旧地址换成新的。只动本地文件，不请求 115，不用通知 Emby。
 * 打开时先只读预演（列出各旧地址各几个），用户勾选后入队执行，按 job_id 轮询结果
 */
const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{
  /** 保存配置后自动弹出时，父组件已经预演过，直接用它（不再读一遍） */
  preload?: StrmRebaseReport | null
  /** 表单里的域名改了还没保存：改写按已保存的地址 */
  unsaved?: boolean
  /** 刚改了域名弹出来的：开头那句换个说法 */
  afterSave?: boolean
}>()

const loading = ref(false)
const loadErr = ref('')
const report = ref<StrmRebaseReport | null>(null)
const picked = ref<Record<string, boolean>>({})
const starting = ref(false)
const job = ref<TaskJob | null>(null)
let timer: ReturnType<typeof setTimeout> | undefined

const FINISHED = ['success', 'partial', 'failed', 'canceled', 'interrupted']
const running = computed(() => !!job.value && !FINISHED.includes(job.value.status))
const result = computed(() => {
  const r = job.value?.result as unknown as StrmRebaseReport | undefined
  return r && typeof r.rewritten === 'number' ? r : null
})

const pickedCount = computed(() =>
  (report.value?.stale ?? []).reduce((n, s) => n + (picked.value[s.base] ? s.count : 0), 0),
)

function setReport(r: StrmRebaseReport) {
  report.value = r
  picked.value = Object.fromEntries(r.stale.map((s) => [s.base, true]))
}

async function load() {
  loading.value = true
  loadErr.value = ''
  try {
    setReport(await syncApi.strmRebasePreview())
  } catch (e) {
    loadErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

async function start() {
  if (!report.value) return
  const from = report.value.stale.filter((s) => picked.value[s.base]).map((s) => s.base)
  starting.value = true
  try {
    const d = await syncApi.strmRebase(from)
    job.value = { id: d.job_id, status: 'queued', message: d.message } as TaskJob
    poll()
  } catch (e) {
    toastError(e, '改写没能开始')
  } finally {
    starting.value = false
  }
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
  if (show.value && running.value) timer = setTimeout(poll, 1500)
}

// 每次打开都是新的一轮：上次的结果留着会让人以为还没改
watch(
  show,
  (v) => {
    if (!v) {
      clearTimeout(timer)
      return
    }
    if (running.value) return poll()
    job.value = null
    if (props.preload) setReport(props.preload)
    else void load()
  },
  { immediate: true },
)
onUnmounted(() => clearTimeout(timer))
</script>

<template>
  <HModal v-model:show="show" title="改写已有 STRM 的地址" width="640px">
    <div class="rebase">
      <p class="lead">
        <template v-if="afterSave">直连域名改了，已经生成的 STRM 里还是旧地址。</template>
        把本地已有 STRM 里的地址换成当前的直连域名，pickcode、后缀与文件名不变。
        只改本地文件，不请求 115，Emby 不用刷新；文件修改时间保持原样，Emby 里的排序不受影响。
      </p>

      <div v-if="loading" class="state">
        <HSpinner />
        <span>正在读本地 STRM…（库大、本地是网络盘时要一会儿）</span>
      </div>

      <HAlert v-else-if="loadErr" status="danger" title="没能读取">{{ loadErr }}</HAlert>

      <template v-else-if="job">
        <div v-if="running" class="state">
          <HSpinner />
          <span>
            {{ job.progress?.phase || job.message || '排队中…' }}
            <template v-if="job.progress?.total">（{{ job.progress.done }} / {{ job.progress.total }}）</template>
          </span>
        </div>
        <HAlert v-else-if="result && !result.failed" status="success" title="改好了">{{ job.message }}</HAlert>
        <HAlert v-else-if="result" status="warning" title="有一部分没改成">{{ job.message }}</HAlert>
        <HAlert v-else status="danger" title="没有完成">{{ job.message }}</HAlert>
      </template>

      <template v-else-if="report">
        <HAlert v-if="unsaved" status="warning" title="表单里的域名还没保存">
          下面按已保存的地址 <code>{{ report.target }}</code> 改写。要改成新填的地址，先保存配置再打开这里。
        </HAlert>

        <div class="target">
          改成 <code>{{ report.target }}</code>
        </div>

        <HAlert v-if="!report.stale_total" status="success" title="不用改">
          本地 {{ report.current }} 个本站 STRM 都已经是这个地址。
        </HAlert>

        <template v-else>
          <div class="groups">
            <div class="groups-title">写着旧地址的（勾选要改的）</div>
            <HCheckbox
              v-for="s in report.stale"
              :key="s.base"
              :checked="!!picked[s.base]"
              @update:checked="(v: boolean) => (picked[s.base] = v)"
            >
              <code>{{ s.base }}</code>
              <span class="count">{{ s.count }} 个</span>
            </HCheckbox>
          </div>

          <div v-if="report.samples.length" class="samples">
            <div class="groups-title">示例</div>
            <div v-for="x in report.samples" :key="x.rel" class="sample">
              <div class="rel">{{ x.rel }}</div>
              <code class="old">{{ x.old }}</code>
              <code class="new">{{ x.new }}</code>
            </div>
          </div>
        </template>

        <div class="summary">
          <span>台账视频 {{ report.total }}</span>
          <span>已是新地址 {{ report.current }}</span>
          <span v-if="report.foreign">非本站格式 {{ report.foreign }}（不碰）</span>
          <span v-if="report.missing">本地没有 {{ report.missing }}</span>
        </div>
        <p class="note">
          这里只换地址。改了「直连格式」或「保留文件后缀」的，要在「全量」页签把「本地已存在时」设为覆盖、跑一次全量。
        </p>
      </template>
    </div>

    <template #footer>
      <HButton variant="tertiary" @click="show = false">{{ job && !running ? '完成' : '关闭' }}</HButton>
      <HButton
        v-if="!job && report && report.stale_total"
        variant="primary"
        :disabled="!pickedCount"
        :loading="starting"
        @click="start"
      >
        改写 {{ pickedCount }} 个
      </HButton>
    </template>
  </HModal>
</template>

<style scoped>
.rebase {
  display: flex;
  flex-direction: column;
  gap: 12px;
  font-size: 13px;
}
.lead,
.note {
  margin: 0;
  color: var(--muted);
  line-height: 1.6;
}
.note {
  font-size: 12px;
}
.state {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 24px 0;
  color: var(--muted);
}
code {
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--foreground);
  word-break: break-all;
}
.target code {
  font-size: 13px;
  color: var(--accent);
}
.groups,
.samples {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.groups-title {
  font-size: 12px;
  color: var(--muted);
}
.count {
  margin-left: 8px;
  color: var(--muted);
}
.sample {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 8px 10px;
  border-radius: var(--r-sm);
  background: var(--surface-secondary);
}
.rel {
  font-size: 12px;
  color: var(--muted);
  word-break: break-all;
}
.old {
  color: var(--muted);
  text-decoration: line-through;
}
.summary {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 16px;
  color: var(--muted);
}
</style>
