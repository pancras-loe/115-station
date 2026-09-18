<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NInput, NRadioButton, NRadioGroup } from 'naive-ui'
import SectionCard from '@/components/ui/SectionCard.vue'
import SecretInput from '@/components/ui/SecretInput.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import TestBanner, { type BannerState } from '@/components/ui/TestBanner.vue'
import { configApi } from '@/api'
import { toastError, useFeedback } from '@/composables/useFeedback'

const { message } = useFeedback()

const DEFAULTS = {
  api_url: 'https://api.tmdb.org',
  image_url: 'https://image.tmdb.org',
  api_key: '',
  language: 'zh-CN',
}

const model = ref({ ...DEFAULTS })
const saving = ref(false)
const testing = ref(false)
const banner = ref<BannerState | null>(null)

async function load() {
  try {
    const res = await configApi.getTmdb()
    const c = res.data ?? res
    model.value = {
      api_url: c.api_url || DEFAULTS.api_url,
      image_url: c.image_url || DEFAULTS.image_url,
      api_key: c.api_key || '',
      language: c.language || DEFAULTS.language,
    }
  } catch {
    // 首次使用尚无配置，保持默认值
  }
}

async function save() {
  saving.value = true
  try {
    await configApi.saveTmdb(model.value)
    message.success('保存成功')
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}

async function reset() {
  model.value = { ...DEFAULTS }
  await save()
}

async function test() {
  testing.value = true
  banner.value = { status: 'pending', title: '正在连接 TMDB…' }
  try {
    const d = await configApi.testTmdb()
    banner.value = d.ok
      ? { status: 'ok', title: 'TMDB 连接成功', trailing: d.latency_ms ? `${d.latency_ms}ms` : undefined }
      : { status: 'err', title: 'TMDB 连接失败', detail: d.error }
  } catch (e) {
    banner.value = { status: 'err', title: 'TMDB 连接失败', detail: e instanceof Error ? e.message : '' }
  } finally {
    testing.value = false
  }
}

onMounted(load)
</script>

<template>
  <SectionCard title="TMDB 配置" hint="识别与刮削的元数据来源">
    <FieldRow label="API 域名" tip="TMDB API 地址，国内网络可换反代加速。">
      <NInput v-model:value="model.api_url" placeholder="https://api.tmdb.org" />
    </FieldRow>

    <FieldRow label="图片域名" tip="TMDB 海报图片域名，可换反代加速。">
      <NInput v-model:value="model.image_url" placeholder="https://image.tmdb.org" />
    </FieldRow>

    <FieldRow
      label="API 密钥"
      required
      tip="TMDB API Key（themoviedb.org 注册获取），识别与搜片必填。"
    >
      <SecretInput v-model="model.api_key" name="tmdb-api-key" placeholder="TMDB API Key" />
    </FieldRow>

    <FieldRow label="语言" tip="中文优先返回中文片名与简介；英文返回原名。">
      <NRadioGroup v-model:value="model.language">
        <NRadioButton value="zh-CN">中文</NRadioButton>
        <NRadioButton value="en-US">英文</NRadioButton>
      </NRadioGroup>
    </FieldRow>

    <FormActions>
      <NButton type="primary" :loading="saving" @click="save">保存配置</NButton>
      <NButton :loading="testing" @click="test">测试连接</NButton>
      <NButton @click="reset">重置配置</NButton>
    </FormActions>

    <TestBanner :state="banner" />
  </SectionCard>
</template>
