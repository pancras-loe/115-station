<script setup lang="ts">
import { ref } from 'vue'
import HTooltip from '@/components/hero/HTooltip.vue'
import { Check, Copy } from '@lucide/vue'

defineProps<{ value: string; tone?: 'primary' | 'success' }>()

const copied = ref(false)

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    // 非 HTTPS 环境下 clipboard API 不可用（本项目常见的明文 HTTP 部署），
    // 回退到选中文本，用户按 Ctrl+C
    const sel = window.getSelection()
    const range = document.createRange()
    const el = document.getElementById('copybox-' + text.length)
    if (el) {
      range.selectNodeContents(el)
      sel?.removeAllRanges()
      sel?.addRange(range)
    }
    return
  }
  copied.value = true
  setTimeout(() => (copied.value = false), 1600)
}
</script>

<template>
  <div class="box" :class="tone">
    <code :id="'copybox-' + value.length">{{ value }}</code>
    <HTooltip :content="copied ? '已复制' : '复制'">
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
