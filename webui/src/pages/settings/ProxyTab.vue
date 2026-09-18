<script setup lang="ts">
import { ref } from 'vue'
import { NButton, NInput } from 'naive-ui'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import TestBanner, { type BannerState } from '@/components/ui/TestBanner.vue'
import { configApi } from '@/api'
import { useSetting } from '@/composables/useSetting'
import { useFeedback } from '@/composables/useFeedback'

const { message } = useFeedback()
const { model, saving, save, reset } = useSetting('proxy', { url: '' })

const banner = ref<BannerState | null>(null)
/** 网络连接测试一次探多个目标，所以单独一组横幅，不复用 banner */
const netResults = ref<BannerState[] | null>(null)
const testingProxy = ref(false)
const testingNet = ref(false)

async function testProxy() {
  const url = model.value.url.trim()
  if (!url) {
    message.warning('请先填写代理地址')
    return
  }
  testingProxy.value = true
  netResults.value = null
  banner.value = { status: 'pending', title: '正在通过代理探测 google_204…' }
  try {
    const d = await configApi.testProxy(url)
    banner.value = d.ok
      ? { status: 'ok', title: '代理可用', trailing: d.latency_ms ? `${d.latency_ms}ms` : undefined }
      : { status: 'err', title: '代理不可用', detail: d.error || '连接失败' }
  } catch (e) {
    banner.value = { status: 'err', title: '代理不可用', detail: e instanceof Error ? e.message : '' }
  } finally {
    testingProxy.value = false
  }
}

async function networkCheck() {
  testingNet.value = true
  banner.value = { status: 'pending', title: '正在探测外部网络…' }
  netResults.value = null
  try {
    const d = await configApi.networkCheck()
    banner.value = null
    netResults.value = (d.results ?? []).map((r) => ({
      status: r.ok ? ('ok' as const) : ('err' as const),
      title: `${r.name} ${r.ok ? '可达' : '不可达'}`,
      detail: r.ok ? undefined : r.error,
      trailing: r.ok && r.latency_ms !== undefined ? `${r.latency_ms}ms` : undefined,
    }))
  } catch (e) {
    banner.value = { status: 'err', title: '网络测试失败', detail: e instanceof Error ? e.message : '' }
  } finally {
    testingNet.value = false
  }
}
</script>

<template>
  <SectionCard title="代理配置" hint="访问 TMDB / Telegram 等外网服务">
    <FieldRow
      label="代理地址"
      tip="HTTP(S) 代理，用于访问 TMDB 等外网服务，格式 http://IP:端口，留空不代理。"
    >
      <NInput v-model:value="model.url" placeholder="如 http://127.0.0.1:7890，留空不代理" />
    </FieldRow>

    <FormActions>
      <NButton type="primary" :loading="saving" @click="save()">保存配置</NButton>
      <NButton :loading="testingProxy" @click="testProxy">测试延迟</NButton>
      <NButton :loading="testingNet" @click="networkCheck">网络连接测试</NButton>
      <NButton @click="reset">重置配置</NButton>
    </FormActions>

    <TestBanner :state="banner" />
    <TestBanner v-for="(r, i) in netResults" :key="i" :state="r" />
  </SectionCard>
</template>
