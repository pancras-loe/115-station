<script setup lang="ts">
/**
 * 来源链接一行：类型徽标 + 截断的链接 + 复制按钮（http/分享链接再挂一个打开）。
 * 整理记录用它显示这批内容的来源链接，不占版面。
 */
import { computed, ref } from 'vue'
import { NTooltip } from 'naive-ui'
import { Check, Copy, ExternalLink } from '@lucide/vue'

const props = defineProps<{ link: string; kind?: string }>()

const KIND_LABEL: Record<string, string> = {
  magnet: '磁力',
  ed2k: 'ed2k',
  http: 'HTTP',
  ftp: 'FTP',
  share: '115 分享',
}

/** 后端没给 kind（老记录）时按链接前缀兜底判一次 */
const kindOf = computed(() => {
  if (props.kind) return props.kind
  const l = props.link.toLowerCase()
  if (l.startsWith('magnet:?')) return 'magnet'
  if (l.startsWith('ed2k://')) return 'ed2k'
  if (/\/s\//.test(l) && /115\.com|115cdn\.com|anxia\.com/.test(l)) return 'share'
  if (l.startsWith('ftp://')) return 'ftp'
  if (l.startsWith('http')) return 'http'
  return ''
})

const label = computed(() => KIND_LABEL[kindOf.value] ?? '链接')
/** 可点开的只有 http/ftp/分享；磁力和 ed2k 交给浏览器唤起外部程序反而容易报错 */
const openable = computed(() => ['http', 'share'].includes(kindOf.value))

const copied = ref(false)
async function copy() {
  try {
    await navigator.clipboard.writeText(props.link)
    copied.value = true
    setTimeout(() => (copied.value = false), 1600)
  } catch {
    // 明文 HTTP 部署下 clipboard API 不可用：提示用户手动选中
    window.prompt('复制下面的链接', props.link)
  }
}
</script>

<template>
  <div class="src-link">
    <span class="kind">{{ label }}</span>
    <span class="text" :title="link">{{ link }}</span>
    <NTooltip>
      <template #trigger>
        <button class="act" :aria-label="copied ? '已复制' : '复制链接'" @click="copy">
          <Check v-if="copied" :size="13" />
          <Copy v-else :size="13" />
        </button>
      </template>
      {{ copied ? '已复制' : '复制链接' }}
    </NTooltip>
    <NTooltip v-if="openable">
      <template #trigger>
        <a class="act" :href="link" target="_blank" rel="noopener" aria-label="打开链接">
          <ExternalLink :size="13" />
        </a>
      </template>
      在新标签打开
    </NTooltip>
  </div>
</template>

<style scoped>
.src-link {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  font-size: 12px;
}
.kind {
  flex-shrink: 0;
  padding: 0 6px;
  border-radius: var(--radius-sm);
  background: var(--c-bg-raised);
  border: 1px solid var(--c-border);
  color: var(--c-text-3);
  line-height: 18px;
}
.text {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: var(--font-mono);
  color: var(--c-text-3);
}
.act {
  all: unset;
  flex-shrink: 0;
  display: grid;
  place-items: center;
  width: 20px;
  height: 20px;
  border-radius: var(--radius-sm);
  cursor: pointer;
  color: var(--c-text-3);
  transition: background-color 0.15s, color 0.15s;
}
.act:hover {
  background: var(--c-bg-hover);
  color: var(--c-text-1);
}
</style>
