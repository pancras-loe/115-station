<script setup lang="ts">
import { computed } from 'vue'
import { NButton, NInput, NRadioButton, NRadioGroup } from 'naive-ui'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import CopyBox from '@/components/ui/CopyBox.vue'
import { useSetting } from '@/composables/useSetting'

// keep_ext 后端存的是字符串 'true'/'false' 而不是布尔（旧版遗留），
// 这里保持原样，改成布尔会让老配置读出来是 undefined
const { model, saving, save, reset } = useSetting('strm', {
  domain: '',
  format: 'pick_code_name',
  keep_ext: 'true',
  exist: 'overwrite',
})

const example = computed(() => {
  const domain = model.value.domain.replace(/\/+$/, '')
  if (!domain) return ''
  const pickcode = 'abchrb6gnrw0hhh80'
  const id = model.value.keep_ext === 'true' ? `${pickcode}.mkv` : pickcode
  // pick_code:      /d/{pickcode}[.ext]
  // pick_code_name: /d/{pickcode}[.ext]?/{原文件名}（?/ 后的名字供播放器识别容器）
  return model.value.format === 'pick_code'
    ? `${domain}/d/${id}`
    : `${domain}/d/${id}?/钢铁侠.2008.1080p.BluRay.X264.DTS-TnT.mkv`
})
</script>

<template>
  <SectionCard title="STRM 配置" hint="直链域名与格式">
    <FieldRow
      label="STRM 直连域名"
      required
      tip="STRM 内直链的域名，需指向 302 代理端口（http://NAS_IP:6086）。播放时反代会按客户端实际访问地址自动改写，此项为兜底值。"
    >
      <NInput v-model:value="model.domain" placeholder="http://172.17.0.1:6060" />
    </FieldRow>

    <FieldRow
      label="STRM 直连格式"
      tip="pick_code_name = 直链带原文件名（播放器显示友好）；pick_code = 仅文件 ID（最短）。"
    >
      <NRadioGroup v-model:value="model.format">
        <NRadioButton value="pick_code_name">pick_code_name</NRadioButton>
        <NRadioButton value="pick_code">pick_code</NRadioButton>
      </NRadioGroup>
    </FieldRow>

    <FieldRow
      label="保留文件后缀"
      tip="是 = 直链 ID 段带 .mkv 等后缀（播放器据 URL 识别容器格式）；否 = 纯 ID。"
    >
      <NRadioGroup v-model:value="model.keep_ext">
        <NRadioButton value="true">是</NRadioButton>
        <NRadioButton value="false">否</NRadioButton>
      </NRadioGroup>
    </FieldRow>

    <FieldRow
      label="本地已存在时"
      tip="本地已有同名 STRM：覆盖 = 重新生成（推荐）；跳过 = 只生成一次。"
    >
      <NRadioGroup v-model:value="model.exist">
        <NRadioButton value="overwrite">覆盖</NRadioButton>
        <NRadioButton value="skip">跳过</NRadioButton>
      </NRadioGroup>
    </FieldRow>

    <FieldRow
      v-if="example"
      label="STRM 直链示例"
      wide
      tip="按当前配置实时预览生成的直链样式，切换选项立即更新。"
    >
      <CopyBox :value="example" tone="primary" />
    </FieldRow>

    <FormActions>
      <NButton type="primary" :loading="saving" @click="save()">保存配置</NButton>
      <NButton @click="reset">重置配置</NButton>
    </FormActions>
  </SectionCard>
</template>
