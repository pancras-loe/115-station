<script setup lang="ts">
import { computed, ref } from 'vue'
import { NAlert, NButton, NInput, NInputNumber, NRadioButton, NRadioGroup, NSwitch } from 'naive-ui'
import SectionCard from '@/components/ui/SectionCard.vue'
import SecretInput from '@/components/ui/SecretInput.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import TestBanner, { type BannerState } from '@/components/ui/TestBanner.vue'
import { configApi } from '@/api'
import { useSetting } from '@/composables/useSetting'
import { useFeedback } from '@/composables/useFeedback'

const { message } = useFeedback()
// 键名 org-gpt 是历史的（这张卡早年叫「GPT 识别」），改名只会让已填的密钥消失
const { model, saving, save, reset } = useSetting('org-gpt', {
  enabled: false,
  url: 'https://api.deepseek.com',
  key: '',
  model: '',
  /** AI 判定出来的结果：off 直接入库 / auto 按分数 / force 一律等人工确认 */
  confirm_mode: 'auto' as 'off' | 'auto' | 'force',
  /** auto 模式下的自动入库线（0-100） */
  min_score: 80,
})

const CONFIRM_HINT: Record<string, string> = {
  off: 'AI 判定出来的结果和规则识别的一样，直接走后续整理，不停下来。',
  auto: '分数达到下面的线就直接整理，不够的停在「整理记录 → 待确认」等你点确认或改指定。',
  force: 'AI 判定出来的结果一律停在「整理记录 → 待确认」，由你确认后才入库。',
}

const banner = ref<BannerState | null>(null)
const testing = ref(false)
const checking = ref(false)

/** 跑一遍后端校验并把结果画到 banner 上；返回是否通过 */
async function runTest(pendingTitle: string): Promise<boolean> {
  banner.value = { status: 'pending', title: pendingTitle }
  try {
    const d = await configApi.testAi({ url: model.value.url, key: model.value.key, model: model.value.model })
    // endpoint 是后端补全后真正请求的地址，地址填错时这一行最省排查时间
    banner.value = d.ok
      ? {
          status: 'ok',
          title: '连接成功',
          detail: d.endpoint,
          trailing: d.latency_ms ? `${d.latency_ms}ms` : undefined,
        }
      : { status: 'err', title: '连接失败', detail: [d.endpoint, d.error].filter(Boolean).join(' — ') }
    return !!d.ok
  } catch (e) {
    banner.value = { status: 'err', title: '连接失败', detail: e instanceof Error ? e.message : '' }
    return false
  }
}

