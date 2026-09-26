<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ChevronRight, ListChecks, RotateCcw, X } from '@lucide/vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HPopover from '@/components/hero/HPopover.vue'
import HTooltip from '@/components/hero/HTooltip.vue'
import { useQueueStore } from '@/stores/queue'
import type { TaskJob } from '@/api/tasks'
import { JOB_STATUS, dur, elapsed, pct, retryable } from '@/utils/jobStatus'

/**
 * 顶栏的任务队列入口：图标 + 角标（执行中 + 排队数），点开看执行中 / 排队中 / 最近结束。
 * 只管「现在怎么样」；完整历史、筛选、详情与「清理已结束」在任务中心（/tasks）。
 * 轮询在这里启停 —— 顶栏常驻，所以整站只有这一条队列轮询，不依赖某个页面挂着。
 */
const queue = useQueueStore()
const router = useRouter()
const open = ref(false)

onMounted(() => queue.start())
onUnmounted(() => queue.stop())

const badge = computed(() => queue.running + queue.queued)
const runningJobs = computed(() => queue.jobs.filter((j) => j.status === 'running'))
const queuedJobs = computed(() => queue.jobs.filter((j) => j.status === 'queued'))
const doneJobs = computed(() => queue.finished.slice(0, 20))

const STATUS = JOB_STATUS

/** 排在第一位、而锁被别的任务占着：说清楚在等谁（多半是定时整理或增量同步） */
const waitingFor = computed(() => {
  if (runningJobs.value.length || !queue.lock.busy || !queue.lock.holder) return ''
  return `${queue.lock.holder}（已运行 ${dur(queue.lock.held_sec ?? 0)}）`
})

/** 可不可以取消 / 停止以后端为准：单条任务执行中不能停（中途打断会留下搬了一半的中间态） */
function stoppable(j: TaskJob) {
  return !!j.stoppable
}

/** 进任务中心；带 id 时直接打开那条任务的详情 */
function openCenter(id?: number) {
  open.value = false
  void router.push({ name: 'tasks', query: id ? { job: String(id) } : {} })
}

/**
 * 锁被不在队列里的后台任务占着（定时整理、转存触发、增量轮询）：面板上单独显示一行。
 * 占用不到 3 秒的不显示 —— 增量轮询 30 秒一轮、多数一两秒就完，每轮闪一下没有意义
 */
const background = computed(() => {
  if (runningJobs.value.length || !queue.lock.busy || (queue.lock.held_sec ?? 0) < 3) return null
  return { title: queue.lock.holder || '后台任务', since: dur(queue.lock.held_sec ?? 0), progress: queue.lock.progress }
})
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
          <HButton size="sm" variant="ghost" class="qp-clear" @click="openCenter()">
            查看全部
            <ChevronRight :size="14" />
          </HButton>
        </div>

        <p v-if="!queue.jobs.length && !background" class="qp-empty">
          没有任务。整理、重新整理、同步等操作提交后会在这里排队依次执行。
        </p>

        <ul class="qp-list">
          <li v-if="background" class="qp-item">
            <div class="qp-row">
              <HChip color="accent">后台</HChip>
              <span class="qp-title" :title="background.title">{{ background.title }}</span>
            </div>
            <p class="qp-sub">
              <span v-if="background.progress">{{ background.progress }}</span>
              <span class="qp-dim"> · 已运行 {{ background.since }}</span>
            </p>
          </li>
          <li v-for="j in [...runningJobs, ...queuedJobs, ...doneJobs]" :key="j.id" class="qp-item">
            <div class="qp-row">
              <HChip :color="STATUS[j.status]?.color ?? 'default'">{{ STATUS[j.status]?.text ?? j.status }}</HChip>
              <button type="button" class="qp-title qp-link" :title="`${j.title}（查看详情）`" @click="openCenter(j.id)">
                {{ j.title }}
              </button>
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
              <span v-if="j.priority !== 0" class="qp-dim"> · 后台任务，手动提交的会排在它前面</span>
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
.qp-link {
  padding: 0;
  border: 0;
  background: none;
  font: inherit;
  font-size: 13px;
  text-align: left;
  cursor: pointer;
}
.qp-link:hover {
  color: var(--accent);
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
