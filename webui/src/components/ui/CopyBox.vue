<script setup lang="ts">
import { ref } from 'vue'
import HTooltip from '@/components/hero/HTooltip.vue'
import { Check, Copy } from '@lucide/vue'
import { copyText } from '@/utils/clipboard'

defineProps<{ value: string; tone?: 'primary' | 'success' }>()

const copied = ref(false)
const failed = ref(false)
const codeEl = ref<HTMLElement>()

async function copy(text: string) {
  if (await copyText(text)) {
    copied.value = true
    setTimeout(() => (copied.value = false), 1600)
    return
  }
  // 两种写法都失败：选中文本让用户按 Ctrl+C。用模板 ref 定位，
  // 原来按「值长度」拼 id，同页两个等长的值会选错框
  const el = codeEl.value
  if (el) {
    const range = document.createRange()
    range.selectNodeContents(el)
    const sel = window.getSelection()
    sel?.removeAllRanges()
    sel?.addRange(range)
  }
  failed.value = true
  setTimeout(() => (failed.value = false), 2400)
}
</script>

<template>
  <div class="box" :class="tone">
    <code ref="codeEl">{{ value }}</code>
    <HTooltip :content="copied ? '已复制' : failed ? '已选中，按 Ctrl+C 复制' : '复制'">
      <button class="copy" :aria-label="copied ? '已复制' : '复制'" @click="copy(value)">
        <Check v-if="copied" :size="14" />
        <Copy v-else :size="14" />
      </button>
    </HTooltip>
  </div>
</template>

<style scoped>
.box {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 9px 10px 9px 14px;
  border-radius: 14px;
  background: var(--default);
}
.box code {
  flex: 1;
  min-width: 0;
  padding-top: 2px;
  font-family: var(--font-mono);
  font-size: 12px;
  line-height: 1.6;
  word-break: break-all;
  color: var(--foreground);
}
.box.primary code {
  color: var(--accent-soft-foreground);
}
.box.success code {
  color: var(--success-soft-foreground);
}

.copy {
  all: unset;
  flex-shrink: 0;
  display: grid;
  place-items: center;
  width: 28px;
  height: 28px;
  border-radius: 999px;
  cursor: pointer;
  color: var(--muted);
  transition:
    background-color 0.15s,
    color 0.15s;
}
.copy:hover {
  background: var(--surface);
  color: var(--foreground);
}
.copy:focus-visible {
  box-shadow: 0 0 0 2px var(--focus);
}
</style>
