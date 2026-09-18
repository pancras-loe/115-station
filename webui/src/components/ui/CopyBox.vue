<script setup lang="ts">
import { ref } from 'vue'
import { NTooltip } from 'naive-ui'
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
    <NTooltip>
      <template #trigger>
        <button class="copy" :aria-label="copied ? '已复制' : '复制'" @click="copy(value)">
          <Check v-if="copied" :size="14" />
          <Copy v-else :size="14" />
        </button>
      </template>
      {{ copied ? '已复制' : '复制' }}
    </NTooltip>
  </div>
</template>

<style scoped>
.box {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 8px 10px;
  border-radius: var(--radius);
  background: var(--c-bg-raised);
  border: 1px solid var(--c-border);
}
.box code {
  flex: 1;
  min-width: 0;
  font-family: var(--font-mono);
  font-size: 12px;
  line-height: 1.6;
  word-break: break-all;
  color: var(--c-text-1);
}
.box.primary code {
  color: var(--c-primary);
}
.box.success code {
  color: var(--c-success);
}

.copy {
  all: unset;
  flex-shrink: 0;
  display: grid;
  place-items: center;
  width: 24px;
  height: 24px;
  border-radius: var(--radius-sm);
  cursor: pointer;
  color: var(--c-text-3);
  transition: background-color 0.15s, color 0.15s;
}
.copy:hover {
  background: var(--c-bg-hover);
  color: var(--c-text-1);
}
</style>
