<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { Check, Clapperboard, Ellipsis, Images, RefreshCw, Search, Sparkles, Tv } from '@lucide/vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HDropdown from '@/components/hero/HDropdown.vue'
import HSearchField from '@/components/hero/HSearchField.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSelect from '@/components/hero/HSelect.vue'
import HSkeleton from '@/components/hero/HSkeleton.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import ScrapeDialog from '@/components/local/ScrapeDialog.vue'
import { localApi } from '@/api'
import type { LocalTitle, LocalTitleSort, LocalTitleStats, LocalTitleStatus } from '@/api/local'
import { toastError } from '@/composables/useFeedback'
import { useQueueStore } from '@/stores/queue'

/**
 * 本地文件：本地媒体库里的片目卡片墙（一张卡片 = 台账里的一个片目），对所选片目刮削。
 *
 * 状态只看本地标题目录里有没有 NFO 与海报（后端 locallib.go），不发 115 请求。
 * 勾选跨筛选、跨翻页保留（按 key 记），批量刮削一次提交。
 * 刮削原来挂在网盘文件页，2026-09 挪到这里：只刮本地已经有的片目。
 */
const router = useRouter()
const queue = useQueueStore()

const PAGE = 60

const keyword = ref('')
const type = ref<'' | 'movie' | 'tv'>('')
const status = ref<'' | LocalTitleStatus>('')
const sort = ref<LocalTitleSort>('added_desc')

const items = ref<LocalTitle[]>([])
const total = ref(0)
const stats = ref<LocalTitleStats>({ all: 0, ok: 0, partial: 0, miss: 0, movie: 0, tv: 0 })
const configured = ref(true)
const missing = ref(0)
const loading = ref(false)
const loadingMore = ref(false)
const loaded = ref(false)

/** 勾选：key → 条目（换了筛选看不到的也还在） */
const selected = ref(new Map<string, LocalTitle>())

let seq = 0
async function load(refresh = false) {
  const my = ++seq
  loading.value = true
  try {
    const d = await localApi.listTitles({
      q: keyword.value.trim(), type: type.value, status: status.value, sort: sort.value, limit: PAGE, refresh,
    })
    if (my !== seq) return
    configured.value = d.configured
    items.value = d.items ?? []
    total.value = d.total
    stats.value = d.stats
    missing.value = d.missing ?? 0
    loaded.value = true
  } catch (e) {
    if (my === seq) toastError(e, '读取本地媒体库失败')
  } finally {
    if (my === seq) loading.value = false
  }
}

async function loadMore() {
  const my = seq
  loadingMore.value = true
  try {
    const d = await localApi.listTitles({
      q: keyword.value.trim(), type: type.value, status: status.value, sort: sort.value,
      offset: items.value.length, limit: PAGE,
    })
    if (my !== seq) return
    items.value = [...items.value, ...(d.items ?? [])]
    total.value = d.total
  } catch (e) {
    toastError(e, '读取本地媒体库失败')
  } finally {
    loadingMore.value = false
  }
}

watch([type, status, sort], () => load())
// 打字时不必每个字都查一次：停 300ms 再查（清空立即查）
let kwTimer: ReturnType<typeof setTimeout> | undefined
watch(keyword, (v) => {
  clearTimeout(kwTimer)
  kwTimer = setTimeout(() => load(), v ? 300 : 0)
})

const typeOptions = computed(() => [
  { label: '全部类型', value: '' as const },
  { label: `电影 ${stats.value.movie}`, value: 'movie' as const },
  { label: `剧集 ${stats.value.tv}`, value: 'tv' as const },
])
const statusOptions = computed(() => [
  { label: `全部 ${stats.value.all}`, value: '' as const },
  { label: `已刮削 ${stats.value.ok}`, value: 'ok' as const },
  { label: `缺一项 ${stats.value.partial}`, value: 'partial' as const },
  { label: `未刮削 ${stats.value.miss}`, value: 'miss' as const },
])
const SORTS: { label: string; value: LocalTitleSort }[] = [
  { label: '最近入库', value: 'added_desc' },
  { label: '片名', value: 'title' },
  { label: '年份（新→旧）', value: 'year_desc' },
  { label: '年份（旧→新）', value: 'year_asc' },
]

const STATUS_TEXT: Record<LocalTitleStatus, string> = { ok: '已刮削', partial: '缺一项', miss: '未刮削' }
const STATUS_TONE: Record<LocalTitleStatus, 'success' | 'warning' | 'danger'> = { ok: 'success', partial: 'warning', miss: 'danger' }

function partialText(t: LocalTitle) {
  if (t.status !== 'partial') return ''
  return t.has_nfo ? '缺海报' : '缺 NFO'
}

function metaLine(t: LocalTitle) {
  const parts = [t.media_type === 'tv' ? '剧集' : '电影']
  if (t.year) parts.push(t.year)
  if (t.media_type === 'tv') parts.push(`${t.videos} 集`)
  else if (t.videos > 1) parts.push(`${t.videos} 个视频`)
  return parts.join(' · ')
}

