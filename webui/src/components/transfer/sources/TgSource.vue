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
    tip="每行一个公开频道：@频道名、频道名或 https://t.me/频道名 都行。搜索时按片名查每个频道的公开网页（t.me/s/频道），最多查前 20 个。TG 关键词订阅没配订阅源时用的也是这份清单。"
    hint="国内网络连不上 t.me，要在「系统配置 → 代理」里设代理。只收频道里的 115 分享、磁力、ed2k 与其他网盘链接。"
  >
    <HInput v-model="channels" :rows="5" mono placeholder="@频道名（每行一个）" :input-attrs="{ 'aria-label': 'TG 频道清单' }" />
  </FieldRow>
  <FormActions>
    <HButton variant="primary" :loading="saving" @click="save">保存频道</HButton>
  </FormActions>
</template>
