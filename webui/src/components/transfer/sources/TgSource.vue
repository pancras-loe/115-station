<script setup lang="ts">
import { onMounted, ref } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import { resourcesApi } from '@/api'
import { toastError, useFeedback } from '@/composables/useFeedback'

const emit = defineEmits<{ changed: [] }>()
const { message } = useFeedback()

const channels = ref('')
const saving = ref(false)

/**
 * 推荐频道：从盘搜官方 compose 的 CHANNELS 清单（MediaSync115 docker/pansou.env 里的同一份）
 * 按频道名挑出 115 分享 / 磁力为主的。只是名字看着像，没逐个核实还在不在更新，
 * 所以只填进输入框让人删改，不自动保存。盘搜本身就在搜这批频道，公开实例限流时它们才更有用
 */
const RECOMMENDED = [
  'Lsp115',
  'oneonefivewpfx',
  'Channel_Shares_115',
  'vip115hot',
  'gimy115',
  'gimy115iso',
  'cilidianying',
  'ciliziyuanku',
  'dianying4k',
  'Oscar_4Kmovies',
]

function fillRecommended() {
  // 贴的是 p115strmhelper 的 JSON 时先存一下让后端转成每行一个，别往 JSON 后面接文本
  if (channels.value.trim().startsWith('[')) {
    message.warning('先点「保存频道」把 JSON 转成每行一个，再填入推荐频道')
    return
  }
  const have = new Set(
    channels.value
      .split(/[\n,，\s]+/)
      .map((c) => c.trim().replace(/^@/, '').toLowerCase())
      .filter(Boolean),
  )
  const add = RECOMMENDED.filter((c) => !have.has(c.toLowerCase()))
  if (!add.length) {
    message.info('推荐频道都已经在清单里了')
    return
  }
  const base = channels.value.trim()
  channels.value = (base ? base + '\n' : '') + add.join('\n')
  message.success(`填入 ${add.length} 个推荐频道，删掉不要的再保存`)
}

async function load() {
  try {
    channels.value = (await resourcesApi.tgConfig()).channels ?? ''
  } catch {
    // 首次使用尚无配置
  }
}

async function save() {
  saving.value = true
  try {
    const d = await resourcesApi.tgSaveConfig(channels.value)
    // 后端会去重、把 @xxx / 链接统一成频道名，回显整理后的清单
    channels.value = d.channels ?? channels.value
    message.success('保存成功')
    emit('changed')
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<template>
  <FieldRow
    label="频道"
    wide
    tip="每行一个公开频道：@频道名、频道名或 https://t.me/频道名 都行；也可以直接粘贴 p115strmhelper 导出的频道 JSON，保存时转成每行一个。搜索时按片名查每个频道的公开网页（t.me/s/频道），最多查前 20 个。"
    hint="国内网络连不上 t.me，要在「系统配置 → 代理」里设代理。只收频道里的 115 分享、磁力、ed2k 与其他网盘链接。"
  >
    <HInput v-model="channels" :rows="5" mono placeholder="@频道名（每行一个）" :input-attrs="{ 'aria-label': 'TG 频道清单' }" />
  </FieldRow>
  <FormActions>
    <HButton variant="primary" :loading="saving" @click="save">保存频道</HButton>
    <HButton variant="tertiary" @click="fillRecommended">填入推荐频道</HButton>
  </FormActions>
</template>
