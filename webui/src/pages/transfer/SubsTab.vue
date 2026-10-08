<script setup lang="ts">
import { computed, ref } from 'vue'
import { Clapperboard, Plus, Tv } from '@lucide/vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HSearchField from '@/components/hero/HSearchField.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSkeleton from '@/components/hero/HSkeleton.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import PosterImage from '@/components/PosterImage.vue'
import { resourcesApi } from '@/api'
import type { Subscription } from '@/api/subscribe'
import { STATE_TEXT, STATE_TONE, scopeText } from '@/api/subscribe'
import { useSubscriptions } from '@/composables/subscriptions'

/**
 * 订阅海报墙。新建订阅在「找资源」选定影片后点「订阅」，这里只管看和改 ——
 * 选片的流程那边已经有了，别在这里再做一份 TMDB 搜索。详情抽屉由页面挂着（?sub=）。
 */
const emit = defineEmits<{ create: [] }>()
const { rows, loading, loaded, counts, openDetail } = useSubscriptions()

const filter = ref<'all' | 'active' | 'stalled' | 'paused' | 'done'>('all')
const keyword = ref('')

const filterOptions = computed(() => [
  { label: `全部 ${counts.value.all}`, value: 'all' as const },
  { label: `追更中 ${counts.value.active}`, value: 'active' as const },
  { label: `找不到 ${counts.value.stalled}`, value: 'stalled' as const },
  { label: `暂停 ${counts.value.paused}`, value: 'paused' as const },
  { label: `完成 ${counts.value.done}`, value: 'done' as const },
])
const items = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  return rows.value.filter(
    (r) =>
      (filter.value === 'all' || r.state === filter.value) &&
      (!kw || r.title.toLowerCase().includes(kw) || (r.orig_title ?? '').toLowerCase().includes(kw) || String(r.tmdb_id) === kw),
  )
})

function progress(r: Subscription) {
  if (r.media_type === 'movie') return r.have ? '已入库' : r.inflight ? '在路上' : '未入库'
  if (!r.total) return r.last_check_at ? '还没有已播的集' : '等待检查'
  return `${r.have} / ${r.total} 集`
}
function pct(r: Subscription) {
  if (r.media_type === 'movie') return r.have ? 100 : 0
  return r.total ? Math.round((r.have / r.total) * 100) : 0
}
</script>

<template>
  <div class="page">
    <div class="bar">
      <HSegmented v-model="filter" :options="filterOptions" size="sm" class="filters" />
      <HSearchField v-model="keyword" placeholder="片名或 TMDB ID" class="search" />
      <HButton variant="primary" size="sm" @click="emit('create')"><Plus :size="14" />新订阅</HButton>
    </div>

    <div v-if="loading && !loaded" class="grid" aria-busy="true">
      <div v-for="i in 8" :key="i" class="sk">
        <HSkeleton width="100%" height="auto" radius="10px" class="sk-poster" />
        <HSkeleton width="70%" height="13px" radius="999px" />
      </div>
    </div>
    <EmptyState
      v-else-if="!items.length"
      :text="rows.length ? '没有符合条件的订阅' : '还没有订阅：在「找资源」里选一部影片，点「订阅」。剧集默认补缺集，转存后自动整理入库'"
    />
    <div v-else class="grid" :class="{ dim: loading }">
      <button v-for="r in items" :key="r.id" type="button" class="card" :class="`st-${r.state}`" @click="openDetail(r.id)">
        <div class="poster-wrap">
          <div class="poster-none">
            <Tv v-if="r.media_type === 'tv'" :size="28" />
            <Clapperboard v-else :size="28" />
          </div>
          <PosterImage
            v-if="r.poster_path"
            class="poster"
            :src="resourcesApi.tmdbImageUrl(r.poster_path, 'w342')"
            :alt="r.title"
          />
          <HChip v-if="r.state !== 'active'" :color="STATE_TONE[r.state]" variant="primary" size="sm" class="badge">
            {{ STATE_TEXT[r.state] }}
          </HChip>
          <HChip v-else-if="r.running" color="accent" variant="primary" size="sm" class="badge">检查中</HChip>
          <div class="bar-track"><div class="bar-fill" :style="{ width: `${pct(r)}%` }" /></div>
        </div>
        <div class="info">
          <h3 class="name" :title="r.title">{{ r.title }}</h3>
          <p class="meta">{{ r.year ? `${r.year} · ` : '' }}{{ progress(r) }}</p>
          <p class="meta">
            <span v-if="r.missing" class="miss">缺 {{ r.missing }}</span>
            <span v-if="r.inflight" class="fly">在路上 {{ r.inflight }}</span>
            <span v-if="!r.missing && !r.inflight" :title="r.last_result">{{ scopeText(r) }}</span>
          </p>
        </div>
      </button>
    </div>
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 10px;
}
.filters {
  max-width: 100%;
  overflow-x: auto;
}
.search {
  width: 220px;
  margin-left: auto;
}
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
  gap: 14px;
  transition: opacity 0.15s;
}
.grid.dim {
  opacity: 0.6;
}
.sk {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.sk-poster {
  aspect-ratio: 2 / 3;
}
.card {
  all: unset;
  display: flex;
  flex-direction: column;
  min-width: 0;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  overflow: hidden;
  cursor: pointer;
  transition: box-shadow 0.15s, transform 0.15s;
}
.card:focus-visible {
  box-shadow: 0 0 0 2px var(--focus);
}
@media (hover: hover) {
  .card:hover {
    transform: translateY(-3px);
    box-shadow:
      0 14px 28px -16px rgb(0 0 0 / 0.6),
      0 0 0 1.5px color-mix(in oklab, var(--accent) 55%, transparent);
  }
}
.card.st-paused .poster,
.card.st-done .poster {
  opacity: 0.6;
}
.poster-wrap {
  position: relative;
  aspect-ratio: 2 / 3;
  background: var(--surface-tertiary, var(--surface-secondary));
}
.poster-none {
  position: absolute;
  inset: 0;
  display: grid;
  place-items: center;
  color: var(--muted);
}
.poster {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
}
.badge {
  position: absolute;
  top: 8px;
  right: 8px;
}
.bar-track {
  position: absolute;
  left: 0;
  right: 0;
  bottom: 0;
  height: 4px;
  background: color-mix(in oklab, var(--background) 55%, transparent);
}
.bar-fill {
  height: 100%;
  background: var(--success);
}
.info {
  display: flex;
  flex-direction: column;
  gap: 3px;
  padding: 8px 10px 10px;
}
.name {
  margin: 0;
  font-size: 13px;
  font-weight: 600;
  color: var(--foreground);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.meta {
  display: flex;
  gap: 8px;
  margin: 0;
  font-size: 11.5px;
  color: var(--muted);
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.miss {
  color: var(--warning);
}
.fly {
  color: var(--accent);
}
@media (max-width: 720px) {
  .search {
    width: auto;
    flex: 1;
    margin-left: 0;
  }
  .grid {
    grid-template-columns: repeat(auto-fill, minmax(104px, 1fr));
    gap: 10px;
  }
}
</style>
