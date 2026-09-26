<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ListChecks, RotateCcw, X } from '@lucide/vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HPopover from '@/components/hero/HPopover.vue'
import HTooltip from '@/components/hero/HTooltip.vue'
import { useQueueStore } from '@/stores/queue'
import type { TaskJob } from '@/api/tasks'

/**
 * 顶栏的任务队列入口：图标 + 角标（执行中 + 排队数），点开看执行中 / 排队中 / 最近结束。
 * 轮询在这里启停 —— 顶栏常驻，所以整站只有这一条队列轮询，不依赖某个页面挂着。
 */
const queue = useQueueStore()
const open = ref(false)

onMounted(() => queue.start())
onUnmounted(() => queue.stop())

const badge = computed(() => queue.running + queue.queued)
const runningJobs = computed(() => queue.jobs.filter((j) => j.status === 'running'))
const queuedJobs = computed(() => queue.jobs.filter((j) => j.status === 'queued'))
const doneJobs = computed(() => queue.finished.slice(0, 20))

const STATUS: Record<string, { text: string; color: 'default' | 'accent' | 'success' | 'warning' | 'danger' }> = {
  running: { text: '执行中', color: 'accent' },
  queued: { text: '排队中', color: 'default' },
  success: { text: '完成', color: 'success' },
  failed: { text: '失败', color: 'danger' },
  canceled: { text: '已取消', color: 'warning' },
  interrupted: { text: '中断', color: 'danger' },
}

/** 排在第一位、而锁被别的任务占着：说清楚在等谁（多半是定时整理或增量同步） */
const waitingFor = computed(() => {
  if (runningJobs.value.length || !queue.lock.busy || !queue.lock.holder) return ''
  return `${queue.lock.holder}（已运行 ${dur(queue.lock.held_sec ?? 0)}）`
})

function dur(sec: number) {
  if (sec < 60) return `${Math.max(0, Math.round(sec))} 秒`
  const m = Math.floor(sec / 60)
  if (m < 60) return `${m} 分 ${Math.round(sec % 60)} 秒`
  return `${Math.floor(m / 60)} 小时 ${m % 60} 分`
}

function elapsed(j: TaskJob) {
  if (!j.started_at) return ''
  const end = j.finished_at ? new Date(j.finished_at).getTime() : Date.now()
  return dur((end - new Date(j.started_at).getTime()) / 1000)
}

function pct(j: TaskJob) {
  const p = j.progress
  if (!p || !p.total) return 0
  return Math.min(100, Math.round((p.done / p.total) * 100))
}

/** 单条任务执行中不能停（中途打断会留下搬了一半的中间态），批量的可以做完手上这条再停 */
function stoppable(j: TaskJob) {
  return j.status === 'queued' || (j.status === 'running' && (j.record_ids?.length ?? 0) > 1)
}

function retryable(j: TaskJob) {
  return j.status === 'failed' || j.status === 'interrupted' || j.status === 'canceled'
}
</script>