// ---- 勾选 ----

const selectedList = computed(() => [...selected.value.values()])
const pageAllChecked = computed(() => items.value.length > 0 && items.value.every((t) => selected.value.has(t.key)))

function toggle(t: LocalTitle) {
  const m = new Map(selected.value)
  if (m.has(t.key)) m.delete(t.key)
  else m.set(t.key, t)
  selected.value = m
}
function toggleShown() {
  const m = new Map(selected.value)
  const all = pageAllChecked.value
  for (const t of items.value) {
    if (all) m.delete(t.key)
    else m.set(t.key, t)
  }
  selected.value = m
}

// ---- 刮削 ----

const dialogTargets = ref<LocalTitle[]>([])
const dialogPreset = ref<'auto' | 'force' | 'pick'>('auto')
const showScrape = ref(false)

function openScrape(list: LocalTitle[], preset: 'auto' | 'force' | 'pick' = 'auto') {
  if (!list.length) return
  dialogTargets.value = list
  dialogPreset.value = preset
  showScrape.value = true
}

const CARD_MENU = [
  { key: 'auto', label: '刮削', icon: Images },
  { key: 'force', label: '强制重刮', icon: RefreshCw },
  { key: 'pick', label: '改指定 TMDB 条目', icon: Search },
]

function onCardAction(t: LocalTitle, key: string) {
  openScrape([t], key as 'auto' | 'force' | 'pick')
}

function onPosterError(e: Event) {
  // 海报读不出来（刚被删 / 格式不认）：退回占位，别显示破图
  ;(e.target as HTMLImageElement).style.display = 'none'
}

// 刮削跑完：本地产物变了，跳过后端缓存重列一次，清掉勾选
const offFinished = queue.onFinished((j) => {
  if (j.kind === 'scrape') {
    selected.value = new Map()
    void load(true)
  }
})
onBeforeUnmount(() => {
  offFinished()
  clearTimeout(kwTimer)
})

onMounted(() => load())
</script>

<template>
  <div class="page">
    <SectionCard title="本地文件" hint="本地媒体库里的影片与剧集，对所选片目刮削 NFO 与海报">
      <template #extra>
        <HButton variant="ghost" size="sm" :loading="loading" @click="load(true)"><RefreshCw :size="14" />刷新</HButton>
      </template>

      <HAlert v-if="!configured" status="warning" class="tip">
        还没有配置本地媒体库根目录（STRM 输出目录）。
        <template #actions>
          <HButton variant="tertiary" size="sm" @click="router.push({ name: 'sync' })">去 Strm 管理</HButton>
        </template>
      </HAlert>
      <template v-else>
        <div class="toolbar">
          <HSearchField v-model="keyword" class="filter" placeholder="搜索片名 / 路径 / TMDB 编号" />
          <HSegmented v-model="type" :options="typeOptions" size="sm" aria-label="类型" />
          <HSegmented v-model="status" :options="statusOptions" size="sm" aria-label="刮削状态" />
          <HSelect v-model="sort" :options="SORTS" aria-label="排序" class="sort" />
        </div>

        <div class="batch">
          <span class="sel-count">已选 {{ selected.size }} 部</span>
          <HButton variant="tertiary" size="sm" :disabled="!items.length" @click="toggleShown">
            {{ pageAllChecked ? '取消勾选已显示' : '勾选已显示' }}
          </HButton>
          <HButton variant="tertiary" size="sm" :disabled="!selected.size" @click="selected = new Map()">清除</HButton>
          <HButton variant="secondary" size="sm" :disabled="!selected.size" @click="openScrape(selectedList)">
            <Images :size="14" />批量刮削
          </HButton>
        </div>

        <HAlert v-if="missing" status="warning" class="tip">
          有 {{ missing }} 个片目台账里有、本地却找不到目录（被手工删掉，或挂载还没就绪）。刮削会重新建出目录并写入。
        </HAlert>

        <div v-if="loading && !loaded" class="grid" aria-busy="true">
          <div v-for="i in 12" :key="i" class="card-sk">
            <HSkeleton width="100%" height="auto" radius="10px" class="sk-poster" />
            <HSkeleton width="70%" height="13px" radius="999px" />
            <HSkeleton width="45%" height="11px" radius="999px" />
          </div>
        </div>
        <EmptyState
          v-else-if="!items.length"
          :text="keyword || type || status ? '没有符合条件的片目' : '台账里还没有片目：先跑一次全量同步或自动整理'"
        />
        <div v-else class="grid" :class="{ dim: loading }">
          <article
            v-for="t in items"
            :key="t.key"
            class="title-card"
            :class="{ checked: selected.has(t.key) }"
          >
            <button
              type="button"
              class="poster"
              :aria-pressed="selected.has(t.key)"
              :aria-label="`选择 ${t.title}`"
              :title="t.key"
              @click="toggle(t)"
            >
              <div class="poster-none">
                <Tv v-if="t.media_type === 'tv'" :size="28" />
                <Clapperboard v-else :size="28" />
              </div>
              <img v-if="t.poster" :src="localApi.posterUrl(t)" :alt="t.title" loading="lazy" @error="onPosterError" />
              <span class="check" aria-hidden="true"><Check :size="14" /></span>
              <HChip :color="STATUS_TONE[t.status]" variant="primary" size="sm" class="badge">
                {{ t.status === 'partial' ? partialText(t) : STATUS_TEXT[t.status] }}
              </HChip>
            </button>
            <div class="info">
              <div class="info-text">
                <h3 class="name" :title="t.title">{{ t.title }}</h3>
                <p class="meta">
                  {{ metaLine(t) }}
                  <span v-if="!t.tmdb_id" class="no-id" title="目录名里没有 TMDB 编号：刮削时按片名识别">
                    <Sparkles :size="11" />无编号
                  </span>
                </p>
              </div>
              <HDropdown :options="CARD_MENU" align="end" @select="(k) => onCardAction(t, k)">
                <HButton variant="ghost" size="sm" icon-only :aria-label="`${t.title} 的操作`"><Ellipsis :size="16" /></HButton>
              </HDropdown>
            </div>
          </article>
        </div>

        <div v-if="items.length < total" class="more">
          <HButton variant="tertiary" size="sm" :loading="loadingMore" @click="loadMore">
            再显示 {{ Math.min(PAGE, total - items.length) }} 部（共 {{ total }} 部）
          </HButton>
        </div>
      </template>
    </SectionCard>

    <ScrapeDialog v-model:show="showScrape" :targets="dialogTargets" :preset="dialogPreset" />
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 16px;
  min-width: 0;
}
.toolbar,
.batch {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}
.filter {
  flex: 1 1 200px;
  min-width: 0;
  max-width: 320px;
}
.sort {
  width: 150px;
}
.batch {
  justify-content: flex-end;
}
.sel-count {
  margin-right: auto;
  font-size: 12.5px;
  color: var(--muted);
}
.tip {
  margin: 0;
}

