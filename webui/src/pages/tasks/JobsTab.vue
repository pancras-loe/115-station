<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ChevronRight, X } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import { syncApi, tasksApi } from '@/api'
import type { IncrStatus } from '@/api/sync'
import type { ProbeStatus, TaskJob } from '@/api/tasks'
import { useNow } from '@/composables/useNow'
import { useQueueStore } from '@/stores/queue'
import HistoryRow from './HistoryRow.vue'
import { JOB_STATUS, dur, elapsed, jobKindText, jobSourceText, laneText, pct, subProgressText } from '@/utils/jobStatus'
import { fullTime, relTime } from '@/utils/time'

/**
 * 任务中心 · 进行中页签：概况 / 进行中 / 最近结束。
 * 历史与 Emby 探测失败清单各自单独成页签（原来都叠在这一页，历史要滚过三张卡片才看得到），
 * 这里只各放一个入口：概况里的探测状态、「最近结束」的几条与失败计数。
 * 进行中直接读顶栏那条轮询（stores/queue.ts），不另起轮询；探测状态由 TasksPage 统一轮询后传进来
 */
const props = defineProps<{ probe: ProbeStatus | null; probeVisible: boolean }>()
const emit = defineEmits<{ open: [id: number]; goto: [tab: string, status?: string] }>()
const queue = useQueueStore()
const now = useNow()

// ---- 概况：锁 + 增量 + 探测 ----
const incr = ref<IncrStatus | null>(null)
async function loadIncr() {
  try {
    incr.value = await syncApi.incrStatus()
  } catch {
    // 状态条拉不到不影响下面的列表
  }
}

const running = computed(() => queue.jobs.filter((j) => j.status === 'running'))
/** 主队列上正在跑的：刮削单独一条队列、不拿任务锁，「锁被谁占着」只看主队列 */
const runningMain = computed(() => running.value.filter((j) => j.kind !== 'scrape' && j.kind !== 'probe'))
const queued = computed(() => queue.jobs.filter((j) => j.status === 'queued'))

/** 锁被不在队列里的东西占着（增量轮询、Emby 事件深删）：进行中列表里单独一行 */
const outside = computed(() => {
  if (runningMain.value.length || !queue.lock.busy || (queue.lock.held_sec ?? 0) < 3) return null
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

/** 探测只放一句话，详情与失败清单在「Emby 探测」页签 */
const probeText = computed(() => {
  const s = props.probe
  if (!s) return '—'
  if (!s.emby) return '没有配置 Emby'
  const t = s.paused_until ? new Date(s.paused_until).getTime() : 0
  if (t > now.value) return `连续失败，暂停到 ${fullTime(s.paused_until)}`
  if (s.running) return `正在探测${s.queue ? ` · 还有 ${s.queue} 个片目排队` : ''}`
  if (s.queue) return `${s.queue} 个片目排队中`
  return s.enabled ? '空闲' : '自动探测未开启'
})

// ---- 最近结束 ----
const RECENT = 5
const recent = ref<TaskJob[]>([])
const counts = ref<Record<string, number>>({})
async function loadRecent() {
  try {
    const d = await tasksApi.history({ status: 'all', page: 1, size: RECENT })
    recent.value = d.data ?? []
    counts.value = d.counts ?? {}
  } catch {
    // 拉不到只是少了这一块，历史页签里会报错
  }
}
const troubled = computed(() => (counts.value.failed ?? 0) + (counts.value.interrupted ?? 0))

async function retry(j: TaskJob) {
  await queue.retry(j.id)
}

let offFinished: (() => void) | undefined
let timer: number | undefined
onMounted(() => {
  void loadIncr()
  void loadRecent()
  // 有任务跑完，最近结束就变了；增量状态 15 秒一刷，和顶栏空闲时的轮询同一节奏
  offFinished = queue.onFinished(() => {
    void loadRecent()
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
    <SectionCard title="概况" hint="同一时间只有一个任务在动网盘；整理、同步、全量、深删都排这一把锁">
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
        <div v-if="probeVisible" class="state-item is-link" role="link" tabindex="0" @click="emit('goto', 'probe')" @keydown.enter="emit('goto', 'probe')">
          <dt>Emby 提前探测<ChevronRight class="go" /></dt>
          <dd>
            {{ probeText }}<span v-if="probe?.fail_total" class="warn"> · 失败 {{ probe.fail_total }}</span>
          </dd>
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
              <p v-if="subProgressText(j)" class="sub dim">└ {{ subProgressText(j) }}</p>
            </template>
            <p v-else class="sub">
              {{ laneText(j.kind) }}第 {{ j.position }} 位
              <span v-if="j.priority !== 0" class="dim"> · 手动提交的会排在它前面</span>
              <span v-if="(j.eta_sec ?? 0) >= 60"> · 预计约 {{ Math.round((j.eta_sec ?? 0) / 60) }} 分钟内跑完</span>
              <span v-if="j.position === 1 && !laneText(j.kind) && !runningMain.length && queue.lock.busy" class="dim">
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

    <SectionCard title="最近结束">
      <template #extra>
        <div class="extra">
          <HButton v-if="troubled" size="sm" variant="ghost" class="danger-text" @click="emit('goto', 'history', 'failed')">
            失败 / 中断 {{ troubled }}
          </HButton>
          <HButton size="sm" variant="ghost" @click="emit('goto', 'history')">
            全部历史<span v-if="counts.all" class="n">{{ counts.all }}</span>
            <ChevronRight class="go" />
          </HButton>
        </div>
      </template>
      <EmptyState v-if="!recent.length" text="还没有已结束的任务" />
      <ul v-else class="plain">
        <HistoryRow v-for="j in recent" :key="j.id" :job="j" @open="emit('open', $event)" @retry="retry" />
      </ul>
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
.state-item.is-link {
  cursor: pointer;
  transition: background 150ms ease;
}
.state-item.is-link:hover {
  background: color-mix(in oklab, var(--surface-secondary), var(--foreground) 6%);
}
.state-item.is-link dt {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.state-item .warn {
  color: var(--warning);
}
.go {
  width: 14px;
  height: 14px;
  vertical-align: -2px;
  color: var(--muted);
}
.extra {
  display: flex;
  align-items: center;
  gap: 4px;
}
.n {
  margin-left: 4px;
  color: var(--muted);
}
.danger-text {
  color: var(--danger);
}

.list,
.plain {
  list-style: none;
  margin: 0;
  padding: 0;
}
.list {
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

@media (max-width: 720px) {
  .meta {
    display: none;
  }
}
</style>
