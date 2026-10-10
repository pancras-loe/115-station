<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { ChevronRight, RotateCcw, X } from '@lucide/vue'
import HModal from '@/components/hero/HModal.vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HSkeleton from '@/components/hero/HSkeleton.vue'
import ProbeErrNote from '@/components/ui/ProbeErrNote.vue'
import { tasksApi } from '@/api'
import type { TaskJobDetail, TaskProbeItem } from '@/api/tasks'
import { toastError } from '@/composables/useFeedback'
import { untilText, useNow } from '@/composables/useNow'
import { useQueueStore } from '@/stores/queue'
import {
  JOB_STATUS,
  PROBE_ITEM_KIND,
  PROBE_REPORT_STATE,
  elapsed,
  jobKindText,
  jobSourceText,
  msgTone,
  pct,
  probeRetryReadyAt,
  resultIssues,
  resultSummary,
  retryable,
  subProgressText,
} from '@/utils/jobStatus'
import { fullTime } from '@/utils/time'

/**
 * 任务详情。打开的任务 id 由页面放在地址栏 ?job=，刷新 / 转发链接能直接打开。
 * 运行中与排队中的实时字段（进度、位置）取顶栏那条轮询里的同一条，不再单独轮询
 */
const props = defineProps<{ jobId: number | null }>()
const emit = defineEmits<{ close: []; records: [id: number] }>()
const queue = useQueueStore()

const show = computed({
  get: () => props.jobId != null,
  set: (v) => {
    if (!v) emit('close')
  },
})

const detail = ref<TaskJobDetail | null>(null)
const loading = ref(false)

async function load(id: number) {
  loading.value = true
  try {
    detail.value = (await tasksApi.detail(id)).data
  } catch (e) {
    detail.value = null
    toastError(e, '读取任务详情失败')
  } finally {
    loading.value = false
  }
}

watch(
  () => props.jobId,
  (id) => {
    detail.value = null
    if (id != null) void load(id)
  },
  { immediate: true },
)

// 这个任务跑完：重新拉一次，结果与涉及的记录都是结束时才定下来的
const off = queue.onFinished((j) => {
  if (j.id === props.jobId) void load(j.id)
})
onUnmounted(off)

/** 活的那份（进行中时进度在变）；已结束的就是详情本身 */
const job = computed(() => {
  const d = detail.value
  if (!d) return null
  const live = queue.jobs.find((j) => j.id === d.id)
  return live && (live.status === 'running' || live.status === 'queued') ? { ...d, ...live } : d
})

const RECORD_STATUS: Record<string, { text: string; color: 'default' | 'accent' | 'success' | 'warning' | 'danger' }> = {
  awaiting: { text: '待确认', color: 'warning' },
  success: { text: '成功', color: 'success' },
  exists: { text: '已存在', color: 'accent' },
  unrecognized: { text: '未识别', color: 'warning' },
  failed: { text: '失败', color: 'danger' },
}

// ---- Emby 探测任务 ----
// 进行中的报告随顶栏那条轮询（2 秒一轮）一起更新，这里不另起轮询

const now = useNow()

/** 探测报告的计数行：「片目 1/1 · 视频 3/12 · 成功 3 · 失败 1」 */
const probeCounts = computed(() => {
  const p = job.value?.probe
  if (!p) return ''
  const parts: string[] = []
  if (p.paths > 1) parts.push(`片目 ${p.finished}/${p.paths}`)
  if (p.planned) parts.push(`视频 ${p.ok + p.failed + (p.canceled ?? 0)}/${p.planned}`)
  if (p.ok) parts.push(`成功 ${p.ok}`)
  if (p.failed) parts.push(`失败 ${p.failed}`)
  if (p.errors) parts.push(`出错 ${p.errors}`)
  if (p.held) parts.push(`防抖跳过 ${p.held}`)
  if (p.missing) parts.push(`查不到 ${p.missing}`)
  if (p.canceled) parts.push(`停止后没探 ${p.canceled}`)
  if (!parts.length && p.state === 'done') parts.push('全部视频都已有媒体信息')
  return parts.join(' · ')
})

/** 失败 / 防抖跳过的条目还要等多久才能重试：重试按钮据此倒计时 */
const retryWait = computed(() => {
  const j = job.value
  if (!j || j.kind !== 'probe') return ''
  return untilText(probeRetryReadyAt(j.probe?.items, now.value), now.value)
})

/** 一个条目接下来会怎样 */
function probeNext(it: TaskProbeItem): string {
  const wait = untilText(it.retry_at, now.value)
  switch (it.kind) {
    case 'failed':
    case 'held': {
      const manual = wait ? `${wait}后可以手动重试（同一视频 ${debounceMin} 分钟内不重复请求）` : '现在可以手动重试'
      return it.auto_stopped ? `${manual}；自动探测次数已用完，入库后不会再自动探` : manual
    }
    case 'missing':
      return 'Emby 扫描入库后再试；入库确认后也会自动排进探测'
  }
  return ''
}
// 与后端 embyExtractDebounce 一致（embyextract.go）
const debounceMin = 5