.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: 16px;
  transition: opacity 0.15s;
}
.grid.dim {
  opacity: 0.6;
}
.card-sk {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.sk-poster {
  aspect-ratio: 2 / 3;
}

.title-card {
  display: flex;
  flex-direction: column;
  min-width: 0;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  overflow: hidden;
  box-shadow: 0 0 0 2px transparent;
  transition: box-shadow 0.15s;
}
.title-card.checked {
  box-shadow: 0 0 0 2px var(--accent);
}
.poster {
  all: unset;
  position: relative;
  display: block;
  aspect-ratio: 2 / 3;
  background: var(--surface-tertiary, var(--surface-secondary));
  cursor: pointer;
  overflow: hidden;
}
.poster:focus-visible {
  box-shadow: inset 0 0 0 2px var(--focus);
}
.poster img {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  object-fit: cover;
}
.poster-none {
  position: absolute;
  inset: 0;
  display: grid;
  place-items: center;
  color: var(--muted);
}
.check {
  position: absolute;
  top: 8px;
  left: 8px;
  display: grid;
  place-items: center;
  width: 22px;
  height: 22px;
  border-radius: 999px;
  background: color-mix(in oklab, var(--background) 70%, transparent);
  box-shadow: inset 0 0 0 1.5px color-mix(in oklab, var(--foreground) 45%, transparent);
  color: transparent;
  opacity: 0;
  transition: opacity 0.15s;
}
@media (hover: hover) {
  .poster:hover .check {
    opacity: 1;
  }
}
.title-card.checked .check {
  opacity: 1;
  background: var(--accent);
  box-shadow: none;
  color: var(--accent-foreground);
}
.badge {
  position: absolute;
  top: 8px;
  right: 8px;
}
.info {
  display: flex;
  align-items: flex-start;
  gap: 4px;
  padding: 8px 6px 10px 10px;
}
.info-text {
  flex: 1;
  min-width: 0;
}
.name {
  margin: 0;
  font-size: 13.5px;
  font-weight: 600;
  line-height: 1.4;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.meta {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 2px 6px;
  margin: 2px 0 0;
  font-size: 12px;
  color: var(--muted);
}
.no-id {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  color: var(--warning);
}
.more {
  display: flex;
  justify-content: center;
}

@media (max-width: 720px) {
  .grid {
    grid-template-columns: repeat(auto-fill, minmax(110px, 1fr));
    gap: 10px;
  }
  .filter {
    max-width: none;
    flex-basis: 100%;
  }
  /* 手机上没有悬停：勾选圈常显 */
  .check {
    opacity: 1;
  }
}
</style>
