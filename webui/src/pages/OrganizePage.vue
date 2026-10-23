<script setup lang="ts">
import LibScope from '@/components/LibScope.vue'
import { computed } from 'vue'
import HTabs from '@/components/hero/HTabs.vue'
import BasicTab from './organize/BasicTab.vue'
import ScrapeSummary from './organize/ScrapeSummary.vue'
import RecognizeTab from './organize/RecognizeTab.vue'
import AiTab from './organize/AiTab.vue'
import RenameTab from './organize/RenameTab.vue'
import OrganizeOverview from './organize/OrganizeOverview.vue'
import WashTab from './organize/WashTab.vue'
import SchemesTab from './organize/SchemesTab.vue'
import { useTabQuery } from '@/composables/useTabQuery'

const tab = useTabQuery('basic')
// 整理记录已搬到任务中心（/tasks?tab=records）：这一页只剩配置。
// 旧地址 /organize?tab=records 由路由重定向过去，见 router/index.ts

const tabs = computed(() => [
  { value: 'basic', label: '基础配置' },
  { value: 'scrape', label: '影视刮削' },
  { value: 'recognize', label: '识别规则' },
  { value: 'ai', label: 'AI 增强识别' },
  { value: 'rename', label: '重命名策略' },
  { value: 'category', label: '二级分类策略' },
  { value: 'wash', label: '洗版策略' },
])
</script>

<template>
  <div class="page">
    <!-- 页签全是配置：顶部一行先说现在怎么样（下次定时 / 上一轮 / 待确认） -->
    <OrganizeOverview @goto="(t) => (tab = t)" />
    <HTabs v-model="tab" :items="tabs" />

    <!-- 页签内容按需挂载：没选中的页签不渲染、不发请求（与原 NTabPane 的 display-directive="if" 一致） -->
    <!-- 多库时说清这一页的配置管到哪些库（LibScope 自己判断显不显示） -->
    <LibScope
      v-if="tab === 'basic' || tab === 'category' || tab === 'wash'"
      :key="tab"
      :kind="tab === 'basic' ? 'organize' : tab"
    />
    <div class="panel">
      <BasicTab v-if="tab === 'basic'" />
      <!-- 刮削配置已搬到「影视刮削」页，这里只读，改要跳过去 -->
      <ScrapeSummary v-else-if="tab === 'scrape'" />
      <RecognizeTab v-else-if="tab === 'recognize'" />
      <AiTab v-else-if="tab === 'ai'" />
      <RenameTab v-else-if="tab === 'rename'" />
      <SchemesTab v-else-if="tab === 'category'" key="category" />
      <WashTab v-else-if="tab === 'wash'" key="wash" />
    </div>
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
