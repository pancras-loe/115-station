<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NAlert, NButton } from 'naive-ui'
import SectionCard from '@/components/ui/SectionCard.vue'
import FormActions from '@/components/ui/FormActions.vue'
import YamlEditor from '@/components/ui/YamlEditor.vue'
import { toastError, useFeedback } from '@/composables/useFeedback'

/** 二级分类与洗版两个页签结构完全一致，只有接口和文案不同 */
const props = defineProps<{
  title: string
  hint?: string
  note: string
  load: () => Promise<{ config?: string }>
  persist: (yaml: string) => Promise<unknown>
  fallback: string
}>()

const { message } = useFeedback()

const yaml = ref('')
const loading = ref(true)
const saving = ref(false)

async function read() {
  loading.value = true
  try {
    const d = await props.load()
    yaml.value = d.config || props.fallback
  } catch (e) {
    yaml.value = props.fallback
    toastError(e, '规则读取失败')
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  try {
    // 后端会解析 YAML，格式错误时返回带行号的错误信息，直接透传给用户
    await props.persist(yaml.value)
    message.success('保存成功')
  } catch (e) {
    toastError(e, '保存失败：请检查 YAML 缩进与格式')
  } finally {
    saving.value = false
  }
}

onMounted(read)
</script>

<template>
  <SectionCard :title="title" :hint="hint">
    <NAlert class="note" type="info" :bordered="false">{{ note }}</NAlert>

    <YamlEditor v-if="!loading" v-model="yaml" :rows="26" />
    <div v-else class="skeleton" />

    <FormActions>
      <NButton type="primary" :loading="saving" @click="save">保存配置</NButton>
      <NButton @click="read">放弃修改</NButton>
    </FormActions>
  </SectionCard>
</template>

<style scoped>
.note {
  margin-bottom: 12px;
}
.skeleton {
  height: 460px;
  border-radius: var(--radius);
  background: var(--c-bg-raised);
  border: 1px solid var(--c-border);
}
</style>
