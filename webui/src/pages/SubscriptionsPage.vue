<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Clapperboard, Plus, Tv } from '@lucide/vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HSearchField from '@/components/hero/HSearchField.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSkeleton from '@/components/hero/HSkeleton.vue'
import HTabs from '@/components/hero/HTabs.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import PosterImage from '@/components/PosterImage.vue'
import SubDetail from '@/components/subscribe/SubDetail.vue'
import SubscribeDialog from '@/components/subscribe/SubscribeDialog.vue'
import SubSettings from '@/pages/subscribe/SubSettings.vue'
import { resourcesApi } from '@/api'
import * as subscribeApi from '@/api/subscribe'
import type { Subscription } from '@/api/subscribe'
import { STATE_TEXT, STATE_TONE, scopeText } from '@/api/subscribe'
import { toastError } from '@/composables/useFeedback'
import { useTabQuery } from '@/composables/useTabQuery'
import { useQueueStore } from '@/stores/queue'

/**
 * 资源订阅：海报墙 + 详情抽屉（?sub= 打开）。新建订阅在「影视转存 → 找资源」选定影片后点「订阅」，
 * 这一页只管看和改 —— 选片的流程那边已经有了，别在这里再做一份 TMDB 搜索。
 */
const route = useRoute()
const router = useRouter()
const queue = useQueueStore()

const tab = useTabQuery('list')

const rows = ref<Subscription[]>([])
const loading = ref(false)
const loaded = ref(false)
const filter = ref<'all' | 'active' | 'stalled' | 'paused' | 'done'>('all')
const keyword = ref('')

async function load() {
  loading.value = true
  try {
    rows.value = (await subscribeApi.list()).data ?? []
    loaded.value = true
  } catch (e) {
    toastError(e, '读取订阅失败')
  } finally {
    loading.value = false
  }
}
onMounted(load)
const off = queue.onFinished((j) => {
  if (j.kind === 'subscribe') void load()
})
onBeforeUnmount(off)

const counts = computed(() => {
  const n = { all: rows.value.length, active: 0, stalled: 0, paused: 0, done: 0 }
  for (const r of rows.value) n[r.state]++
  return n
})
const filterOptions = computed(() => [
  { label: `全部 ${counts.value.all}`, value: 'all' as const },
  { label: `追更中 ${counts.value.active}`, value: 'active' as const },
  { label: `长期找不到 ${counts.value.stalled}`, value: 'stalled' as const },
  { label: `已暂停 ${counts.value.paused}`, value: 'paused' as const },
  { label: `已完成 ${counts.value.done}`, value: 'done' as const },
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

// ---- 详情抽屉：?sub= ----
const detailId = ref<number | null>(Number(route.query.sub) || null)
const detailOpen = ref(!!detailId.value)
function openDetail(r: Subscription) {
  detailId.value = r.id
  detailOpen.value = true
}
watch(detailOpen, (open) => {
  const q = { ...route.query, sub: open && detailId.value ? String(detailId.value) : undefined }
  void router.replace({ query: q })
})

// ---- 修改 ----
const editing = ref<Subscription | null>(null)
const editOpen = ref(false)
function onEdit(s: Subscription) {
  editing.value = s
  editOpen.value = true
}
function onSaved() {
  void load()
  if (detailOpen.value) {
    // 抽屉里的数据跟着刷新：关一下再开最省事，但会闪；让它自己的 watch 重新拉
    const id = detailId.value
    detailId.value = null
    setTimeout(() => (detailId.value = id), 0)
  }
}
function onRemoved(id: number) {
  rows.value = rows.value.filter((r) => r.id !== id)
}
</script>

<template>
  <div class="page">
    <HTabs
      v-model="tab"
      :items="[
        { value: 'list', label: '订阅', count: counts.active + counts.stalled },
        { value: 'settings', label: '设置' },
      ]"
      variant="secondary"
    />

    <template v-if="tab === 'list'">
      <div class="bar">
        <HSegmented v-model="filter" :options="filterOptions" size="sm" class="filters" />
        <HSearchField v-model="keyword" placeholder="片名或 TMDB ID" class="search" />
        <HButton variant="primary" size="sm" @click="router.push({ name: 'media-transfer' })">
          <Plus :size="14" />新订阅
        </HButton>
      </div>
      <p class="hint">在「影视转存 → 找资源」选定影片后点「订阅」。剧集默认补缺集，115 分享只转缺的那几集，转存后自动整理入库。</p>

      <div v-if="loading && !loaded" class="grid" aria-busy="true">
        <div v-for="i in 8" :key="i" class="sk">
          <HSkeleton width="100%" height="auto" radius="10px" class="sk-poster" />
          <HSkeleton width="70%" height="13px" radius="999px" />
        </div>
      </div>
      <EmptyState
        v-else-if="!items.length"
        :text="rows.length ? '没有符合条件的订阅' : '还没有订阅：到「影视转存 → 找资源」选一部影片，点「订阅」'"
      />
      <div v-else class="grid" :class="{ dim: loading }">
        <button v-for="r in items" :key="r.id" type="button" class="card" :class="`st-${r.state}`" @click="openDetail(r)">
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
    </template>

    <SubSettings v-else />

    <SubDetail v-model:show="detailOpen" :sub-id="detailId" @edit="onEdit" @changed="load" @removed="onRemoved" />
    <SubscribeDialog v-model:show="editOpen" :edit="editing" @saved="onSaved" />
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
.hint {
  margin: -4px 0 0;
  font-size: 12px;
  color: var(--muted);
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
