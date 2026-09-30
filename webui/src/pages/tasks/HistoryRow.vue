<script setup lang="ts">
import { computed } from 'vue'
import { RotateCcw } from '@lucide/vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import type { TaskJob } from '@/api/tasks'
import { JOB_STATUS, elapsed, jobKindText, jobSourceText, msgTone, resultSummary, retryable } from '@/utils/jobStatus'
import { fullTime, relTime } from '@/utils/time'

/**
 * 已结束任务的一行：「历史」页签与「进行中」页签的「最近结束」共用。
 * clock = 只显示时分：历史页签按天分组，组头已经写了日期，行里再写「3 小时前」是重复
 */
const props = defineProps<{ job: TaskJob; clock?: boolean }>()
const emit = defineEmits<{ open: [id: number]; retry: [job: TaskJob] }>()

const at = computed(() => props.job.finished_at || props.job.started_at || props.job.created_at)
const when = computed(() => {
  if (!props.clock) return relTime(at.value)
  const d = new Date(at.value)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false })
})
const summary = computed(() => resultSummary(props.job))
const text = computed(() => [summary.value, props.job.message].filter(Boolean).join(' · '))
</script>

<template>
  <li class="h-row">
    <HChip size="sm" class="h-chip" :color="JOB_STATUS[job.status]?.color ?? 'default'">
      {{ JOB_STATUS[job.status]?.text ?? job.status }}
    </HChip>
    <div class="h-main">
      <button type="button" class="h-title" :title="job.title" @click="emit('open', job.id)">{{ job.title }}</button>
      <p v-if="text" class="h-msg" :class="msgTone(job)" :title="text">
        <span v-if="summary" class="h-result">{{ summary }}</span>
        <span v-if="summary && job.message"> · </span>
        <span class="h-text">{{ job.message }}</span>
      </p>
    </div>
    <div class="h-aside">
      <span :title="fullTime(at)">{{ when }}</span>
      <span class="h-meta">
        {{ jobKindText(job.kind) }} · {{ jobSourceText(job.source) }}<template v-if="job.started_at"> · {{ elapsed(job) }}</template>
      </span>
    </div>
    <div class="h-act">
      <HButton v-if="retryable(job)" size="sm" variant="ghost" title="按原参数重新加入队列" @click="emit('retry', job)">
        <template #icon><RotateCcw /></template>
        重试
      </HButton>
    </div>
  </li>
</template>

<style scoped>
.h-row {
  display: grid;
  grid-template-columns: 4.5em minmax(0, 1fr) auto 5.5em;
  align-items: center;
  gap: 4px 12px;
  padding: 9px 4px;
  border-top: 1px solid var(--separator);
}
.h-row:first-child {
  border-top: 0;
}
.h-chip {
  justify-self: start;
}
.h-main {
  min-width: 0;
}
.h-title {
  display: block;
  max-width: 100%;
  padding: 0;
  border: 0;
  background: none;
  cursor: pointer;
  text-align: left;
  font: inherit;
  font-size: 13.5px;
  font-weight: 500;
  color: var(--foreground);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.h-title:hover {
  color: var(--accent);
}
/* 结果说明最多两行：失败原因常常是一长串接口报错，全文在悬浮提示和任务详情里 */
.h-msg {
  margin: 2px 0 0;
  font-size: 12.5px;
  color: color-mix(in oklab, var(--foreground) 72%, var(--muted));
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  word-break: break-all;
}
.h-msg.err .h-text {
  color: var(--danger);
}
.h-msg.warn .h-text {
  color: var(--warning);
}
.h-result {
  color: var(--foreground);
}
.h-aside {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 2px;
  font-size: 12.5px;
  color: var(--foreground);
  white-space: nowrap;
}
.h-meta {
  font-size: 12px;
  color: var(--muted);
}
.h-act {
  display: flex;
  justify-content: flex-end;
}

/* 手机：状态与标题一行，说明占整行，时间 / 类型 / 重试挪到最下面一行 */
@media (max-width: 720px) {
  .h-row {
    grid-template-columns: auto minmax(0, 1fr) auto;
    grid-template-areas:
      'chip main main'
      'aside aside act';
    padding: 10px 0;
  }
  .h-chip {
    grid-area: chip;
    align-self: start;
  }
  .h-main {
    grid-area: main;
  }
  .h-aside {
    grid-area: aside;
    flex-direction: row;
    align-items: center;
    gap: 8px;
    white-space: normal;
  }
  .h-aside > span:first-child {
    white-space: nowrap;
  }
  .h-act {
    grid-area: act;
  }
}
</style>
