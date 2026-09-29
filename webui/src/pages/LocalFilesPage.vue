<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Check, Clapperboard, Ellipsis, Grid3x3, Images, Info, LayoutGrid, List, RefreshCw, ScanSearch, Search, Sparkles, Square, Tv, X } from '@lucide/vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HCheckbox from '@/components/hero/HCheckbox.vue'
import HChip from '@/components/hero/HChip.vue'
import HDropdown from '@/components/hero/HDropdown.vue'
import HSearchField from '@/components/hero/HSearchField.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSelect from '@/components/hero/HSelect.vue'
import HSkeleton from '@/components/hero/HSkeleton.vue'
import HSpinner from '@/components/hero/HSpinner.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import ScrapeDialog from '@/components/local/ScrapeDialog.vue'
import TitleDetail from '@/components/local/TitleDetail.vue'
import { localApi } from '@/api'
import type { LocalEmbyStats, LocalTitle, LocalTitleList, LocalTitleSort, LocalTitleStats, LocalTitleStatus } from '@/api/local'
import { STATUS_TONE, probeLabel, probeTitle, statusLabel, statusTitle } from '@/utils/localStatus'
import { toastError } from '@/composables/useFeedback'
import { useQueueStore } from '@/stores/queue'

/**
 * 本地文件：本地媒体库里的片目卡片墙（一张卡片 = 台账里的一个片目），对所选片目刮削。
 *
 * 状态看片目级 / 季 / 每集的 NFO、海报、背景图齐不齐（后端 localdetail.go 的 grade，与详情抽屉同口径），
 * 角标直接写缺什么；只读本地，不发 115 请求。
 * Emby 媒体信息（有没有探测过音视频轨道）是另一个维度：打开页面时向 Emby 拉一次快照（localemby.go），
 * 拉完原地重读列表，卡片左下角标「未探测 N」，筛选栏多一个「缺媒体信息」。
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
/** 只看 Emby 里还缺媒体信息的（有 Emby 快照才显示这个开关） */
const probeLack = ref(false)
const sort = ref<LocalTitleSort>('added_desc')

const items = ref<LocalTitle[]>([])
const total = ref(0)
const stats = ref<LocalTitleStats>({ all: 0, ok: 0, partial: 0, miss: 0, movie: 0, tv: 0, probe_lack: 0 })
const configured = ref(true)
const missing = ref(0)
const loading = ref(false)
const loadingMore = ref(false)
const loaded = ref(false)

/** 勾选：key → 条目（换了筛选看不到的也还在） */
const selected = ref(new Map<string, LocalTitle>())

const scroller = ref<HTMLElement | null>(null)

/** 当前筛选（列表、翻页、全选共用） */
function filters() {
  return {
    q: keyword.value.trim(), type: type.value, status: status.value, sort: sort.value,
    probe: probeLack.value ? ('lack' as const) : ('' as const),
  }
}

function apply(d: LocalTitleList) {
  configured.value = d.configured
  total.value = d.total
  stats.value = d.stats
  missing.value = d.missing ?? 0
  loaded.value = true
}

let seq = 0
async function load(refresh = false) {
  const my = ++seq
  matched.value = null // 筛选变了，上次全选拿到的那一批不再代表当前结果
  loading.value = true
  try {
    const d = await localApi.listTitles({ ...filters(), limit: PAGE, refresh })
    if (my !== seq) return
    items.value = d.items ?? []
    apply(d)
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
    const d = await localApi.listTitles({ ...filters(), offset: items.value.length, limit: PAGE })
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
watch([type, status, sort, probeLack], reload)
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
  { label: `不完整 ${stats.value.partial}`, value: 'partial' as const },
  { label: `未刮削 ${stats.value.miss}`, value: 'miss' as const },
])
const SORTS: { label: string; value: LocalTitleSort }[] = [
  { label: '最近入库', value: 'added_desc' },
  { label: '片名', value: 'title' },
  { label: '年份（新→旧）', value: 'year_desc' },
  { label: '年份（旧→新）', value: 'year_asc' },
]

