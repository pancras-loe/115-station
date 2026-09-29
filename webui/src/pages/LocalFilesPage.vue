<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Check, Clapperboard, Ellipsis, Images, Info, RefreshCw, Search, Sparkles, Tv, X } from '@lucide/vue'
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
import TitleDetail from '@/components/local/TitleDetail.vue'
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
 *
 * 卡片只放要紧的（海报、片名、刮削状态）；每个文件在不在、Emby 探测到的音视频字幕轨道，
 * 点海报进详情抽屉看（TitleDetail，地址带 ?title= 可以直接打开、返回键关闭）。
 * 点左上角的圆圈勾选；已经勾了至少一部时，点海报也是勾选（批量挑片时不必瞄准小圆圈）。
 *
 * 版面：筛选栏与批量栏固定，只有海报墙在自己的容器里滚动（桌面 / 平板）。
 * 手机屏幕矮，固定区会吃掉半屏，退回整页滚动，勾选后批量栏浮在底栏上方。
 */
const route = useRoute()
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

const scroller = ref<HTMLElement | null>(null)

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

// 换了筛选：结果从头来，滚动位置也回到顶
function reload() {
  scroller.value?.scrollTo({ top: 0 })
  void load()
}
watch([type, status, sort], reload)
// 打字时不必每个字都查一次：停 300ms 再查（清空立即查）
let kwTimer: ReturnType<typeof setTimeout> | undefined
watch(keyword, (v) => {
  clearTimeout(kwTimer)
  kwTimer = setTimeout(reload, v ? 300 : 0)
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
const selecting = computed(() => selected.value.size > 0)

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
function clearSelection() {
  selected.value = new Map()
}

/** 点海报：勾选模式下是勾选，否则打开详情 */
function onPoster(t: LocalTitle) {
  if (selecting.value) toggle(t)
  else openDetail(t)
}

// ---- 详情（地址带 ?title=，返回键即关闭）----

const detailKey = computed(() => (typeof route.query.title === 'string' ? route.query.title : null))
const showDetail = computed({
  get: () => !!detailKey.value,
  set: (v) => {
    if (!v) closeDetail()
  },
})
/** 详情是这个页面自己推进历史的：关闭时退回去；从外部链接直接打开的就只去掉参数 */
let pushedDetail = false

function openDetail(t: LocalTitle) {
  pushedDetail = true
  void router.push({ query: { ...route.query, title: t.key } })
}
function closeDetail() {
  if (!detailKey.value) return
  if (pushedDetail && window.history.state?.back) {
    pushedDetail = false
    router.back()
    return
  }
  const q = { ...route.query }
  delete q.title
  void router.replace({ query: q })
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
  { key: 'detail', label: '查看详情', icon: Info },
  { key: 'auto', label: '刮削', icon: Images },
  { key: 'force', label: '强制重刮', icon: RefreshCw },
  { key: 'pick', label: '改指定 TMDB 条目', icon: Search },
]

function onCardAction(t: LocalTitle, key: string) {
  if (key === 'detail') openDetail(t)
  else openScrape([t], key as 'auto' | 'force' | 'pick')
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

// ---- 版面高度：页面正好占满视口，海报墙在里面滚 ----
//
// 顶栏高度随标题 / 描述 / 安全区变化，写死常量总会差几像素（多出来的就是整页也跟着滚），
// 所以量出本页顶端到视口顶的距离
const pageEl = ref<HTMLElement | null>(null)
const pageTop = ref(0)
const pageBottom = ref(24)
function measure() {
  const el = pageEl.value
  if (!el) return
  pageTop.value = Math.round(el.getBoundingClientRect().top + window.scrollY)
  // 内容区的下内边距随断点变（28 / 24 / 手机上还要让出底栏），照实扣掉，否则整页会多滚几像素
  const pad = el.parentElement ? parseFloat(getComputedStyle(el.parentElement).paddingBottom) : NaN
  if (Number.isFinite(pad)) pageBottom.value = Math.round(pad)
}
let ro: ResizeObserver | undefined
onMounted(async () => {
  void load()
  await nextTick()
  measure()
  setTimeout(measure, 250) // 切页动画（位移 4px）结束后再量一次
  window.addEventListener('resize', measure)
  // 顶栏在窄屏时会折行，高度一变页面顶端就跟着动
  const topbar = document.querySelector('.topbar')
  if (topbar && 'ResizeObserver' in window) {
    ro = new ResizeObserver(measure)
    ro.observe(topbar)
  }
})
onBeforeUnmount(() => {
  offFinished()
  clearTimeout(kwTimer)
  window.removeEventListener('resize', measure)
  ro?.disconnect()
})
</script>

<template>
  <div ref="pageEl" class="page" :style="{ '--page-top': `${pageTop}px`, '--page-bottom': `${pageBottom}px` }">
    <SectionCard title="本地文件" hint="本地媒体库里的影片与剧集。点海报看详情，点左上角圆圈勾选后批量刮削" class="shell">
      <template #extra>
        <HButton variant="ghost" size="sm" :loading="loading" @click="load(true)"><RefreshCw :size="14" />刷新</HButton>
      </template>

      <HAlert v-if="!configured" status="warning" class="tip">
        还没有配置本地媒体库根目录（STRM 输出目录）。
        <template #actions>
          <HButton variant="tertiary" size="sm" @click="router.push({ name: 'sync' })">去 Strm 管理</HButton>
        </template>
      </HAlert>
      <div v-else class="body">
        <!-- 固定区：筛选 + 批量 -->
        <div class="lf-head">
          <div class="toolbar">
            <HSearchField v-model="keyword" class="filter" placeholder="搜索片名 / 路径 / TMDB 编号" />
            <div class="segs">
              <HSegmented v-model="type" :options="typeOptions" size="sm" aria-label="类型" />
              <HSegmented v-model="status" :options="statusOptions" size="sm" aria-label="刮削状态" />
            </div>
            <HSelect v-model="sort" :options="SORTS" aria-label="排序" class="sort" />
          </div>

          <div class="batch" :class="{ active: selecting }">
            <span class="sel-count">
              <template v-if="selecting">已选 <b>{{ selected.size }}</b> 部</template>
              <template v-else>共 {{ total }} 部<span class="sel-tip">，勾选后可批量刮削</span></template>
            </span>
            <div class="batch-btns">
              <HButton variant="tertiary" size="sm" :disabled="!items.length" @click="toggleShown">
                {{ pageAllChecked ? '取消勾选已显示' : `勾选已显示（${items.length}）` }}
              </HButton>
              <HButton variant="tertiary" size="sm" :disabled="!selecting" @click="clearSelection">清除</HButton>
              <HButton variant="primary" size="sm" :disabled="!selecting" @click="openScrape(selectedList)">
                <Images :size="14" />批量刮削
              </HButton>
            </div>
          </div>

          <HAlert v-if="missing" status="warning" class="tip">
            有 {{ missing }} 个片目台账里有、本地却找不到目录（被手工删掉，或挂载还没就绪）。
          </HAlert>
        </div>

        <!-- 滚动区：海报墙 -->
        <div ref="scroller" class="scroller">
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
          <div v-else class="grid" :class="{ dim: loading, selecting }">
            <article
              v-for="t in items"
              :key="t.key"
              class="title-card"
              :class="{ checked: selected.has(t.key), active: detailKey === t.key }"
            >
              <div class="poster-wrap">
                <button
                  type="button"
                  class="poster"
                  :aria-label="selecting ? `选择 ${t.title}` : `查看 ${t.title} 的详情`"
                  :title="t.key"
                  @click="onPoster(t)"
                >
                  <div class="poster-none">
                    <Tv v-if="t.media_type === 'tv'" :size="28" />
                    <Clapperboard v-else :size="28" />
                  </div>
                  <img v-if="t.poster" :src="localApi.posterUrl(t)" :alt="t.title" loading="lazy" @error="onPosterError" />
                  <HChip :color="STATUS_TONE[t.status]" variant="primary" size="sm" class="badge">
                    {{ t.status === 'partial' ? partialText(t) : STATUS_TEXT[t.status] }}
                  </HChip>
                </button>
                <button
                  type="button"
                  class="check"
                  role="checkbox"
                  :aria-checked="selected.has(t.key)"
                  :aria-label="`勾选 ${t.title}`"
                  @click="toggle(t)"
                >
                  <span class="check-dot"><Check :size="14" /></span>
                </button>
              </div>
              <div class="info">
                <button type="button" class="info-text" @click="openDetail(t)">
                  <h3 class="name" :title="t.title">{{ t.title }}</h3>
                  <p class="meta">
                    {{ metaLine(t) }}
                    <span v-if="!t.tmdb_id" class="no-id" title="目录名里没有 TMDB 编号：刮削时按片名识别">
                      <Sparkles :size="11" />无编号
                    </span>
                  </p>
                </button>
                <HDropdown :options="CARD_MENU" align="end" @select="(k) => onCardAction(t, k)">
                  <HButton variant="ghost" size="sm" icon-only :aria-label="`${t.title} 的操作`"><Ellipsis :size="16" /></HButton>
                </HDropdown>
              </div>
            </article>
          </div>

          <div v-if="items.length < total" class="more">
            <HButton variant="tertiary" size="sm" :loading="loadingMore" @click="loadMore">
              再显示 {{ Math.min(PAGE, total - items.length) }} 部（已显示 {{ items.length }} / {{ total }}）
            </HButton>
          </div>
        </div>
      </div>
    </SectionCard>

    <!-- 手机：勾选后批量栏浮在底栏上方 -->
    <Transition name="float">
      <div v-if="selecting" class="float-bar">
        <span>已选 <b>{{ selected.size }}</b> 部</span>
        <HButton variant="ghost" size="sm" icon-only aria-label="清除勾选" @click="clearSelection"><X :size="16" /></HButton>
        <HButton variant="primary" size="sm" @click="openScrape(selectedList)"><Images :size="14" />批量刮削</HButton>
      </div>
    </Transition>

    <TitleDetail v-model:show="showDetail" :title-key="detailKey" @scrape="(t, p) => openScrape([t], p)" />
    <!-- 必须写在详情后面：两者 z-index 相同、按 Portal 落点先后叠放，从详情里点「刮削」弹窗要在上面 -->
    <ScrapeDialog v-model:show="showScrape" :targets="dialogTargets" :preset="dialogPreset" />
  </div>
</template>

<style scoped>
/* 页面正好占满视口剩余高度（--page-top / --page-bottom 由脚本量出） */
.page {
  display: flex;
  flex-direction: column;
  height: calc(100dvh - var(--page-top, 90px) - var(--page-bottom, 28px));
  min-height: 480px;
  min-width: 0;
}
.shell {
  flex: 1;
  min-height: 0;
}
.shell :deep(.section-body) {
  display: flex;
  flex-direction: column;
  flex: 1;
  min-height: 0;
}
.body {
  display: flex;
  flex-direction: column;
  flex: 1;
  min-height: 0;
  gap: 12px;
}
.lf-head {
  display: flex;
  flex-direction: column;
  flex: none;
  gap: 12px;
}
.toolbar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px 10px;
}
.segs {
  display: flex;
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
  margin-left: auto;
}
.tip {
  margin: 0;
}

/* 批量栏：单独一条浅底，按钮有呼吸空间；有勾选时换成强调色描边 */
.batch {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px 12px;
  min-height: 52px;
  padding: 8px 10px 8px 16px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  box-shadow: inset 0 0 0 1px transparent;
  transition: box-shadow 0.15s, background 0.15s;
}
.batch.active {
  background: color-mix(in oklab, var(--accent) 8%, var(--surface-secondary));
  box-shadow: inset 0 0 0 1px color-mix(in oklab, var(--accent) 35%, transparent);
}
.sel-count {
  margin-right: auto;
  font-size: 13px;
  color: var(--muted);
}
.sel-count b {
  color: var(--accent);
  font-size: 15px;
  font-variant-numeric: tabular-nums;
}
.batch-btns {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

/* 滚动区 */
.scroller {
  flex: 1;
  min-height: 0;
  margin: 0 -8px;
  padding: 4px 8px 8px;
  overflow-y: auto;
  overscroll-behavior: contain;
  scrollbar-gutter: stable;
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
  transition: box-shadow 0.15s, transform 0.15s;
}
.title-card.checked {
  box-shadow: 0 0 0 2px var(--accent);
}
.title-card.active:not(.checked) {
  box-shadow: 0 0 0 2px color-mix(in oklab, var(--accent) 45%, transparent);
}
.poster-wrap {
  position: relative;
}
.poster {
  all: unset;
  position: relative;
  display: block;
  width: 100%;
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
  transition: transform 0.25s;
}
@media (hover: hover) {
  .poster:hover img {
    transform: scale(1.03);
  }
}
.poster-none {
  position: absolute;
  inset: 0;
  display: grid;
  place-items: center;
  color: var(--muted);
}
/* 勾选圈：点击区 36px，比看得见的圆大，手指也好点 */
.check {
  all: unset;
  position: absolute;
  top: 2px;
  left: 2px;
  display: grid;
  place-items: center;
  width: 36px;
  height: 36px;
  cursor: pointer;
}
.check-dot {
  display: grid;
  place-items: center;
  width: 22px;
  height: 22px;
  border-radius: 999px;
  background: color-mix(in oklab, var(--background) 70%, transparent);
  box-shadow: inset 0 0 0 1.5px color-mix(in oklab, var(--foreground) 45%, transparent);
  color: transparent;
  opacity: 0;
  transition: opacity 0.15s, background 0.15s;
}
.check:focus-visible .check-dot {
  opacity: 1;
  box-shadow: 0 0 0 2px var(--focus);
}
@media (hover: hover) {
  .poster-wrap:hover .check-dot {
    opacity: 1;
  }
}
/* 进入勾选模式后所有卡片都露出圆圈：提示「点海报也是勾选」 */
.grid.selecting .check-dot {
  opacity: 1;
}
.title-card.checked .check-dot {
  opacity: 1;
  background: var(--accent);
  box-shadow: none;
  color: var(--accent-foreground);
}
.badge {
  position: absolute;
  top: 8px;
  right: 8px;
  pointer-events: none;
}
.info {
  display: flex;
  align-items: flex-start;
  gap: 4px;
  padding: 8px 6px 10px 10px;
}
.info-text {
  all: unset;
  flex: 1;
  min-width: 0;
  cursor: pointer;
}
.info-text:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
  border-radius: 4px;
}
.info-text:hover .name {
  color: var(--accent);
}
.name {
  margin: 0;
  font-size: 13.5px;
  font-weight: 600;
  line-height: 1.4;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  transition: color 0.15s;
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
  padding: 16px 0 4px;
}

.float-bar {
  display: none;
}

/* 手机：整页滚动，固定区不再固定；勾选后浮出底部批量栏 */
@media (max-width: 720px) {
  .page {
    height: auto;
    min-height: 0;
  }
  .shell,
  .shell :deep(.section-body),
  .body,
  .scroller {
    flex: none;
    min-height: auto;
  }
  .scroller {
    margin: 0;
    padding: 0;
    overflow: visible;
  }
  .grid {
    grid-template-columns: repeat(auto-fill, minmax(110px, 1fr));
    gap: 10px;
  }
  .filter {
    max-width: none;
    flex-basis: 100%;
  }
  .segs {
    flex-wrap: nowrap;
    width: 100%;
    overflow-x: auto;
    scrollbar-width: none;
  }
  .sort {
    width: 100%;
    margin-left: 0;
  }
  .sel-tip {
    display: none;
  }
  .batch.active .batch-btns > :not(:first-child) {
    display: none; /* 清除 / 批量刮削交给底部浮条 */
  }
  /* 手机上没有悬停：勾选圈常显 */
  .check-dot {
    opacity: 1;
  }
  .float-bar {
    position: fixed;
    left: 16px;
    right: 16px;
    bottom: calc(var(--tabbar-h) + env(safe-area-inset-bottom) + 10px);
    z-index: 20;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 8px 8px 16px;
    border-radius: var(--r-xl);
    background: var(--overlay, var(--surface));
    box-shadow: var(--overlay-shadow);
    font-size: 13px;
  }
  .float-bar > span {
    margin-right: auto;
  }
  .float-bar b {
    color: var(--accent);
  }
}
.float-enter-active,
.float-leave-active {
  transition: opacity 0.18s, transform 0.18s;
}
.float-enter-from,
.float-leave-to {
  opacity: 0;
  transform: translateY(12px);
}
</style>
