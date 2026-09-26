<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { RefreshCw, RotateCcw, Trash2, X } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HPagination from '@/components/hero/HPagination.vue'
import HPopconfirm from '@/components/hero/HPopconfirm.vue'
import HSearchField from '@/components/hero/HSearchField.vue'
import HSelect from '@/components/hero/HSelect.vue'
import HTabs from '@/components/hero/HTabs.vue'
import HTooltip from '@/components/hero/HTooltip.vue'
import { syncApi, tasksApi } from '@/api'
import type { IncrStatus } from '@/api/sync'
import type { TaskJob } from '@/api/tasks'
import { toastError } from '@/composables/useFeedback'
import { useQueueStore } from '@/stores/queue'
import {
  JOB_KIND,
  JOB_SOURCE,
  JOB_STATUS,
  dur,
  elapsed,
  jobKindText,
  jobSourceText,
  pct,
  resultSummary,
  retryable,
} from '@/utils/jobStatus'
import { fullTime, relTime } from '@/utils/time'

/**
 * 任务中心 · 任务页签：当前状态 / 进行中 / 历史。
 * 进行中直接读顶栏那条轮询（stores/queue.ts），不另起轮询；历史只在这一页打开时查
 * （GET /tasks/history），任务结束时顺手刷新一次
 */
const emit = defineEmits<{ open: [id: number] }>()
const queue = useQueueStore()

// ---- 当前状态：锁 + 增量 ----
const incr = ref<IncrStatus | null>(null)
async function loadIncr() {
  try {
    incr.value = await syncApi.incrStatus()
  } catch {
    // 状态条拉不到不影响下面的列表
  }
}

const running = computed(() => queue.jobs.filter((j) => j.status === 'running'))
const queued = computed(() => queue.jobs.filter((j) => j.status === 'queued'))

/** 锁被不在队列里的东西占着（增量轮询、Emby 事件深删）：进行中列表里单独一行 */
const outside = computed(() => {
  if (running.value.length || !queue.lock.busy || (queue.lock.held_sec ?? 0) < 3) return null
  return { title: queue.lock.holder || '后台任务', since: dur(queue.lock.held_sec ?? 0), progress: queue.lock.progress }
})

const lockText = computed(() => {
  if (!queue.lock.busy) return '空闲'
  return `${queue.lock.holder || '未知任务'} · 已占用 ${dur(queue.lock.held_sec ?? 0)}`
})

const incrText = computed(() => {
  const s = incr.value
  if (!s) return '—'
  if (!s.last_round?.at) return '还没跑过'
  const sum = s.last_round.summary
  let t = relTime(s.last_round.at)
  if (s.last_round.error) t += ` · 出错：${s.last_round.error}`
  else if (sum?.interrupted) t += ` · 给${sum.yielded_to || '别的任务'}让路，下一轮接着做`
  return t
})

// ---- 历史 ----
const rows = ref<TaskJob[]>([])
const total = ref(0)
const counts = ref<Record<string, number>>({})
const page = ref(1)
const size = ref(20)
const status = ref('all')
const kind = ref('')
const source = ref('')
const keyword = ref('')
const loading = ref(false)

const STATUS_TABS = ['all', 'success', 'failed', 'interrupted', 'canceled'] as const
const statusTabs = computed(() =>
  STATUS_TABS.map((k) => ({
    value: k,
    label: k === 'all' ? '全部' : JOB_STATUS[k].text,
    count: counts.value[k] ?? 0,
    countTone: k === 'failed' || k === 'interrupted' ? ('danger' as const) : ('accent' as const),
  })),
)
const KIND_OPTIONS = [{ label: '全部类型', value: '' }, ...Object.entries(JOB_KIND).map(([value, label]) => ({ label, value }))]
const SOURCE_OPTIONS = [
  { label: '全部来源', value: '' },
  ...Object.entries(JOB_SOURCE).map(([value, label]) => ({ label, value })),
]

async function loadHistory() {
  loading.value = true
  try {
    const d = await tasksApi.history({
      status: status.value,
      kind: kind.value,
      source: source.value,
      q: keyword.value.trim(),
      page: page.value,
      size: size.value,
    })
    rows.value = d.data ?? []
    total.value = d.total ?? 0
    counts.value = d.counts ?? {}
  } catch (e) {
    toastError(e, '读取任务历史失败')
  } finally {
    loading.value = false
  }
}

function refilter() {
  page.value = 1
  void loadHistory()
}

function onPage(p: number) {
  page.value = p
  void loadHistory()
}

function onSize(n: number) {
  size.value = n
  refilter()
}

function pickStatus(v: string) {
  status.value = v
  refilter()
}

function reload() {
  void loadHistory()
  void loadIncr()
  void queue.poll()
}

async function clearAll() {
  await queue.clearFinished()
  refilter()
}

async function retry(j: TaskJob) {
  await queue.retry(j.id)
}

