<script setup lang="ts">
import { NButton, NInput } from 'naive-ui'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import { useSetting } from '@/composables/useSetting'

const { model, saving, save, reset } = useSetting('org-recognize', {
  replace_rules: '',
  release_groups: '',
  min_size: '0',
})
</script>

<template>
  <SectionCard title="识别规则" hint="文件名预处理与过滤">
    <FieldRow
      label="替换规则"
      tip="识别前对视频文件名做字符串 / 正则替换，一行一个。"
      hint="格式：原文本=>替换后文本"
    >
      <NInput
        v-model:value="model.replace_rules"
        type="textarea"
        :rows="3"
        placeholder="一行一个，格式：原文本=>替换后文本"
      />
    </FieldRow>

    <FieldRow
      label="发布组"
      tip="用于扩展未收录的发布组名称，一行一个（已集成常见的 PT 发布组）。"
    >
      <NInput v-model:value="model.release_groups" type="textarea" :rows="3" placeholder="一行一个，如：WiKi" />
    </FieldRow>

    <FieldRow
      label="最小视频大小"
      tip="单位 MB，小于该大小的视频文件跳过识别与整理，0 表示不限制。可过滤封面视频、广告等小文件。"
    >
      <NInput v-model:value="model.min_size" placeholder="0">
        <template #suffix>MB</template>
      </NInput>
    </FieldRow>

    <FormActions>
      <NButton type="primary" :loading="saving" @click="save()">保存配置</NButton>
      <NButton @click="reset">重置配置</NButton>
    </FormActions>
  </SectionCard>
</template>