// ---- 显示方案：大 / 中 / 小卡片、列表。记在本浏览器（只是个人偏好，读不到就用默认的中） ----

type ViewMode = 'lg' | 'md' | 'sm' | 'list'
const VIEW_KEY = 'local.view'
const VIEWS: { label: string; value: ViewMode; icon: typeof Square }[] = [
  { label: '大卡片', value: 'lg', icon: Square },
  { label: '中卡片', value: 'md', icon: LayoutGrid },
  { label: '小卡片', value: 'sm', icon: Grid3x3 },
  { label: '列表', value: 'list', icon: List },
]
function readView(): ViewMode {
  try {
    const v = localStorage.getItem(VIEW_KEY)
    if (VIEWS.some((o) => o.value === v)) return v as ViewMode
  } catch {
    // 隐私模式 / 禁用站点数据：用默认
  }
  return 'md'
}
const view = ref<ViewMode>(readView())
watch(view, (v) => {
  try {
    localStorage.setItem(VIEW_KEY, v)
  } catch {
    // 存不下就只在这次生效
  }
})

// ---- Emby 媒体信息：打开页面（与点「刷新」）时拉一次快照，平时不拉 ----

const emby = ref<LocalEmbyStats | null>(null)
const embyLoading = ref(false)
const embyError = ref('')

async function loadEmby(refresh = false) {
  embyLoading.value = true
  embyError.value = ''
  try {
    const d = await localApi.embyStats(refresh)
    emby.value = d
    embyError.value = d.error ?? ''
    if (d.ready) await refreshInPlace()
  } catch (e) {
    embyError.value = e instanceof Error ? e.message : String(e)
  } finally {
    embyLoading.value = false
  }
}

/**
 * 原地重读已经显示的这些卡片（不回到顶部、不闪骨架）：Emby 快照到了，角标要补上。
 * 抢一个新的 seq：正在飞的 load 结果作废（它不带 Emby 计数），loading 由这里收尾
 */
async function refreshInPlace() {
  const my = ++seq
  const want = Math.max(PAGE, items.value.length)
  const got: LocalTitle[] = []
  let last: LocalTitleList | undefined
  try {
    while (got.length < want) {
      const d = await localApi.listTitles({ ...filters(), offset: got.length, limit: Math.min(200, want - got.length) })
      if (my !== seq) return
      last = d
      got.push(...(d.items ?? []))
      if (!d.items?.length || got.length >= d.total) break
    }
    if (!last) return
    items.value = got
    apply(last)
  } catch (e) {
    if (my === seq) toastError(e, '读取本地媒体库失败')
  } finally {
    if (my === seq) loading.value = false
  }
}

function refreshAll() {
  void load(true)
  void loadEmby(true)
}

function metaLine(t: LocalTitle) {
  // 剧集有「N 集」就不再写「剧集」：中卡片一行只放得下十来个字，三段全写集数会被截掉
  const tvWithCount = t.media_type === 'tv' && t.videos > 0
  const parts: string[] = tvWithCount ? [] : [t.media_type === 'tv' ? '剧集' : '电影']
  if (t.year) parts.push(t.year)
  if (tvWithCount) parts.push(`${t.videos} 集`)
  else if (t.videos > 1) parts.push(`${t.videos} 个视频`)
  return parts.join(' · ')
}

// ---- 勾选 ----

const selectedList = computed(() => [...selected.value.values()])
const selecting = computed(() => selected.value.size > 0)

function toggle(t: LocalTitle) {
  const m = new Map(selected.value)
  if (m.has(t.key)) m.delete(t.key)
  else m.set(t.key, t)
  selected.value = m
}

/**
 * 全选框：只有一个，对象永远是「当前筛选下的全部片目」，与海报墙加载了多少无关。
 *
 * 原来并排两颗「勾选已显示（60）」「全选筛选结果（820）」：「已显示」是分页加载的实现细节，
 * 用户分不清两者差在哪、点了前者以为勾全了。现在海报墙没加载完时按当前筛选向后端要一次全量
 * （内存快照，零 115 请求）；matchedKeys 记住那一批，用来判断全选框是勾满 / 半选 / 空。
 * 换了筛选（load 重新开始）这份记录就作废。
 */