let offFinished: (() => void) | undefined
let timer: number | undefined
onMounted(() => {
  void loadHistory()
  void loadIncr()
  // 有任务跑完，历史第一页就变了；增量状态 15 秒一刷，和顶栏空闲时的轮询同一节奏
  offFinished = queue.onFinished(() => {
    void loadHistory()
  })
  timer = window.setInterval(loadIncr, 15_000)
})
onUnmounted(() => {
  offFinished?.()
  if (timer !== undefined) clearInterval(timer)
})
</script>

<template>
  <div class="stack">
    <SectionCard title="当前状态" hint="同一时间只有一个任务在动网盘；整理、同步、全量、深删都排这一把锁">
      <dl class="state">
        <div class="state-item">
          <dt>任务锁</dt>
          <dd :class="{ 'is-busy': queue.lock.busy }">{{ lockText }}</dd>
        </div>
        <div class="state-item">
          <dt>队列</dt>
          <dd>执行中 {{ queue.running }} · 排队 {{ queue.queued }}</dd>
        </div>
        <div class="state-item">
          <dt>增量同步上一轮</dt>
          <dd :title="fullTime(incr?.last_round?.at)">{{ incrText }}</dd>
        </div>
        <div v-if="(incr?.stall?.rounds ?? 0) > 0" class="state-item">
          <dt>增量重放</dt>
          <dd class="is-warn">连续 {{ incr?.stall.rounds }} 轮没消费：{{ incr?.stall.reason }}</dd>
        </div>
      </dl>
    </SectionCard>

    <SectionCard title="进行中" :hint="`执行中 ${queue.running} · 排队 ${queue.queued}`">
      <EmptyState
        v-if="!running.length && !queued.length && !outside"
        text="没有进行中的任务。整理、重新整理、同步等操作提交后会在这里排队依次执行。"
      />
      <ul v-else class="list">
        <li v-if="outside" class="row">
          <div class="row-main">
            <div class="row-head">
              <HChip color="accent">后台</HChip>
              <span class="title">{{ outside.title }}</span>
            </div>
            <p class="sub">
              <span v-if="outside.progress">{{ outside.progress }} · </span>
              <span class="dim">已运行 {{ outside.since }}（不在队列里，跑完自动放锁）</span>
            </p>
          </div>
        </li>
        <li v-for="j in [...running, ...queued]" :key="j.id" class="row">
          <div class="row-main">
            <div class="row-head">
              <HChip :color="JOB_STATUS[j.status]?.color ?? 'default'">{{ JOB_STATUS[j.status]?.text ?? j.status }}</HChip>
              <button type="button" class="title link" :title="j.title" @click="emit('open', j.id)">{{ j.title }}</button>
              <span class="meta">
                {{ jobKindText(j.kind) }} · {{ jobSourceText(j.source) }}{{ j.priority !== 0 ? ' · 后台优先级' : '' }}
              </span>
            </div>
            <template v-if="j.status === 'running'">
              <div v-if="j.progress?.total" class="bar"><span :style="{ width: `${pct(j)}%` }" /></div>
              <p class="sub">
                <span v-if="j.progress?.phase">{{ j.progress.phase }}</span>
                <span v-if="j.progress?.total"> {{ j.progress.done }}/{{ j.progress.total }}</span>
                <span v-if="j.progress?.label">：{{ j.progress.label }}</span>
                <span class="dim"> · 已运行 {{ elapsed(j) }}</span>
              </p>
            </template>
            <p v-else class="sub">
              第 {{ j.position }} 位
              <span v-if="j.priority !== 0" class="dim"> · 手动提交的会排在它前面</span>
              <span v-if="(j.eta_sec ?? 0) >= 60"> · 预计约 {{ Math.round((j.eta_sec ?? 0) / 60) }} 分钟内跑完</span>
              <span v-if="j.position === 1 && !running.length && queue.lock.busy" class="dim">
                · 正在等 {{ queue.lock.holder }}
              </span>
              <span class="dim"> · 提交于 {{ relTime(j.created_at) }}</span>
            </p>
          </div>
          <HButton
            v-if="j.stoppable"
            size="sm"
            variant="ghost"
            :title="j.status === 'queued' ? '取消（未执行）' : '停止：当前这一条做完即退出'"
            @click="queue.cancel(j.id)"
          >
            <template #icon><X /></template>
            {{ j.status === 'queued' ? '取消' : '停止' }}
          </HButton>
        </li>
      </ul>
    </SectionCard>

    <SectionCard
      title="历史"
      hint="成功与取消的保留 7 天，失败与中断保留 30 天；什么都没做的定时整理不留记录"
    >
      <HTabs :model-value="status" :items="statusTabs" class="filters" @update:model-value="pickStatus" />

      <div class="toolbar">
        <div class="sel">
          <HSelect v-model="kind" :options="KIND_OPTIONS" aria-label="任务类型" @update:model-value="refilter" />
        </div>
        <div class="sel">
          <HSelect v-model="source" :options="SOURCE_OPTIONS" aria-label="提交来源" @update:model-value="refilter" />
        </div>
        <HSearchField v-model="keyword" class="kw" placeholder="标题 / 结果说明" @search="refilter" />
        <HTooltip content="刷新">
          <HButton variant="ghost" icon-only :loading="loading" aria-label="刷新" @click="reload">
            <RefreshCw />
          </HButton>
        </HTooltip>
        <span class="grow" />
        <HPopconfirm danger confirm-text="清理" :disabled="!counts.all" @confirm="clearAll">
          <HButton variant="danger-soft" :disabled="!counts.all">
            <template #icon><Trash2 /></template>
            清理已结束
          </HButton>
          <template #content>删除全部 {{ counts.all ?? 0 }} 条已结束任务的历史（排队中与执行中的不受影响）。整理记录不会被删。</template>
        </HPopconfirm>
      </div>

      <div class="list-wrap" :class="{ 'is-loading': loading }">
        <EmptyState v-if="!rows.length && !loading" text="没有符合条件的任务" />
        <ul v-else class="list">
          <li v-for="j in rows" :key="j.id" class="row">
            <div class="row-main">
              <div class="row-head">
                <HChip :color="JOB_STATUS[j.status]?.color ?? 'default'">{{ JOB_STATUS[j.status]?.text ?? j.status }}</HChip>
                <button type="button" class="title link" :title="j.title" @click="emit('open', j.id)">{{ j.title }}</button>
                <span class="meta">{{ jobKindText(j.kind) }} · {{ jobSourceText(j.source) }}</span>
              </div>
              <p class="sub" :class="{ err: j.status === 'failed' || j.status === 'interrupted' }">
                <span v-if="resultSummary(j)" class="result">{{ resultSummary(j) }}</span>
                <span v-if="resultSummary(j) && j.message"> · </span>
                <span class="msg">{{ j.message || (resultSummary(j) ? '' : '—') }}</span>
              </p>
              <p class="sub dim">
                <span :title="fullTime(j.started_at || j.created_at)">{{ relTime(j.started_at || j.created_at) }}</span>
                <span v-if="j.started_at"> · 用时 {{ elapsed(j) }}</span>
                <span v-if="j.record_ids?.length"> · {{ j.record_ids.length }} 条记录</span>
              </p>
            </div>
            <HButton v-if="retryable(j)" size="sm" variant="ghost" title="按原参数重新加入队列" @click="retry(j)">
              <template #icon><RotateCcw /></template>
              重试
            </HButton>
          </li>
        </ul>
      </div>

      <HPagination
        v-if="total > size"
        :page="page"
        :page-size="size"
        :total="total"
        class="pager"
        @update:page="onPage"
        @update:page-size="onSize"
      />
    </SectionCard>
  </div>
