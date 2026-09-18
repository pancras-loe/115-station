<script setup lang="ts">
import { ref } from 'vue'
import { NAlert, NButton, NInput } from 'naive-ui'
import SectionCard from '@/components/ui/SectionCard.vue'
import SecretInput from '@/components/ui/SecretInput.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import TestBanner, { type BannerState } from '@/components/ui/TestBanner.vue'
import { configApi } from '@/api'
import { useSetting } from '@/composables/useSetting'
import { useFeedback } from '@/composables/useFeedback'

const { message } = useFeedback()
const { model, saving, save, reset } = useSetting('org-gpt', {
  url: 'https://api.siliconflow.cn/v1',
  key: '',
  model: '',
})

const banner = ref<BannerState | null>(null)
const testing = ref(false)

async function test() {
  if (!model.value.url || !model.value.model) {
    message.warning('请先填写 API 地址和模型名称')
    return
  }
  testing.value = true
  banner.value = { status: 'pending', title: '正在连接大模型接口…' }
  try {
    const d = await configApi.testGpt({ url: model.value.url, key: model.value.key, model: model.value.model })
    banner.value = d.ok
      ? { status: 'ok', title: '连接成功', trailing: d.latency_ms ? `${d.latency_ms}ms` : undefined }
      : { status: 'err', title: '连接失败', detail: d.error }
  } catch (e) {
    banner.value = { status: 'err', title: '连接失败', detail: e instanceof Error ? e.message : '' }
  } finally {
    testing.value = false
  }
}
</script>

<template>
  <SectionCard title="GPT 识别" hint="TMDB 识别失败时的兜底">
    <NAlert class="note" type="info" :bordered="false">
      当 TMDB 无法识别媒体信息时，调用大模型辅助识别。API 地址需支持 OpenAI 协议标准。
    </NAlert>

    <FieldRow label="API 地址" tip="支持 OpenAI 协议标准的模型接口地址。">
      <NInput v-model:value="model.url" placeholder="如 https://api.siliconflow.cn/v1" />
    </FieldRow>
    <FieldRow label="API 密钥" tip="大模型 API 密钥，推荐使用硅基流动等国内平台。">
      <SecretInput v-model="model.key" name="gpt-api-key" placeholder="sk-xxx" />
    </FieldRow>
    <FieldRow label="模型名称" tip="用于识别的模型名称，如 Qwen2.5-7B、gpt-4o-mini 等。">
      <NInput v-model:value="model.model" placeholder="如 Qwen2.5-7B" />
    </FieldRow>

    <FormActions>
      <NButton type="primary" :loading="saving" @click="save()">保存配置</NButton>
      <NButton :loading="testing" @click="test">测试连接</NButton>
      <NButton @click="reset">重置配置</NButton>
    </FormActions>
    <TestBanner :state="banner" />
  </SectionCard>
</template>

<style scoped>
.note {
  margin-bottom: 12px;
}
</style>
