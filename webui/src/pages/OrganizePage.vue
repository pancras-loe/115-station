<script setup lang="ts">
import { computed, onMounted } from 'vue'
import HTabs from '@/components/hero/HTabs.vue'
import BasicTab from './organize/BasicTab.vue'
import RecordsTab from './organize/RecordsTab.vue'
import ScrapeTab from './organize/ScrapeTab.vue'
import RecognizeTab from './organize/RecognizeTab.vue'
import AiTab from './organize/AiTab.vue'
import RenameTab from './organize/RenameTab.vue'
import EnrichTab from './organize/EnrichTab.vue'
import YamlRuleTab from './organize/YamlRuleTab.vue'
import { organizeApi } from '@/api'
import { DEFAULT_CATEGORY_YAML } from './organize/defaultRules'
import { useTabQuery } from '@/composables/useTabQuery'
import { recordStats, refreshRecordStats } from './organize/recordStats'

const tab = useTabQuery('basic')
// 开着人工确认时，待确认的条目要在哪个页签都看得见，不然只能靠用户自己想起来去翻记录
onMounted(refreshRecordStats)

const tabs = computed(() => [
  { value: 'basic', label: '基础配置' },
  { value: 'records', label: '整理记录', count: recordStats.value.awaiting || 0, countTone: 'warning' as const },
  { value: 'scrape', label: '影视刮削' },
  { value: 'recognize', label: '识别规则' },
  { value: 'ai', label: 'AI 增强识别' },
  { value: 'rename', label: '重命名策略' },
  { value: 'category', label: '二级分类策略' },
  { value: 'wash', label: '洗版策略' },
  { value: 'enrich', label: '媒体补全' },
])
</script>

<template>
  <div class="page">
    <HTabs v-model="tab" :items="tabs" />

    <!-- 页签内容按需挂载：没选中的页签不渲染、不发请求（与原 NTabPane 的 display-directive="if" 一致） -->
    <div class="panel">
      <BasicTab v-if="tab === 'basic'" />
      <RecordsTab v-else-if="tab === 'records'" />
      <ScrapeTab v-else-if="tab === 'scrape'" />
      <RecognizeTab v-else-if="tab === 'recognize'" />
      <AiTab v-else-if="tab === 'ai'" />
      <RenameTab v-else-if="tab === 'rename'" />
      <YamlRuleTab
        v-else-if="tab === 'category'"
        key="category"
        kind="category"
        title="二级分类策略"
        hint="分类名即 115 目录名"
        note="分类名即 115 目录名，整理时目录不存在会自动创建。规则从上往下匹配，先命中的先停；不想手写 YAML 就用右上角的「添加分类规则」。"
        :load="organizeApi.getCategories"
        :persist="organizeApi.saveCategories"
        :fallback="DEFAULT_CATEGORY_YAML"
      />
      <YamlRuleTab
        v-else-if="tab === 'wash'"
        key="wash"
        kind="wash"
        title="洗版策略"
        hint="同一影视多版本时的取舍规则"
        note="按优先级字段决定保留哪个版本。mode 决定共存 / 跳过 / 替换，scope 决定是全局只留一个还是按分辨率分组各留一个；字段含义见右上角「规则说明」。"
        :load="organizeApi.getWash"
        :persist="organizeApi.saveWash"
      />
      <EnrichTab v-else-if="tab === 'enrich'" />
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