const matched = ref<{ keys: string[] } | null>(null)
const matchedKeys = computed<string[] | null>(() => {
  if (total.value <= items.value.length) return items.value.map((t) => t.key) // 已全部加载，眼前这批就是全部
  return matched.value?.keys ?? null
})
const allChecked = computed(() => {
  const keys = matchedKeys.value
  return !!keys && keys.length > 0 && keys.every((k) => selected.value.has(k))
})
/** 半选：勾了一部分（包括别的筛选下勾的），但当前筛选没勾满 */
const someChecked = computed(() => selecting.value && !allChecked.value)

const selectingAll = ref(false)
async function toggleAll() {
  if (allChecked.value) {
    // 只取消当前筛选下的；别的筛选下勾的保留（要全清用「清空」）
    const m = new Map(selected.value)
    for (const k of matchedKeys.value ?? []) m.delete(k)
    selected.value = m
    return
  }
  if (total.value <= items.value.length) {
    const m = new Map(selected.value)
    for (const t of items.value) m.set(t.key, t)
    selected.value = m
    return
  }
  const my = seq
  selectingAll.value = true
  try {
    const d = await localApi.listTitles({ ...filters(), all: true })
    if (my !== seq) return // 等的时候换了筛选：这份结果已经不是用户眼前那一批
    const list = d.items ?? []
    const m = new Map(selected.value)
    for (const t of list) m.set(t.key, t)
    selected.value = m
    matched.value = { keys: list.map((t) => t.key) }
  } catch (e) {
    toastError(e, '读取本地媒体库失败')
  } finally {
    selectingAll.value = false
  }
}
/** 已选里有多少不在当前筛选下（知道全集时才算得出） */
const selectedOutside = computed(() => {
  const keys = matchedKeys.value
  if (!keys || !selecting.value) return 0
  const inView = new Set(keys)
  let n = 0
  for (const k of selected.value.keys()) if (!inView.has(k)) n++
  return n
})
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
const dialogPreset = ref<'auto' | 'pick'>('auto')
const showScrape = ref(false)

function openScrape(list: LocalTitle[], preset: 'auto' | 'pick' = 'auto') {
  if (!list.length) return
  dialogTargets.value = list
  dialogPreset.value = preset
  showScrape.value = true
}

const CARD_MENU = [
  { key: 'detail', label: '查看详情', icon: Info },
  { key: 'auto', label: '刮削', icon: Images },
  { key: 'pick', label: '改指定 TMDB 条目', icon: Search },
]