async function retry() {
  if (!job.value) return
  await queue.retry(job.value.id)
  emit('close')
}
</script>

<template>
  <HModal v-model:show="show" :title="job?.title || '任务详情'" width="640px">
    <div v-if="loading && !job" class="skeleton">
      <HSkeleton v-for="i in 4" :key="i" class="sk-line" />
    </div>

    <div v-else-if="job" class="detail">
      <div class="chips">
        <HChip :color="JOB_STATUS[job.status]?.color ?? 'default'">{{ JOB_STATUS[job.status]?.text ?? job.status }}</HChip>
        <HChip>{{ jobKindText(job.kind) }}</HChip>
        <HChip>来源：{{ jobSourceText(job.source) }}</HChip>
        <HChip v-if="job.priority !== 0">后台优先级</HChip>
        <span class="id">#{{ job.id }}</span>
      </div>

      <p v-if="job.message" class="msg" :class="msgTone(job)">
        {{ job.message }}
      </p>

      <section class="block">
        <h3>时间</h3>
        <dl class="kv">
          <dt>提交</dt>
          <dd>{{ fullTime(job.created_at) }}</dd>
          <template v-if="job.started_at">
            <dt>开始</dt>
            <dd>{{ fullTime(job.started_at) }}</dd>
          </template>
          <template v-if="job.finished_at">
            <dt>结束</dt>
            <dd>{{ fullTime(job.finished_at) }}</dd>
          </template>
          <template v-if="job.started_at">
            <dt>{{ job.finished_at ? '用时' : '已运行' }}</dt>
            <dd>{{ elapsed(job) }}</dd>
          </template>
          <template v-if="job.status === 'queued'">
            <dt>排队</dt>
            <dd>
              第 {{ job.position }} 位
              <span v-if="(job.eta_sec ?? 0) >= 60">，预计约 {{ Math.round((job.eta_sec ?? 0) / 60) }} 分钟内跑完</span>
            </dd>
          </template>
        </dl>
      </section>

      <section v-if="job.progress && (job.progress.phase || job.progress.total)" class="block">
        <h3>{{ job.status === 'running' ? '进度' : '最后进度' }}</h3>
        <div v-if="job.progress.total" class="bar"><span :style="{ width: `${pct(job)}%` }" /></div>
        <p class="line">
          <span v-if="job.progress.phase">{{ job.progress.phase }}</span>
          <span v-if="job.progress.total"> {{ job.progress.done }}/{{ job.progress.total }}</span>
          <span v-if="job.progress.label" class="dim">：{{ job.progress.label }}</span>
        </p>
        <p v-if="subProgressText(job)" class="line dim">└ {{ subProgressText(job) }}</p>
      </section>

      <section v-if="resultSummary(job)" class="block">
        <h3>结果</h3>
        <p class="line">{{ resultSummary(job) }}</p>
      </section>

      <section v-if="resultIssues(job).length" class="block">
        <h3>问题（{{ resultIssues(job).length }}）</h3>
        <ul class="plain issues">
          <li v-for="(s, i) in resultIssues(job)" :key="i">{{ s }}</li>
        </ul>
        <p class="dim">最多列 50 条，完整的在实时日志里（搜「[影视刮削]」）。</p>
      </section>

      <section v-if="job.probe" class="block">
        <h3>探测结果 · {{ PROBE_REPORT_STATE[job.probe.state] ?? job.probe.state }}</h3>
        <p v-if="probeCounts" class="line">{{ probeCounts }}</p>
        <p v-if="job.probe.paused_until" class="line warn">
          连续失败，整个探测队列暂停到 {{ fullTime(job.probe.paused_until) }}（可能是 115 风控或 Emby 异常），之后自动接着探
        </p>
        <ul v-if="job.probe.items?.length" class="probe-items">
          <li v-for="(it, i) in job.probe.items" :key="i" class="probe-item">
            <div class="pi-head">
              <HChip size="sm" :color="PROBE_ITEM_KIND[it.kind]?.color ?? 'default'">
                {{ PROBE_ITEM_KIND[it.kind]?.text ?? it.kind }}
              </HChip>
              <span class="pi-label">{{ it.label || '—' }}</span>
              <span v-if="it.attempts" class="dim">累计请求 {{ it.attempts }} 次</span>
            </div>
            <p v-if="it.err" class="pi-line">{{ it.kind === 'held' ? `刚请求过，上次结果：${it.err}` : it.err }}</p>
            <ProbeErrNote :err="it.err" />
            <p v-if="probeNext(it)" class="pi-line dim">{{ probeNext(it) }}</p>
          </li>
        </ul>
        <p v-if="job.probe.more" class="dim">另有 {{ job.probe.more }} 条没列出，见实时日志（搜「[Emby探测]」）。</p>
        <p class="dim">
          手动探测不受自动探测的次数限制，只防抖：同一个视频 {{ debounceMin }} 分钟内不重复请求（每次都要取一次 115 直链）。
          重试会按原来的片目重新检查，已经探好的自动跳过。每集的状态也可以在「海报墙」的片目详情里看。
        </p>
      </section>

      <section v-if="job.params_summary?.length" class="block">
        <h3>参数</h3>
        <ul class="plain">
          <li v-for="s in job.params_summary" :key="s">{{ s }}</li>
        </ul>
      </section>

      <section v-if="job.kind !== 'probe'" class="block">
        <h3>涉及的整理记录{{ job.record_total ? `（${job.record_total}）` : '' }}</h3>
        <p v-if="!job.records?.length" class="dim">
          {{ job.status === 'queued' || job.status === 'running' ? '任务结束后显示。' : '这次任务没有产生或处理整理记录。' }}
        </p>
        <ul v-else class="recs">
          <li v-for="r in job.records" :key="r.id" class="rec">
            <HChip :color="RECORD_STATUS[r.status]?.color ?? 'default'">{{ RECORD_STATUS[r.status]?.text ?? r.status }}</HChip>
            <span class="rec-title" :title="r.source">
              {{ r.title ? `${r.title}${r.year ? ` (${r.year})` : ''}` : r.source }}
            </span>
          </li>
        </ul>
      </section>
    </div>

    <template #footer>
      <HButton v-if="job?.stoppable" variant="tertiary" @click="queue.cancel(job.id)">
        <template #icon><X /></template>
        {{ job.status === 'queued' ? '取消' : '停止' }}
      </HButton>
      <span v-if="job && retryable(job) && retryWait" class="retry-wait">防抖中，{{ retryWait }}后可重试</span>
      <HButton v-if="job && retryable(job)" variant="tertiary" :disabled="!!retryWait" @click="retry">
        <template #icon><RotateCcw /></template>
        重试
      </HButton>
      <HButton v-if="job?.record_total" variant="primary" @click="emit('records', job.id)">
        在整理记录中查看
        <ChevronRight :size="15" />
      </HButton>
    </template>
  </HModal>
