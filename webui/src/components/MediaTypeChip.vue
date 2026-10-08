<script setup lang="ts">
import { computed } from 'vue'

/**
 * 「电影」「剧集」标签，全站统一一个样子。以前各页各写各的：有的电影黄剧集橙（暗色下几乎分不出），
 * 有的电影灰剧集橙，有的两个都灰。颜色是专门的 --media-movie / --media-tv（main.css），
 * 不占用状态色 —— 状态色要留给「成功 / 警告 / 失败」。
 * type 认 TMDB 的 movie / tv，也认 Emby 的 Movie / Series / Episode。
 */
const props = withDefaults(defineProps<{ type?: string | null; size?: 'sm' | 'md' }>(), { size: 'sm' })

const kind = computed(() => {
  const t = (props.type ?? '').toLowerCase()
  if (t === 'tv' || t === 'series' || t === 'season' || t === 'episode') return 'tv'
  if (t === 'movie') return 'movie'
  return ''
})
</script>

<template>
  <span v-if="kind" class="media-chip" :class="[`is-${kind}`, `is-${size}`]">{{ kind === 'tv' ? '剧集' : '电影' }}</span>
</template>

<style scoped>
.media-chip {
  display: inline-flex;
  align-items: center;
  flex: none;
  border-radius: 999px;
  font-weight: 600;
  line-height: 1;
  white-space: nowrap;
  color: var(--c);
  background: color-mix(in oklab, var(--c) 15%, transparent);
}
.is-movie {
  --c: var(--media-movie);
}
.is-tv {
  --c: var(--media-tv);
}
.is-sm {
  padding: 4px 8px;
  font-size: 11.5px;
}
.is-md {
  padding: 5px 10px;
  font-size: 12.5px;
}
</style>
