<script setup lang="ts">
import HSpinner from '@/components/hero/HSpinner.vue'
import PosterCard from '@/components/transfer/PosterCard.vue'
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
        <PosterCard
          v-for="it in items"
          :key="`${it.media_type}-${it.id}`"
          :item="it"
          :owned="owned[`${it.media_type}:${it.id}`]"
          @pick="emit('pick', it)"
        />
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
  grid-template-columns: repeat(auto-fill, minmax(136px, 1fr));
  gap: 22px 16px;
}
@media (max-width: 720px) {
  .grid {
    grid-template-columns: repeat(auto-fill, minmax(104px, 1fr));
    gap: 16px 10px;
  }
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