</template>

<style scoped>
.skeleton {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.sk-line {
  height: 18px;
}
.detail {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.chips {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
}
.id {
  margin-left: auto;
  font-size: 12px;
  color: var(--muted);
}
.msg {
  margin: 0;
  padding: 10px 12px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  font-size: 13px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-all;
  color: var(--foreground);
}
.msg.err {
  color: var(--danger);
  background: var(--danger-soft);
}
.msg.warn {
  color: var(--warning);
  background: var(--warning-soft);
}
.line.warn {
  color: var(--warning);
}
.probe-items {
  list-style: none;
  margin: 8px 0 6px;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
  max-height: 280px;
  overflow-y: auto;
}
.probe-item {
  min-width: 0;
}
.pi-head {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.pi-label {
  min-width: 0;
  font-size: 13px;
  color: var(--foreground);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.pi-line {
  margin: 2px 0 0;
  font-size: 12.5px;
  color: var(--foreground);
  word-break: break-all;
}
.pi-line.dim {
  color: var(--muted);
}
.retry-wait {
  margin-right: auto;
  font-size: 12.5px;
  color: var(--muted);
}
.block h3 {
  margin: 0 0 6px;
  font-size: 12.5px;
  font-weight: 500;
  color: var(--muted);
}
.kv {
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 4px 14px;
  margin: 0;
  font-size: 13px;
}
.kv dt {
  color: var(--muted);
}
.kv dd {
  margin: 0;
  color: var(--foreground);
}
.line {
  margin: 4px 0 0;
  font-size: 13px;
  color: var(--foreground);
  word-break: break-all;
}
.dim {
  margin: 0;
  font-size: 12.5px;
  color: var(--muted);
}
.plain {
  margin: 0;
  padding-left: 18px;
  font-size: 13px;
  color: var(--foreground);
}
.issues {
  max-height: 240px;
  overflow-y: auto;
  margin-bottom: 6px;
  font-size: 12.5px;
  word-break: break-all;
}
.bar {
  height: 4px;
  border-radius: 999px;
  background: var(--default);
  overflow: hidden;
}
.bar > span {
  display: block;
  height: 100%;
  background: var(--accent);
  transition: width 300ms ease;
}
.recs {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
  max-height: 260px;
  overflow-y: auto;
}
.rec {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  padding: 4px 0;
}
.rec-title {
  min-width: 0;
  font-size: 13px;
  color: var(--foreground);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
</style>
