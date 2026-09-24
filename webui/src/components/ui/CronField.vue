<script setup lang="ts">
import { ref, watch } from 'vue'
import HInput from '@/components/hero/HInput.vue'
import { syncApi } from '@/api'

const props = defineProps<{ modelValue: string; placeholder?: string }>()
const emit = defineEmits<{ 'update:modelValue': [string] }>()

const next = ref<string[]>([])
const error = ref('')
let timer: number | undefined

watch(
  () => props.modelValue,
  (expr) => {
    clearTimeout(timer)
    next.value = []
    error.value = ''
    if (!expr.trim()) return
    // 防抖：cron 是一个字符一个字符敲出来的，中间态几乎都是非法表达式
    timer = window.setTimeout(async () => {
      try {
        const d = await syncApi.cronPreview(expr.trim())
        next.value = d.next ?? []
        if (!next.value.length) error.value = '未来一年内不会触发，请检查表达式'
      } catch (e) {
        error.value = e instanceof Error ? e.message : '表达式无效'
      }
    }, 500)
  },
  { immediate: true },
)
</script>

<template>
  <div>
    <HInput
      mono
      :model-value="modelValue"
      :placeholder="placeholder || '0 8 * * *'"
      @update:model-value="emit('update:modelValue', $event)"
    />
    <div class="preview">
      <span v-if="error" class="err">{{ error }}</span>
      <template v-else-if="next.length">
        接下来：<span v-for="(t, i) in next.slice(0, 3)" :key="i" class="t">{{ t }}</span>
      </template>
      <span v-else class="dim">5 段式 cron：分 时 日 月 周</span>
    </div>
  </div>
</template>

<style scoped>
.preview {
  margin-top: 6px;
  font-size: 12px;
  color: var(--muted);
  line-height: 1.7;
}
.t {
  font-variant-numeric: tabular-nums;
  color: var(--foreground);
}
.t + .t::before {
  content: '·';
  margin: 0 6px;
  color: var(--muted);
}
.err {
  color: var(--danger);
}
.dim {
  color: var(--muted);
}
</style>