function onCardAction(t: LocalTitle, key: string) {
  if (key === 'detail') openDetail(t)
  else openScrape([t], key as 'auto' | 'pick')
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
  void loadEmby()
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
        <HButton variant="ghost" size="sm" :loading="loading" @click="refreshAll"><RefreshCw :size="14" />刷新</HButton>
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
              <HButton
                v-if="emby?.ready"
                size="sm"
                :variant="probeLack ? 'primary' : 'tertiary'"
                :aria-pressed="probeLack"
                :loading="embyLoading"
                title="只看 Emby 里还有条目没探测过媒体信息（音视频轨道）的片目"
                @click="probeLack = !probeLack"
              >
                <ScanSearch :size="14" />缺媒体信息 {{ stats.probe_lack }}
              </HButton>
              <span v-else-if="embyLoading" class="emby-hint"><HSpinner size="sm" />读取 Emby 媒体信息…</span>
              <span v-else-if="embyError" class="emby-hint" :title="embyError">Emby 媒体信息读取失败</span>
            </div>
            <div class="view-sort">
              <HSegmented v-model="view" :options="VIEWS" size="sm" aria-label="显示方案" />
              <HSelect v-model="sort" :options="SORTS" aria-label="排序" class="sort" />
            </div>
          </div>

          <div class="batch" :class="{ active: selecting }">
            <HCheckbox
              class="sel-all"
              :checked="allChecked"
              :indeterminate="someChecked"
              :disabled="!total || selectingAll"
              @update:checked="toggleAll"
            >
              <span class="sel-count">
                <template v-if="!selecting">全选 {{ total }} 部</template>
                <template v-else-if="allChecked && !selectedOutside">已全选 <b>{{ selected.size }}</b> 部</template>
                <template v-else>
                  已选 <b>{{ selected.size }}</b> 部
                  <span v-if="selectedOutside" class="sel-tip">（{{ selectedOutside }} 部不在当前筛选里）</span>
                </template>
              </span>
            </HCheckbox>
            <span v-if="!selecting" class="sel-tip">勾选后可批量刮削</span>
            <div class="batch-btns">
              <HButton v-if="selecting" variant="ghost" size="sm" @click="clearSelection">清空</HButton>
              <HButton variant="primary" size="sm" :disabled="!selecting" @click="openScrape(selectedList)">
                <Images :size="14" />批量刮削<template v-if="selecting">（{{ selected.size }}）</template>
              </HButton>
            </div>
          </div>

          <HAlert v-if="emby?.ready && emby.scanned && !emby.titles" status="warning" class="tip">
            Emby 里读到 {{ emby.scanned }} 个影视条目，但一个都对不上本地片目，卡片上看不到媒体信息：检查 Emby 设置里的路径映射。
          </HAlert>
          <HAlert v-if="missing" status="warning" class="tip">
            有 {{ missing }} 个片目台账里有、本地却找不到目录（被手工删掉，或挂载还没就绪）。
          </HAlert>
        </div>

        <!-- 滚动区：海报墙 -->
        <div ref="scroller" class="scroller">
          <div v-if="loading && !loaded" class="grid" :class="`v-${view}`" aria-busy="true">
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
          <div v-else class="grid" :class="[`v-${view}`, { dim: loading, selecting }]">
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
                  <HChip :color="STATUS_TONE[t.status]" variant="primary" size="sm" class="badge" :title="statusTitle(t)">
                    {{ statusLabel(t) }}
                  </HChip>
                  <HChip v-if="probeLabel(t)" color="accent" variant="primary" size="sm" class="probe-badge" :title="probeTitle(t)">
                    {{ probeLabel(t) }}
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
                    <span class="meta-text">{{ metaLine(t) }}</span>
                    <span v-if="!t.tmdb_id" class="no-id" title="目录名里没有 TMDB 编号：刮削时按片名识别">
                      <Sparkles :size="11" />无编号
                    </span>
                    <span class="list-only cat">{{ t.category }}</span>
                  </p>
                </button>
                <!-- 列表里海报太小放不下角标，状态挪到这一行 -->
                <HChip v-if="probeLabel(t)" color="accent" size="sm" class="list-only row-status" :title="probeTitle(t)">
                  {{ probeLabel(t) }}
                </HChip>
                <HChip :color="STATUS_TONE[t.status]" size="sm" class="list-only row-status" :title="statusTitle(t)">
                  {{ statusLabel(t) }}
                </HChip>
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
.view-sort {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-left: auto;
}
.sort {
  width: 150px;
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
.sel-all {
  flex: none;
}
.sel-count {
  font-size: 13px;
  color: var(--foreground);
  font-variant-numeric: tabular-nums;
}
.sel-count b {
  color: var(--accent);
  font-size: 15px;
}
.sel-tip {
  font-size: 12.5px;
  color: var(--muted);
}
.batch-btns {
  display: flex;
  margin-left: auto;
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

/* 卡片最小宽度按显示方案变，列数由容器宽度自己算 */
.grid {
  --col: 132px;
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(var(--col), 1fr));
  gap: 14px;
  transition: opacity 0.15s;
}
.grid.v-lg {
  --col: 176px;
  gap: 18px;
}
.grid.v-sm {
  --col: 100px;
  gap: 10px;
}
.list-only {
  display: none;
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
  max-width: calc(100% - 16px);
  pointer-events: none;
}
.badge :deep(.chip__label),
.probe-badge :deep(.chip__label) {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
/* 媒体信息与刮削是两回事：角标分放两个角，一眼分得开 */
.probe-badge {
  position: absolute;
  left: 8px;
  bottom: 8px;
  max-width: calc(100% - 16px);
  pointer-events: none;
}
.emby-hint {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--muted);
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
/* 放不下时出省略号，而不是把最后一个字切掉半个 */
.meta-text {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.no-id {
  flex-shrink: 0;
  display: inline-flex;
  align-items: center;
  gap: 2px;
  color: var(--warning);
}
/* ---- 中卡片：字号收一点 ---- */
.v-md .name {
  font-size: 13px;
}
.v-md .meta {
  flex-wrap: nowrap;
  overflow: hidden;
  font-size: 11.5px;
  white-space: nowrap;
}

/* ---- 小卡片：只留片名，角标缩成色点（悬停看文字） ---- */
.v-sm .info {
  padding: 6px 2px 6px 8px;
}
.v-sm .name {
  font-size: 12px;
}
.v-sm .meta {
  display: none;
}
.v-sm .badge {
  top: 6px;
  right: 6px;
  width: 10px;
  height: 10px;
  min-width: 0;
  padding: 0;
  border-radius: 999px;
  box-shadow: 0 0 0 2px color-mix(in oklab, var(--background) 60%, transparent);
}
.v-sm .badge :deep(.chip__label) {
  display: none;
}
/* 小卡片放不下字：媒体信息也缩成左下角一个点 */
.v-sm .probe-badge {
  left: 6px;
  bottom: 6px;
  width: 10px;
  height: 10px;
  min-width: 0;
  padding: 0;
  border-radius: 999px;
  box-shadow: 0 0 0 2px color-mix(in oklab, var(--background) 60%, transparent);
}
.v-sm .probe-badge :deep(.chip__label) {
  display: none;
}
.v-sm .info :deep(.button) {
  width: 26px;
  min-width: 26px;
  height: 26px;
}

/* ---- 列表：一行一部，勾选圈在最左，小海报 + 片名 + 分类 + 状态 ---- */
.grid.v-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.v-list .title-card {
  flex-direction: row;
  align-items: center;
  gap: 4px;
  padding: 6px 8px 6px 2px;
  border-radius: var(--r-sm);
  background: transparent;
}
.v-list .title-card:hover {
  background: var(--surface-secondary);
}
.v-list .title-card.checked {
  background: color-mix(in oklab, var(--accent) 8%, transparent);
  box-shadow: inset 0 0 0 1px color-mix(in oklab, var(--accent) 40%, transparent);
}
.v-list .poster-wrap {
  display: flex;
  align-items: center;
  flex: none;
}
.v-list .check {
  position: static;
  order: -1;
}
.v-list .check-dot {
  opacity: 1;
}
.v-list .poster {
  width: 40px;
  border-radius: 6px;
}
.v-list .poster-none :deep(svg) {
  width: 16px;
  height: 16px;
}
.v-list .badge,
.v-list .probe-badge {
  display: none;
}
.v-list .info {
  flex: 1;
  align-items: center;
  gap: 10px;
  min-width: 0;
  padding: 0 0 0 8px;
}
.v-list .list-only {
  display: inline-flex;
}
.v-list .cat {
  color: var(--muted);
}
.v-list .cat::before {
  content: '·';
  margin-right: 6px;
}
.v-list .row-status {
  flex: none;
}
.v-list.grid .card-sk {
  flex-direction: row;
  align-items: center;
}
.v-list .sk-poster {
  width: 40px !important;
  flex: none;
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
    --col: 100px;
    gap: 10px;
  }
  .grid.v-lg {
    --col: 150px;
  }
  .v-md .info {
    padding: 6px 0 8px 8px;
  }
  .v-md .info :deep(.button) {
    width: 28px;
    min-width: 28px;
  }
  .v-md .no-id {
    display: none; /* 一行放不下，详情里有 */
  }
  .grid.v-sm {
    --col: 76px;
    gap: 8px;
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
  .view-sort {
    width: 100%;
    margin-left: 0;
  }
  .sort {
    flex: 1;
    width: auto;
  }
  .sel-tip,
  .batch-btns {
    display: none; /* 清空 / 批量刮削交给勾选后浮出的底部浮条 */
  }
  .batch {
    min-height: 44px;
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
