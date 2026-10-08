<script setup lang="ts">
import MediaTypeChip from '@/components/MediaTypeChip.vue'
import HSpinner from '@/components/hero/HSpinner.vue'
import { Star } from '@lucide/vue'
import PosterImage from '@/components/PosterImage.vue'
import { resourcesApi } from '@/api'
import type { TmdbCandidate } from '@/api/resources'
import type { OwnedInfo } from '@/api/transfer'

/**
 * TMDB 选片，直接铺在页面上（原来是弹窗）。
 * 站内搜索对标题格式很敏感，先经 TMDB 换成规范标题再去各站搜，命中率高很多；
 * 选中的条目还带着原名与年份，后端拿来判断资源标题是不是这部片。
 */
defineProps<{
  items: TmdbCandidate[]
  owned: Record<string, OwnedInfo>
  loading?: boolean
  hint?: string
  /** 榜单里用：没有「跳过 TMDB」这回事 */
  noSkip?: boolean
}>()
const emit = defineEmits<{ pick: [TmdbCandidate]; skip: [] }>()
</script>

<template>
  <div class="cands">
    <div v-if="loading" class="state"><HSpinner size="sm" /><span>TMDB 匹配中…</span></div>
    <template v-else>
      <p v-if="hint" class="state">{{ hint }}</p>
      <div v-if="items.length" class="grid">
        <button
          v-for="it in items"
          :key="`${it.media_type}-${it.id}`"
          type="button"
          class="cand"
          :title="it.overview || it.title"
          @click="emit('pick', it)"
        >
          <div class="poster-box">
            <PosterImage
              class="poster"
              :src="it.poster ? resourcesApi.tmdbImageUrl(it.poster, 'w185') : null"
              :alt="it.title"
              :icon-size="22"
            />
            <span v-if="owned[`${it.media_type}:${it.id}`]" class="owned">已入库</span>
          </div>
          <div class="cand-title">{{ it.title }}</div>
          <div class="cand-meta">
            <MediaTypeChip :type="it.media_type" />
            <span v-if="it.year">{{ it.year }}</span>
            <span v-if="it.vote" class="vote"><Star :size="11" />{{ it.vote.toFixed(1) }}</span>
          </div>
        </button>
      </div>
    </template>
    <button v-if="!loading && !noSkip" type="button" class="skip" @click="emit('skip')">
      TMDB 里没有？跳过 TMDB，直接用关键词搜各站
    </button>
  </div>
</template>

<style scoped>
.cands {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.state {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  margin: 0;
  padding: 24px 0;
  font-size: 13px;
  color: var(--muted);
}
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(118px, 1fr));
  gap: 14px 12px;
}
.cand {
  all: unset;
  box-sizing: border-box;
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
  padding: 6px;
  border-radius: var(--r-lg);
  cursor: pointer;
  transition: background-color 0.15s;
}
.cand:hover {
  background: var(--default);
}
.cand:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 1px;
}
.poster-box {
  position: relative;
}
.poster {
  width: 100%;
  aspect-ratio: 2 / 3;
  border-radius: var(--r-sm);
}
.owned {
  position: absolute;
  left: 6px;
  top: 6px;
  padding: 0 7px;
  border-radius: 999px;
  font-size: 11px;
  line-height: 19px;
  background: var(--success);
  color: var(--success-foreground);
}
.cand-title {
  font-size: 13px;
  font-weight: 500;
  line-height: 1.4;
  color: var(--foreground);
  display: -webkit-box;
  -webkit-line-clamp: 2;
  line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.cand-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--muted);
}
.vote {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  color: var(--warning);
}
.skip {
  all: unset;
  align-self: flex-start;
  font-size: 12.5px;
  color: var(--accent);
  cursor: pointer;
}
.skip:hover {
  text-decoration: underline;
}
</style>