</template>

<style scoped>
.stack {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.state {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 10px;
  margin: 0;
}
.state-item {
  padding: 10px 12px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  min-width: 0;
}
.state-item dt {
  font-size: 12px;
  color: var(--muted);
}
.state-item dd {
  margin: 4px 0 0;
  font-size: 13.5px;
  color: var(--foreground);
  word-break: break-all;
}
.state-item dd.is-busy {
  color: var(--accent);
}
.state-item dd.is-warn {
  color: var(--warning);
}

.filters {
  margin-bottom: 12px;
}
.toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
  flex-wrap: wrap;
}
.sel {
  width: 132px;
}
.kw {
  width: 240px;
  max-width: 100%;
}
.grow {
  flex: 1;
}

.list-wrap {
  transition: opacity 150ms ease;
}
.list-wrap.is-loading {
  opacity: 0.55;
  pointer-events: none;
}
.list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 12px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.row-main {
  flex: 1;
  min-width: 0;
}
.row-head {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.title {
  min-width: 0;
  flex: 0 1 auto;
  font-size: 13.5px;
  font-weight: 500;
  color: var(--foreground);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.link {
  padding: 0;
  border: 0;
  background: none;
  cursor: pointer;
  text-align: left;
  font: inherit;
  font-size: 13.5px;
  font-weight: 500;
}
.link:hover {
  color: var(--accent);
}
.meta {
  flex-shrink: 0;
  font-size: 12px;
  color: var(--muted);
}
.sub {
  margin: 4px 0 0;
  font-size: 12.5px;
  color: color-mix(in oklab, var(--foreground) 72%, var(--muted));
  word-break: break-all;
}
.sub.err .msg {
  color: var(--danger);
}
.result {
  color: var(--foreground);
}
.dim,
.sub.dim {
  color: var(--muted);
  font-size: 12px;
}
.bar {
  margin-top: 6px;
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
.pager {
  margin-top: 12px;
}

@media (max-width: 720px) {
  .meta {
    display: none;
  }
  .sel {
    flex: 1;
    width: auto;
  }
  .kw {
    width: 100%;
  }
}
</style>
