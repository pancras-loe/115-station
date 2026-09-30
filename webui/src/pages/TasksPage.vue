<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import HTabs from '@/components/hero/HTabs.vue'
import JobsTab from './tasks/JobsTab.vue'
import HistoryTab from './tasks/HistoryTab.vue'
import ProbeTab from './tasks/ProbeTab.vue'
import JobDetail from './tasks/JobDetail.vue'
import RecordsTab from './tasks/RecordsTab.vue'
import { tasksApi } from '@/api'
import type { ProbeStatus } from '@/api/tasks'
import { recordStats } from '@/stores/recordStats'
import { useQueueStore } from '@/stores/queue'
import { useTabQuery } from '@/composables/useTabQuery'

/**
 * 任务中心：日常要处理的都在这一页 —— 队列里的任务、任务历史、Emby 提前探测的失败清单，以及整理产出的记录。
 * 顶栏的任务弹层只管「现在怎么样」，历史、筛选、详情在这里。
 *
 * 页签：jobs 进行中（值沿用旧名，收藏的 ?tab=jobs 仍然有效）/ history 历史 / probe Emby 探测 / records 整理记录。
 * 历史与探测原来叠在「任务」一页里，数据一多历史要滚一屏多才看得到，所以拆开。
 *
 * 地址栏：?tab= 页签；?job= 打开的任务详情（刷新 / 转发链接能直接打开）；
 * 历史页签打开时认一次 ?status=；整理记录页签另认 ?status=（筛选档）与 ?job_id=（只看某个任务涉及的记录）。
 * 整理记录原在「自动整理」页，旧地址 /organize?tab=records 重定向到这里
 */
const route = useRoute()
const router = useRouter()
const queue = useQueueStore()
const tab = useTabQuery('jobs')

// ---- Emby 提前探测状态：页签角标、「进行中」的概况、探测页签三处共用，这里统一轮询 ----
const probe = ref<ProbeStatus | null>(null)
async function loadProbe() {
  try {
    probe.value = await tasksApi.probeStatus()
  } catch {
    // 拉不到不影响页面其余部分
  }
}
/** 没开、没配、也没有失败 / 忽略记录的：不给页签，别占地方 */
const probeVisible = computed(() => {
  const s = probe.value
  return !!s && (s.enabled || s.queue > 0 || !!s.running || s.fail_total > 0 || s.ignored_total > 0)
})

const tabs = computed(() => [
  { value: 'jobs', label: '进行中', count: queue.running + queue.queued },
  { value: 'history', label: '历史' },
  ...(probeVisible.value || tab.value === 'probe'
    ? [{ value: 'probe', label: 'Emby 探测', count: probe.value?.fail_total ?? 0, countTone: 'warning' as const }]
    : []),
  { value: 'records', label: '整理记录', count: recordStats.value.awaiting || 0, countTone: 'warning' as const },
])

const jobId = computed(() => {
  const n = Number(route.query.job)
  return Number.isInteger(n) && n > 0 ? n : null
})

function openJob(id: number) {
  void router.replace({ query: { ...route.query, job: String(id) } })
}

function closeJob() {
  const { job: _job, ...rest } = route.query
  void router.replace({ query: rest })
}

/** 「进行中」页签里的入口：跳到历史（可带状态档）或探测页签 */
function goto(t: string, status?: string) {
  void router.replace({ query: status ? { tab: t, status } : { tab: t } })
}

/** 详情里「在整理记录中查看」：切到记录页签，只看这个任务涉及的记录 */
function showRecords(id: number) {
  void router.push({ query: { tab: 'records', job_id: String(id) } })
}

let timer: number | undefined
let offFinished: (() => void) | undefined
onMounted(() => {
  void loadProbe()
  timer = window.setInterval(loadProbe, 15_000)
  // 探测任务跑完，失败清单会变
  offFinished = queue.onFinished((j) => {
    if (j.kind === 'probe') void loadProbe()
  })
})
onUnmounted(() => {
  if (timer !== undefined) clearInterval(timer)
  offFinished?.()
})
</script>

<template>
  <div class="page">
    <HTabs v-model="tab" :items="tabs" />
    <div class="panel">
      <JobsTab v-if="tab === 'jobs'" :probe="probe" :probe-visible="probeVisible" @open="openJob" @goto="goto" />
      <HistoryTab v-else-if="tab === 'history'" @open="openJob" />
      <ProbeTab v-else-if="tab === 'probe'" :st="probe" @changed="loadProbe" />
      <RecordsTab v-else-if="tab === 'records'" @open-job="openJob" />
    </div>
    <JobDetail :job-id="jobId" @close="closeJob" @records="showRecords" />
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 16px;
  min-width: 0;
}
.panel {
  min-width: 0;
}
</style>
