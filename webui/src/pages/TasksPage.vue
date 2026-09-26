<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import HTabs from '@/components/hero/HTabs.vue'
import JobsTab from './tasks/JobsTab.vue'
import JobDetail from './tasks/JobDetail.vue'
import RecordsTab from './tasks/RecordsTab.vue'
import { recordStats } from '@/stores/recordStats'
import { useTabQuery } from '@/composables/useTabQuery'

/**
 * 任务中心：日常要处理的都在这一页 —— 队列里的任务，以及整理产出的记录。
 * 顶栏的任务弹层只管「现在怎么样」，历史、筛选、详情在这里。
 *
 * 地址栏：?tab= 页签；?job= 打开的任务详情（刷新 / 转发链接能直接打开）；
 * 整理记录页签另认 ?status=（筛选档）与 ?job_id=（只看某个任务涉及的记录）。
 * 整理记录原在「自动整理」页，旧地址 /organize?tab=records 重定向到这里
 */
const route = useRoute()
const router = useRouter()
const tab = useTabQuery('jobs')

const tabs = computed(() => [
  { value: 'jobs', label: '任务' },
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

/** 详情里「在整理记录中查看」：切到记录页签，只看这个任务涉及的记录 */
function showRecords(id: number) {
  void router.push({ query: { tab: 'records', job_id: String(id) } })
}
</script>

<template>
  <div class="page">
    <HTabs v-model="tab" :items="tabs" />
    <div class="panel">
      <JobsTab v-if="tab === 'jobs'" @open="openJob" />
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
