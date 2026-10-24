<script setup lang="ts">
import { FolderInput, Link2, Search } from '@lucide/vue'
import HSpinner from '@/components/hero/HSpinner.vue'

/**
 * 找资源的搜索框：一整条胶囊，放大镜在框里、按钮贴右边。
 * 发现首页时嵌在横幅里（压在背景图上，用毛玻璃），其余时候单独一行；两处同一个组件，输入不丢。
 * 贴了下载链接就提示带去「链接转存」；下面一行写着找到的资源转存到哪。
 */
const model = defineModel<string>({ required: true })
defineProps<{
  busy?: boolean
  looksLikeLink?: boolean
  folderPath?: string
  /** 来源状态读到了没有：没读到时不显示转存目录那一行，免得闪一下「还没设置」 */
  ready?: boolean
  /** 压在横幅背景图上 */
  overlay?: boolean
}>()
const emit = defineEmits<{ go: []; toLink: []; folder: [] }>()
</script>

<template>
  <div class="sb" :class="{ overlay }">
    <form class="sb-pill" role="search" @submit.prevent="emit('go')">
      <Search class="sb-icon" :size="18" aria-hidden="true" />
      <input
        v-model="model"
        class="sb-input"
        type="search"
        placeholder="搜索电影、剧集（中英文均可）或 TMDB ID"
        aria-label="片名或 TMDB ID"
        autocomplete="off"
        enterkeyhint="search"
      />
      <button type="submit" class="sb-btn" :disabled="busy">
        <HSpinner v-if="busy" size="sm" />
        <span>搜索</span>
      </button>
    </form>
    <div class="sb-sub">
      <template v-if="looksLikeLink">
        <Link2 :size="13" />
        <span class="kind">这是下载链接</span>
        <button type="button" class="lnk" @click="emit('toLink')">带到「链接转存」提交</button>
      </template>
      <button
        v-else-if="ready"
        type="button"
        class="folder"
        :title="folderPath ? `找到的资源转存到 ${folderPath}，完成后自动整理入库。点击到媒体库里更改` : '去设置转存目录'"
        @click="emit('folder')"
      >
        <FolderInput :size="13" />
        <template v-if="folderPath">转存到 <span class="mono">{{ folderPath }}</span></template>
        <span v-else class="warn">还没设置转存目录</span>
      </button>
    </div>
  </div>
</template>

<style scoped>
.sb {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}
.sb-pill {
  display: flex;
  align-items: center;
  gap: 10px;
  height: 48px;
  padding: 0 5px 0 16px;
  border-radius: 999px;
  background: var(--surface);
  box-shadow:
    0 0 0 1px color-mix(in oklab, var(--foreground) 9%, transparent),
    0 12px 32px -20px rgb(0 0 0 / 0.5);
  transition: box-shadow 0.2s ease;
}
.sb-pill:focus-within {
  box-shadow:
    0 0 0 2px color-mix(in oklab, var(--accent) 75%, transparent),
    0 0 36px -8px color-mix(in oklab, var(--accent) 50%, transparent);
}
.sb-icon {
  flex: none;
  color: var(--muted);
}
.sb-input {
  all: unset;
  flex: 1;
  min-width: 0;
  font-size: 15px;
  color: var(--foreground);
}
.sb-input::placeholder {
  color: var(--muted);
}
.sb-input::-webkit-search-cancel-button {
  display: none;
}
.sb-btn {
  all: unset;
  flex: none;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 38px;
  padding: 0 20px;
  border-radius: 999px;
  font-size: 14px;
  font-weight: 600;
  background: var(--accent);
  color: var(--accent-foreground);
  cursor: pointer;
  transition:
    filter 0.15s,
    transform 0.1s;
}
.sb-btn:hover {
  filter: brightness(1.08);
}
.sb-btn:active {
  transform: scale(0.97);
}
.sb-btn:disabled {
  cursor: default;
  opacity: 0.8;
}
.sb-btn:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
}
/* 压在背景图上：毛玻璃深色底，字是浅色（外面包着 .on-dark） */
.overlay .sb-pill {
  background: rgb(0 0 0 / 0.38);
  backdrop-filter: blur(16px) saturate(160%);
  -webkit-backdrop-filter: blur(16px) saturate(160%);
  box-shadow:
    0 0 0 1px rgb(255 255 255 / 0.16),
    inset 0 1px 0 rgb(255 255 255 / 0.08);
}
.overlay .sb-pill:focus-within {
  background: rgb(0 0 0 / 0.5);
  box-shadow:
    0 0 0 2px color-mix(in oklab, var(--accent) 80%, transparent),
    0 0 40px -6px color-mix(in oklab, var(--accent) 60%, transparent);
}
.sb-sub {
  display: flex;
  align-items: center;
  gap: 6px;
  min-height: 18px;
  padding-left: 16px;
  font-size: 12px;
  color: var(--muted);
}
.kind {
  color: var(--accent);
}
.lnk {
  all: unset;
  color: var(--accent);
  cursor: pointer;
}
.lnk:hover {
  text-decoration: underline;
}
.folder {
  all: unset;
  display: inline-flex;
  align-items: center;
  gap: 5px;
  min-width: 0;
  max-width: 100%;
  cursor: pointer;
}
.folder:hover {
  color: var(--foreground);
}
.folder:focus-visible {
  outline: 2px solid var(--focus);
}
.mono {
  font-family: var(--font-mono);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.warn {
  color: var(--warning);
}
@media (max-width: 720px) {
  .sb-pill {
    height: 44px;
    padding-left: 13px;
  }
  .sb-btn {
    height: 34px;
    padding: 0 14px;
  }
  .sb-input {
    font-size: 14px;
  }
}
</style>
