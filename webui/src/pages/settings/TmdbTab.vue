<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import { heroTone } from '@/components/hero/tone'
import SectionCard from '@/components/ui/SectionCard.vue'
import SecretInput from '@/components/ui/SecretInput.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import TestBanner, { type BannerState } from '@/components/ui/TestBanner.vue'
import { configApi } from '@/api'
import { toastError, useFeedback } from '@/composables/useFeedback'

const { message } = useFeedback()
const route = useRoute()
const router = useRouter()

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
const keyInput = ref<InstanceType<typeof SecretInput> | null>(null)
const keyError = ref('')
const saved = ref('')
const verified = ref('')
const loading = ref(true)
const loadError = ref(false)
const fingerprint = computed(() => JSON.stringify(model.value))
const verifiedSaved = computed(() => verified.value === fingerprint.value && saved.value === fingerprint.value)
const busy = computed(() => loading.value || saving.value || testing.value)
watch(fingerprint, () => {
  banner.value = null
  keyError.value = ''
  verified.value = ''
}, { flush: 'sync' })

function validate() {
  if (model.value.api_key.trim()) return true
  keyError.value = '请填写 TMDB API Key，自动整理需要它来识别影视。'
  keyInput.value?.focus()
  return false
}

async function load() {
  loading.value = true
  loadError.value = false
  try {
    const res = await configApi.getTmdb()
    const c = res.data ?? res
    model.value = {
      api_url: c.api_url || DEFAULTS.api_url,
      image_url: c.image_api_url || c.image_url || DEFAULTS.image_url,
      api_key: c.api_key || '',
      language: c.language || DEFAULTS.language,
    }
    saved.value = fingerprint.value
  } catch (e) {
    loadError.value = true
    toastError(e, 'TMDB 配置读取失败，请重试')
  } finally {
    loading.value = false
  }
}

async function save(): Promise<boolean> {
  if (!validate()) return false
  saving.value = true
  try {
    model.value.api_key = model.value.api_key.trim()
    await configApi.saveTmdb(model.value)
    saved.value = fingerprint.value
    message.success('保存成功')
    return true
  } catch (e) {
    toastError(e, '保存失败')
    return false
  } finally {
    saving.value = false
  }
}

async function reset() {
  model.value = { ...DEFAULTS, api_key: model.value.api_key }
}

async function test() {
  if (!validate()) return
  testing.value = true
  verified.value = ''
  const tested = fingerprint.value
  banner.value = { status: 'pending', title: '正在连接 TMDB…' }
  try {
    const d = await configApi.testTmdb({ ...model.value })
    if (fingerprint.value !== tested) return
    if (d.ok) verified.value = tested
    banner.value = d.ok
      ? { status: 'ok', title: 'TMDB 连接成功', trailing: d.latency_ms ? `${d.latency_ms}ms` : undefined }
      : { status: 'err', title: 'TMDB 连接失败', detail: d.error }
  } catch (e) {
    banner.value = { status: 'err', title: 'TMDB 连接失败', detail: e instanceof Error ? e.message : '' }
  } finally {
    testing.value = false
  }
}

async function saveAndTest() {
  if (await save()) await test()
}

onMounted(load)
</script>

<template>
  <SectionCard title="TMDB 配置" hint="识别与刮削的元数据来源">
    <HAlert status="danger" v-if="loadError">
      TMDB 配置读取失败，请重试后再修改。
      <div class="alert-action"><HButton variant="tertiary" @click="load">重新加载</HButton></div>
    </HAlert>
    <HAlert :status="heroTone(verifiedSaved ? 'success' : 'info')" v-else>
      {{ verifiedSaved ? 'TMDB 已保存并验证可用，可以开始整理。' : '自动整理、影视搜片与刮削需要 TMDB API Key。填写后点击「保存并测试」确认可用。' }}
      <div v-if="verifiedSaved && route.query.from === 'organize'" class="alert-action">
        <HButton variant="tertiary" size="sm" v-if="verifiedSaved && route.query.from === 'organize'" @click="router.push('/organize')">返回自动整理</HButton>
      </div>
    </HAlert>
    <fieldset :disabled="busy || loadError" class="tmdb-fields">
    <FieldRow label="API 域名" tip="TMDB API 地址，国内网络可换反代加速。">
      <HInput v-model="model.api_url" placeholder="https://api.tmdb.org" />
    </FieldRow>

    <FieldRow label="图片域名" tip="TMDB 海报图片域名，可换反代加速。">
      <HInput v-model="model.image_url" placeholder="https://image.tmdb.org" />
    </FieldRow>

    <FieldRow
      label="API 密钥"
      required
      :error="keyError"
      tip="登录 TMDB → 设置 → API，复制 API Key（不是较长的 API Read Access Token）。"
    >
      <SecretInput ref="keyInput" v-model="model.api_key" :status="keyError ? 'error' : undefined" name="tmdb-api-key" placeholder="填写 TMDB API Key（必填）" />
      <HButton variant="ghost" class="text-btn" tag="a" href="https://www.themoviedb.org/settings/api" target="_blank" rel="noopener noreferrer">获取 API Key ↗</HButton>
    </FieldRow>

    <FieldRow label="语言" tip="中文优先返回中文片名与简介；英文返回原名。">
      <HSegmented v-model="model.language" :options="[{ label: '中文', value: 'zh-CN' }, { label: '英文', value: 'en-US' }]" />
    </FieldRow>

    </fieldset>
    <FormActions>
      <HButton variant="primary" :loading="busy" :disabled="busy || loadError" @click="saveAndTest">保存并测试</HButton>
      <HButton variant="tertiary" :disabled="busy || loadError" @click="save">仅保存</HButton>
      <HButton variant="tertiary" :disabled="busy || loadError" @click="test">测试当前填写</HButton>
      <HButton variant="tertiary" :disabled="busy || loadError" @click="reset">恢复默认地址与语言</HButton>
    </FormActions>

    <TestBanner :state="banner" />
    <p v-if="!loading && !loadError && !verifiedSaved">{{ saved === fingerprint ? '已保存的配置尚未在本页验证可用。' : '当前修改尚未保存，整理仍使用已保存的配置。' }}</p>
  </SectionCard>
</template>

<style scoped>
.alert-action { margin-top: 12px; }
.tmdb-fields { border: 0; padding: 0; margin: 16px 0 0; min-width: 0; }
p { color: var(--c-text-3); font-size: 12px; }
</style>