<template>
  <HPopover v-model:open="open" side="bottom" align="end" content-class="queue-pop">
    <span class="queue-trigger">
      <HTooltip content="任务队列" side="bottom">
        <HButton variant="ghost" icon-only aria-label="任务队列">
          <ListChecks :size="18" />
        </HButton>
      </HTooltip>
      <span v-if="badge > 0" class="queue-badge" :class="{ 'is-running': queue.running > 0 }">{{ badge }}</span>
    </span>

    <template #content>
      <div class="qp">
        <div class="qp-head">
          <b>任务队列</b>
          <span class="qp-dim">执行中 {{ queue.running }} · 排队 {{ queue.queued }}</span>
          <HButton v-if="doneJobs.length" size="sm" variant="ghost" class="qp-clear" @click="queue.clearFinished()">
            清理已结束
          </HButton>
        </div>

        <p v-if="!queue.jobs.length" class="qp-empty">
          没有任务。重新整理、确认入库提交后会在这里排队依次执行。
        </p>

        <ul class="qp-list">
          <li v-for="j in [...runningJobs, ...queuedJobs, ...doneJobs]" :key="j.id" class="qp-item">
            <div class="qp-row">
              <HChip :color="STATUS[j.status]?.color ?? 'default'">{{ STATUS[j.status]?.text ?? j.status }}</HChip>
              <span class="qp-title" :title="j.title">{{ j.title }}</span>
              <HButton
                v-if="stoppable(j)"
                size="sm"
                variant="ghost"
                icon-only
                :aria-label="j.status === 'queued' ? '取消' : '停止'"
                :title="j.status === 'queued' ? '取消（未执行）' : '停止：当前这一条做完即退出'"
                @click="queue.cancel(j.id)"
              >
                <X :size="14" />
              </HButton>
              <HButton
                v-if="retryable(j)"
                size="sm"
                variant="ghost"
                icon-only
                aria-label="重试"
                title="按原参数重新加入队列"
                @click="queue.retry(j.id)"
              >
                <RotateCcw :size="14" />
              </HButton>
            </div>

            <template v-if="j.status === 'running'">
              <div v-if="j.progress?.total" class="qp-bar"><span :style="{ width: `${pct(j)}%` }" /></div>
              <p class="qp-sub">
                <span v-if="j.progress?.phase">{{ j.progress.phase }}</span>
                <span v-if="j.progress?.total"> {{ j.progress.done }}/{{ j.progress.total }}</span>
                <span v-if="j.progress?.label" class="qp-label">：{{ j.progress.label }}</span>
                <span class="qp-dim"> · 已运行 {{ elapsed(j) }}</span>
              </p>
            </template>

            <p v-else-if="j.status === 'queued'" class="qp-sub">
              第 {{ j.position }} 位
              <span v-if="(j.eta_sec ?? 0) >= 60"> · 预计约 {{ Math.round((j.eta_sec ?? 0) / 60) }} 分钟内跑完</span>
              <span v-if="j.position === 1 && waitingFor" class="qp-dim"> · 正在等 {{ waitingFor }}</span>
            </p>

            <p v-else class="qp-sub" :class="{ 'qp-err': j.status === 'failed' || j.status === 'interrupted' }">
              {{ j.message || '—' }}
              <span v-if="j.started_at" class="qp-dim"> · 用时 {{ elapsed(j) }}</span>
            </p>
          </li>
        </ul>
      </div>
    </template>
  </HPopover>
</template>

<style scoped>
.queue-trigger {
  position: relative;
  display: inline-flex;
}
.queue-badge {
  position: absolute;
  top: 2px;
  right: 2px;
  min-width: 16px;
  height: 16px;
  padding: 0 4px;
  border-radius: 999px;
  background: var(--default);
  color: var(--foreground);
  font-size: 10.5px;
  line-height: 16px;
  text-align: center;
  pointer-events: none;
}
.queue-badge.is-running {
  background: var(--accent);
  color: var(--accent-foreground);
}
</style>

<!-- 面板在 Portal 里，scoped 选不中 -->
<style>
.queue-pop {
  width: min(420px, calc(100vw - 24px));
  max-height: min(70vh, 560px);
  overflow-y: auto;
  padding: 12px;
}
.qp-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
  font-size: 14px;
  color: var(--foreground);
}
.qp-clear {
  margin-left: auto;
}
.qp-dim {
  color: var(--muted);
  font-size: 12px;
}
.qp-empty {
  margin: 8px 0;
  font-size: 12.5px;
  color: var(--muted);
}
.qp-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.qp-item {
  padding: 8px 10px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.qp-row {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.qp-title {
  flex: 1;
  min-width: 0;
  font-size: 13px;
  color: var(--foreground);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.qp-sub {
  margin: 4px 0 0;
  font-size: 12px;
  color: color-mix(in oklab, var(--foreground) 70%, var(--muted));
  word-break: break-all;
}
.qp-err {
  color: var(--danger);
}
.qp-bar {
  margin-top: 6px;
  height: 4px;
  border-radius: 999px;
  background: var(--default);
  overflow: hidden;
}
.qp-bar > span {
  display: block;
  height: 100%;
  background: var(--accent);
  transition: width 300ms ease;
}
</style>
