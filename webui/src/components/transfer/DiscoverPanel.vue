<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ArrowLeft } from '@lucide/vue'
import HButton from '@/components/hero/HButton.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSelect from '@/components/hero/HSelect.vue'
import CandidateGrid from '@/components/transfer/CandidateGrid.vue'
import { resourcesApi, transferApi } from '@/api'
import type { DiscoverFilter, DiscoverList, DiscoverOpt, TmdbCandidate } from '@/api/resources'
import type { OwnedInfo } from '@/api/transfer'

/**
 * 发现首页「查看全部」/「按条件筛选」点进来的整页海报墙（TMDB 榜单，能翻页）。点一部和搜索结果里点一部是同一回事：
 * 条目自带 TMDB 编号，直接进找资源。榜单清单由后端给（tmdbdiscover.go），这里不写死。
 * 下面一行按 电影 / 剧集 + 地区 + 题材 筛选（list=filter），选项同样由后端给；
 * 动了任何一项就换成筛选结果，点上面的榜单回到榜单。
 */
const props = defineProps<{
  /** 打开时先看哪个榜单；filter = 直接进按条件筛选 */
  initial?: string
}>()
const emit = defineEmits<{ pick: [TmdbCandidate]; back: [] }>()

const LS_KEY = 'transfer.discover.list'
const lists = ref<DiscoverList[]>([])
const current = ref('')
const items = ref<TmdbCandidate[]>([])
const owned = ref<Record<string, OwnedInfo>>({})
const page = ref(1)
const hasMore = ref(false)
const loading = ref(false)
const loadingMore = ref(false)
const hint = ref('')
// 切页签很快时，晚回来的旧榜单别盖掉新的
let seq = 0

// ---- 筛选 ----
const countries = ref<DiscoverOpt[]>([])
const genres = ref<Record<'movie' | 'tv', DiscoverOpt[]>>({ movie: [], tv: [] })
const filtering = ref(false)
const filter = ref<DiscoverFilter>({ kind: 'tv', country: '', genre: '' })
const KIND_OPTS = [
  { label: '剧集', value: 'tv' as const },
  { label: '电影', value: 'movie' as const },
]
const countryOpts = computed(() => [{ label: '全部地区', value: '' }, ...countries.value])
const genreOpts = computed(() => [{ label: '全部题材', value: '' }, ...(genres.value[filter.value.kind] ?? [])])

function setFilter(patch: Partial<DiscoverFilter>) {
  const f = { ...filter.value, ...patch }
  // 电影与剧集的题材表不一样（剧集是「动作冒险」「科幻奇幻」），换类型后对不上的题材清掉
  if (f.genre && !genres.value[f.kind]?.some((g) => g.value === f.genre)) f.genre = ''
  filter.value = f
  filtering.value = true
  void load()
}
function clearFilter() {
  filter.value = { ...filter.value, country: '', genre: '' }
  filtering.value = false
  void load()
}

async function load(more = false) {
  const my = ++seq
  const list = filtering.value ? 'filter' : current.value
  const f = filtering.value ? { ...filter.value } : undefined
  if (more) loadingMore.value = true
  else {
    loading.value = true
    items.value = []
    page.value = 1
    hint.value = ''
  }
  try {
    const p = more ? page.value + 1 : 1
    const d = await resourcesApi.tmdbDiscover(list, p, f)
    if (my !== seq) return
    const got = d.data ?? []
    // 榜单翻页期间排名会变，同一部可能在两页各出现一次
    const seen = new Set(items.value.map((c) => `${c.media_type}:${c.id}`))
    const fresh = got.filter((c) => !seen.has(`${c.media_type}:${c.id}`))
    items.value = [...items.value, ...fresh]
    page.value = p
    hasMore.value = !!d.has_more
    if (!items.value.length) hint.value = f ? '没有符合条件的影片，换个条件试试' : '这个榜单暂时是空的'
    if (fresh.length) {
      transferApi
        .owned(fresh.map((c) => `${c.media_type}:${c.id}`))
        .then((r) => (owned.value = { ...owned.value, ...(r.data ?? {}) }))
        .catch(() => {})
    }
  } catch (e) {
    if (my === seq && !more) hint.value = e instanceof Error ? e.message : '榜单读取失败'
  } finally {
    if (my === seq) {
      loading.value = false
      loadingMore.value = false
    }
  }
}

function switchTo(key: string) {
  current.value = key
  filtering.value = false
  try {
    localStorage.setItem(LS_KEY, key)
  } catch {
    // 存不了就每次从第一个榜单开始
  }
  void load()
}

onMounted(async () => {
  try {
    const d = await resourcesApi.tmdbDiscoverLists()
    lists.value = d.lists ?? []
    countries.value = d.countries ?? []
    if (d.genres) genres.value = d.genres
  } catch (e) {
    hint.value = e instanceof Error ? e.message : '榜单读取失败'
    return
  }
  if (props.initial === 'filter') {
    current.value = lists.value[0]?.key ?? ''
    setFilter({})
    return
  }
  let saved = ''
  try {
    saved = localStorage.getItem(LS_KEY) ?? ''
  } catch {
    // 同上
  }
  const want = props.initial || saved
  const first = lists.value.find((l) => l.key === want)?.key ?? lists.value[0]?.key
  if (first) switchTo(first)
})
</script>

<template>
  <div class="discover">
    <div class="head">
      <HButton variant="ghost" size="sm" @click="emit('back')">
        <template #icon><ArrowLeft :size="15" /></template>
        返回发现
      </HButton>
    </div>
    <HSegmented
      v-if="lists.length"
      :model-value="filtering ? '' : current"
      size="sm"
      aria-label="榜单"
      :options="lists.map((l) => ({ label: l.label, value: l.key }))"
      @update:model-value="(v) => v && (filtering || v !== current) && switchTo(v)"
    />
    <div v-if="countries.length" class="filters">
      <HSegmented
        :model-value="filtering ? filter.kind : ''"
        size="sm"
        aria-label="电影或剧集"
        :options="KIND_OPTS"
        @update:model-value="(v) => v && setFilter({ kind: v as 'movie' | 'tv' })"
      />
      <HSelect
        :model-value="filter.country"
        :options="countryOpts"
        aria-label="地区"
        class="f-sel"
        @update:model-value="(v) => setFilter({ country: (v as string) ?? '' })"
      />
      <HSelect
        :model-value="filter.genre"
        :options="genreOpts"
        aria-label="题材"
        class="f-sel"
        @update:model-value="(v) => setFilter({ genre: (v as string) ?? '' })"
      />
      <button v-if="filtering" type="button" class="clear" @click="clearFilter">清除筛选</button>
      <span v-else class="f-hint">选一项按条件筛选</span>
    </div>
    <CandidateGrid :items="items" :owned="owned" :loading="loading" :hint="hint" no-skip @pick="(c) => emit('pick', c)" />
    <div v-if="hasMore && !loading" class="more">
      <HButton variant="ghost" size="sm" :loading="loadingMore" @click="load(true)">加载更多</HButton>
    </div>
  </div>
</template>

<style scoped>
.discover {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.head {
  display: flex;
  align-items: center;
  margin-bottom: -6px;
}
.filters {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}
.f-sel {
  width: 132px;
}
.f-hint {
  font-size: 12px;
  color: var(--muted);
}
.clear {
  all: unset;
  font-size: 12.5px;
  color: var(--accent);
  cursor: pointer;
}
.clear:hover {
  text-decoration: underline;
}
.clear:focus-visible {
  outline: 2px solid var(--focus);
}
.more {
  display: flex;
  justify-content: center;
}
</style>
