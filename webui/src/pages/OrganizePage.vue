<script setup lang="ts">
import { NTabPane, NTabs } from 'naive-ui'
import BasicTab from './organize/BasicTab.vue'
import ScrapeTab from './organize/ScrapeTab.vue'
import RecognizeTab from './organize/RecognizeTab.vue'
import GptTab from './organize/GptTab.vue'
import RenameTab from './organize/RenameTab.vue'
import YamlRuleTab from './organize/YamlRuleTab.vue'
import VariablesTab from './organize/VariablesTab.vue'
import CategoryHelpTab from './organize/CategoryHelpTab.vue'
import WashHelpTab from './organize/WashHelpTab.vue'
import { organizeApi } from '@/api'
import { DEFAULT_CATEGORY_YAML, DEFAULT_WASH_YAML } from './organize/defaultRules'
import { useTabQuery } from '@/composables/useTabQuery'

const tab = useTabQuery('basic')
</script>

<template>
  <NTabs v-model:value="tab" type="line" animated>
    <NTabPane name="basic" tab="基础配置"><BasicTab /></NTabPane>
    <NTabPane name="scrape" tab="影视刮削"><ScrapeTab /></NTabPane>
    <NTabPane name="recognize" tab="识别规则"><RecognizeTab /></NTabPane>
    <NTabPane name="gpt" tab="GPT 识别"><GptTab /></NTabPane>
    <NTabPane name="rename" tab="重命名策略"><RenameTab /></NTabPane>

    <NTabPane name="category" tab="二级分类策略" display-directive="if">
      <YamlRuleTab
        title="二级分类策略"
        hint="分类名即 115 目录名"
        note="分类名即 115 目录名，整理时目录不存在会自动创建。YAML 对格式要求严格，多余空格或缩进错误会导致保存失败。"
        :load="organizeApi.getCategories"
        :persist="organizeApi.saveCategories"
        :fallback="DEFAULT_CATEGORY_YAML"
      />
    </NTabPane>

    <NTabPane name="wash" tab="洗版策略" display-directive="if">
      <YamlRuleTab
        title="洗版策略"
        hint="同一影视多版本时的取舍规则"
        note="按优先级字段决定保留哪个版本。mode 决定共存 / 跳过 / 替换，scope 决定是全局只留一个还是按分辨率分组各留一个。"
        :load="organizeApi.getWash"
        :persist="organizeApi.saveWash"
        :fallback="DEFAULT_WASH_YAML"
      />
    </NTabPane>

    <NTabPane name="variables" tab="重命名规则"><VariablesTab /></NTabPane>
    <NTabPane name="category-help" tab="分类规则"><CategoryHelpTab /></NTabPane>
    <NTabPane name="wash-help" tab="洗版规则"><WashHelpTab /></NTabPane>
  </NTabs>
</template>
