<script setup lang="ts">
import { onMounted, ref } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import { resourcesApi } from '@/api'
import { toastError, useFeedback } from '@/composables/useFeedback'

const emit = defineEmits<{ changed: [] }>()
const { message } = useFeedback()
const DEFAULT_BASE = 'https://pansou.app'

const baseUrl = ref(DEFAULT_BASE)
const saving = ref(false)

async function load() {
  try {
    baseUrl.value = (await resourcesApi.pansouConfig()).base_url || DEFAULT_BASE
  } catch {
    // 首次使用尚无配置
  }
}

async function save() {
  saving.value = true
  try {
    const d = await resourcesApi.savePansou(baseUrl.value.trim())
    baseUrl.value = d.base_url || baseUrl.value
    message.success('保存成功')
    emit('changed')
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}

async function reset() {
  baseUrl.value = DEFAULT_BASE
  await save()
}

onMounted(load)
</script>

<template>
  <FieldRow
    label="站点地址"
    tip="盘搜（开源项目 PanSou）聚合搜索实例地址，默认 https://pansou.app。公开实例常限流，自建实例改这里，保存即生效。"
  >
    <div class="h-field-row">
      <HInput v-model="baseUrl" placeholder="PanSou 实例地址" />
      <HButton variant="primary" :loading="saving" @click="save">保存</HButton>
      <HButton variant="tertiary" @click="reset">重置</HButton>
    </div>
  </FieldRow>
</template>