/** 地址能不能解析出主机名（没写协议头按 https 补，和后端同一套规则） */
function parsableUrl(v: string): boolean {
  try {
    return !!new URL(/^[a-z][\w+.-]*:\/\//i.test(v) ? v : `https://${v}`).hostname
  } catch {
    return false
  }
}

/** 三项逐字段校验。开关关着时不校验——配置填一半也允许存着 */
const errors = computed(() => {
  const e = { url: '', key: '', model: '' }
  if (!model.value.enabled) return e
  const url = model.value.url.trim()
  if (!url) e.url = '请填写 API 地址'
  else if (!parsableUrl(url)) e.url = '地址格式不对，应形如 https://api.deepseek.com'
  if (!model.value.key.trim()) e.key = '请填写 API 密钥；本地模型不校验密钥，填个占位串即可'
  const name = model.value.model.trim()
  if (!name) e.model = '请填写模型名称'
  else if (/\s/.test(name)) e.model = '模型名称不该带空格，检查下是不是粘贴多了'
  return e
})
const hasError = computed(() => Object.values(errors.value).some(Boolean))

/** 点过保存/测试之后才亮红，免得一进页面满屏红字 */
const showErrors = ref(false)
const errorOf = (k: 'url' | 'key' | 'model') => (showErrors.value ? errors.value[k] : '')

async function test() {
  if (hasError.value) {
    showErrors.value = true
    message.warning('请先补全标红的项')
    return
  }
  // 开关关着时上面不校验，但测试连接本身要这三项
  if (!model.value.url.trim() || !model.value.model.trim()) {
    message.warning('请先填写 API 地址和模型名称')
    return
  }
  testing.value = true
  try {
    await runTest('正在连接模型接口…')
  } finally {
    testing.value = false
  }
}

/**
 * 开着的时候，保存前先把三项必填查一遍、再跑一次后端连通性校验。
 * 这张卡的配置只在「TMDB 全都搜不到」时才会被用到，存错了当场没有任何反馈，
 * 要等某次整理识别失败、翻日志才发现——所以宁可卡在保存这一步。
 */
async function saveChecked() {
  // 关着就是不启用，配置填一半也允许存着，下次开之前再补
  if (!model.value.enabled) {
    banner.value = null
    await save()
    return
  }
  if (hasError.value) {
    showErrors.value = true
    banner.value = null
    message.error('开启「AI 增强识别」后，标红的项都要填对才能保存')
    return
  }
  checking.value = true
  let ok = false
  try {
    ok = await runTest('正在校验模型接口…')
  } finally {
    checking.value = false
  }
  if (!ok) {
    message.error('模型接口校验未通过，配置未保存——按下方提示改完再保存')
    return
  }
  await save()
}
</script>

<template>
  <SectionCard title="AI 增强识别" hint="TMDB 识别失败时的兜底">
    <NAlert class="note" type="info" :bordered="false">
      规则识别全部落空时的最后一环：先把原始文件名和所在目录交给大模型，让它说出片名、年份、类型和季集再搜
      TMDB；还搜不中，就把 TMDB 搜到的几个候选交给它挑。每个条目最多调用两次。接口按 OpenAI
      协议标准调用：地址即各家文档里给的 base_url，后面追加 <code>/chat/completions</code>。
      DeepSeek、硅基流动、Ollama、vLLM 等兼容实现都能直接填。
    </NAlert>

    <FieldRow label="启用" tip="关闭后整理流程完全不碰模型接口，下面填好的配置原样留着，随时能开回来。开启时保存会先校验三项必填与接口连通性，通不过不落库。">
      <NSwitch v-model:value="model.enabled" />
    </FieldRow>
    <FieldRow :required="model.enabled" :error="errorOf('url')" label="API 地址" tip="填各家文档给的 base_url：DeepSeek 是 https://api.deepseek.com，OpenAI 是 https://api.openai.com/v1（/v1 属于地址的一部分，这里不会替你猜）。填整条完整路径也认。">
      <NInput
        v-model:value="model.url"
        :status="errorOf('url') ? 'error' : undefined"
        placeholder="如 https://api.deepseek.com"
      />
    </FieldRow>
    <FieldRow :required="model.enabled" :error="errorOf('key')" label="API 密钥" tip="大模型 API 密钥，推荐使用硅基流动等国内平台。本地模型（Ollama / vLLM）不校验密钥，但这里仍需填一个占位串（如 ollama）。">
      <SecretInput
        v-model="model.key"
        :status="errorOf('key') ? 'error' : undefined"
        name="ai-api-key"
        placeholder="sk-xxx"
      />
    </FieldRow>
    <FieldRow :required="model.enabled" :error="errorOf('model')" label="模型名称" tip="用于识别的模型名称，如 deepseek-flash、Qwen2.5-7B-Instruct 等。">
      <NInput
        v-model:value="model.model"
        :status="errorOf('model') ? 'error' : undefined"
        placeholder="如 deepseek-flash"
      />
    </FieldRow>

    <FieldRow
      label="AI 判定后"
      tip="AI 判定的结果比规则识别更容易出错，这里决定它们要不要先经你确认。每条 AI 判定都会打一个 0-100 的分：模型自评的把握度，年份、类型、片名对不上时封顶。整理记录里会标出「AI 识别 / AI 选定 N 分」，悬停可以看打分依据。"
      :hint="CONFIRM_HINT[model.confirm_mode]"
    >
      <NRadioGroup v-model:value="model.confirm_mode" size="small">
        <NRadioButton value="off">直接整理</NRadioButton>
        <NRadioButton value="auto">按分数</NRadioButton>
        <NRadioButton value="force">一律人工确认</NRadioButton>
      </NRadioGroup>
    </FieldRow>
    <FieldRow
      v-if="model.confirm_mode === 'auto'"
      label="自动整理线"
      tip="分数不低于这条线的 AI 判定直接整理，低于的等人工确认。模型从候选里挑、片名又和文件名对不上的，最高只有 75 分，按默认 80 分的线会停下来。"
      hint="默认 80；调低放行更多，调高更保守"
    >
      <NInputNumber v-model:value="model.min_score" :min="0" :max="100" :step="5" style="width: 140px">
        <template #suffix>分</template>
      </NInputNumber>
    </FieldRow>
    <NAlert v-if="model.confirm_mode !== 'off'" class="note" type="default" :bordered="false">
      「基础配置 → 人工确认」打开时，所有识别结果本来就都要确认，这里的设置不改变那一点。
      因 AI 判定停下的条目，关掉那个开关后也不会被自动整理接手，仍然等你处理。
    </NAlert>

    <FormActions>
      <NButton type="primary" :loading="checking || saving" :disabled="testing" @click="saveChecked">
        保存配置
      </NButton>
      <NButton :loading="testing" :disabled="checking || saving" @click="test">测试连接</NButton>
      <NButton :disabled="checking || saving || testing" @click="reset">重置配置</NButton>
    </FormActions>
    <TestBanner :state="banner" />
  </SectionCard>
</template>

<style scoped>
.note {
  margin-bottom: 12px;
}
.note code {
  font-size: 0.92em;
}
</style>
