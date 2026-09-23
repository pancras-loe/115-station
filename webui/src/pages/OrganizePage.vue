<script setup lang="ts">
import { onMounted } from 'vue'
import { NBadge, NTabPane, NTabs } from 'naive-ui'
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
</script>

<template>
  <NTabs v-model:value="tab" type="line" animated>
    <NTabPane name="basic" tab="基础配置"><BasicTab /></NTabPane>
    <NTabPane name="records" display-directive="if">
      <template #tab>
        <NBadge
          :value="recordStats.awaiting || 0"
          :show="(recordStats.awaiting || 0) > 0"
          type="warning"
          :max="99"
          :offset="[10, -2]"
        >
          整理记录
        </NBadge>
      </template>
      <RecordsTab />
    </NTabPane>
    <NTabPane name="scrape" tab="影视刮削"><ScrapeTab /></NTabPane>
    <NTabPane name="recognize" tab="识别规则"><RecognizeTab /></NTabPane>
    <NTabPane name="ai" tab="AI 增强识别"><AiTab /></NTabPane>
    <NTabPane name="rename" tab="重命名策略"><RenameTab /></NTabPane>

    <NTabPane name="category" tab="二级分类策略" display-directive="if">
      <YamlRuleTab
        kind="category"
        title="二级分类策略"
        hint="分类名即 115 目录名"
        note="分类名即 115 目录名，整理时目录不存在会自动创建。规则从上往下匹配，先命中的先停；不想手写 YAML 就用右上角的「添加分类规则」。"
        :load="organizeApi.getCategories"
        :persist="organizeApi.saveCategories"
        :fallback="DEFAULT_CATEGORY_YAML"
      />
    </NTabPane>

    <NTabPane name="wash" tab="洗版策略" display-directive="if">
      <YamlRuleTab
        kind="wash"
        title="洗版策略"
        hint="同一影视多版本时的取舍规则"
        note="按优先级字段决定保留哪个版本。mode 决定共存 / 跳过 / 替换，scope 决定是全局只留一个还是按分辨率分组各留一个；字段含义见右上角「规则说明」。"
        :load="organizeApi.getWash"
        :persist="organizeApi.saveWash"
      />
    </NTabPane>

    <NTabPane name="enrich" tab="媒体补全" display-directive="if"><EnrichTab /></NTabPane>
  </NTabs>
</template>
