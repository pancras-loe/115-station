<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import {
  ChevronRight,
  CornerLeftUp,
  Ellipsis,
  File,
  FileVideo,
  Folder,
  FolderInput,
  House,
  Images,
  RefreshCw,
  Wand2,
} from '@lucide/vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HCheckbox from '@/components/hero/HCheckbox.vue'
import HChip from '@/components/hero/HChip.vue'
import HDropdown from '@/components/hero/HDropdown.vue'
import HSearchField from '@/components/hero/HSearchField.vue'
import HSkeleton from '@/components/hero/HSkeleton.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import ScrapeDialog from '@/components/files/ScrapeDialog.vue'
import OrganizeDialog from '@/components/files/OrganizeDialog.vue'
import MoveDialog from '@/components/files/MoveDialog.vue'
import { filesApi } from '@/api'
import type { Crumb, FileItem, FileJobBody, WorkspaceRole } from '@/api/files'
import { useQueueStore } from '@/stores/queue'

/**
 * 网盘文件：逐级浏览 115 网盘，对条目「刮削 / 整理 / 移动」（都进任务队列）。
 *
 * 每行最右侧是这一行能做的操作；勾选后只有批量刮削与批量移动（整理一次只做一项）。
 * 媒体库里只有「片目目录」（当前二级分类目录的下一层）能整理和移动，片目里的季目录 / 视频只能刮削，
 * 库根与分类目录什么都不能动。媒体库外只能整理和移动，不能刮削（还没整理的内容刮了也白刮）。哪一行是片目目录按后端给的分类目录列表算，执行时后端会再判一次。
 *
 * 115 只有 cid 没有父目录概念，面包屑就是一路点进来的栈；提交时连同面包屑一起交给后端，
 * 后端优先用 Cookie 通道查真实祖先链，查不到（OpenAPI 独立模式）才用它定位。
 * 面包屑存 sessionStorage：切到别的页再回来还在原目录（只是本标签页的便利，丢了就回根目录）。
 */
const router = useRouter()
const queue = useQueueStore()

const ROLE_TEXT: Record<WorkspaceRole, string> = {
  library: '媒体库',
  pending: '待整理',
  share: '转存',
  existing: '已存在',
  redundant: '冗余',
}
const ROLE_TONE: Record<WorkspaceRole, 'success' | 'accent' | 'warning' | 'default'> = {
  library: 'success',
  pending: 'accent',
  share: 'accent',
  existing: 'warning',
  redundant: 'default',
}
/** 批量移动一次最多几项（与后端 fileMoveMax 一致） */
const MOVE_MAX = 50

const TRAIL_KEY = 'files.trail'
function loadTrail(): Crumb[] {
  try {
    const v = JSON.parse(sessionStorage.getItem(TRAIL_KEY) || '[]')
    return Array.isArray(v) ? v.filter((c) => c && typeof c.cid === 'string' && typeof c.name === 'string') : []
  } catch {
    return []
  }
}
function saveTrail() {
  try {
    sessionStorage.setItem(TRAIL_KEY, JSON.stringify(trail.value))
  } catch {
    // 隐私模式等拿不到存储：只是回来时从根目录开始
  }
}

const trail = ref<Crumb[]>(loadTrail())
const cid = computed(() => trail.value[trail.value.length - 1]?.cid ?? '0')
const items = ref<FileItem[]>([])
const roots = ref<Record<string, WorkspaceRole>>({})
const rootPaths = ref<Record<string, string>>({})
const categories = ref<Set<string>>(new Set())
const truncated = ref(false)
const loading = ref(false)
const error = ref('')
const keyword = ref('')
const selected = ref(new Set<string>())

/** 渲染上限：几千个文件的目录一次铺满会卡，按需再展开 */
const PAGE = 300
const limit = ref(PAGE)
/** 列表自己的滚动容器：表头钉在顶上，只滚内容 */
const listEl = ref<HTMLElement | null>(null)

async function load(refresh = false) {
  loading.value = true
  error.value = ''
  try {
    const d = await filesApi.list(cid.value, refresh)
    items.value = d.data ?? []
    roots.value = d.roots ?? {}
    rootPaths.value = d.root_paths ?? {}
    categories.value = new Set(d.categories ?? [])
    truncated.value = !!d.truncated
  } catch (e) {
    items.value = []
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

function go(next: Crumb[]) {
  trail.value = next
  saveTrail()
  selected.value = new Set()
  keyword.value = ''
  limit.value = PAGE
  listEl.value?.scrollTo({ top: 0 })
  void load()
}

function enter(it: FileItem) {
  if (it.is_dir) go([...trail.value, { cid: it.id, name: it.name }])
}

function goUp() {
  if (trail.value.length) go(trail.value.slice(0, -1))
}

/** 当前目录落在哪个工作区里（离得最近的那个） */
const zone = computed<WorkspaceRole | ''>(() => {
  for (let i = trail.value.length - 1; i >= 0; i--) {
    const r = roots.value[trail.value[i]!.cid]
    if (r) return r
  }
  return ''
})
const configured = computed(() => Object.values(roots.value))

/**
 * 工作区根不在网盘根下时（如 /StrmStation/冗余），一路上的祖先目录要标出「里面有冗余」，
 * 否则从根目录点进来根本不知道往哪走。按路径比：面包屑名字 + 行名 是不是某个根路径的前缀
 */
const curPath = computed(() => trail.value.map((c) => '/' + c.name).join(''))
function containedRoles(it: FileItem): WorkspaceRole[] {
  if (!it.is_dir || it.root) return []
  const prefix = `${curPath.value}/${it.name}/`
  const out: WorkspaceRole[] = []
  for (const [rcid, p] of Object.entries(rootPaths.value)) {
    const role = roots.value[rcid]
    if (role && p.startsWith(prefix) && !out.includes(role)) out.push(role)
  }
  return out
}

// ---- 媒体库里的层级：分类目录的下一层是片目目录（与后端 libCategoryLayout.isTitleRel 同一口径） ----

/** 当前目录在媒体库里的相对路径（不含库名）；不在媒体库里为 null，库根为 '' */
const libRel = computed<string | null>(() => {
  const i = trail.value.findIndex((c) => roots.value[c.cid] === 'library')
  if (i < 0) return null
  return trail.value
    .slice(i + 1)
    .map((c) => c.name)
    .join('/')
})

function isCategoryOrAncestor(rel: string) {
  if (categories.value.has(rel)) return true
  for (const c of categories.value) if (c.startsWith(rel + '/')) return true
  return false
}
function isTitleRel(rel: string) {
  const i = rel.lastIndexOf('/')
  if (i <= 0) return false
  return categories.value.has(rel.slice(0, i)) && !isCategoryOrAncestor(rel)
}
/** 当前目录在某个片目目录里面（季目录那一层，或更深） */
const insideTitle = computed(() => {
  const rel = libRel.value
  if (!rel) return false
  const segs = rel.split('/')
  for (let n = 2; n <= segs.length; n++) if (isTitleRel(segs.slice(0, n).join('/'))) return true
  return false
})

const VIDEO_RE = /\.(mkv|mp4|avi|ts|m2ts|mov|wmv|flv|rmvb|iso|webm|mpg|mpeg|m4v|3gp|vob|strm)$/i
function isVideo(it: FileItem) {
  return !it.is_dir && VIDEO_RE.test(it.name)
}

interface RowActions {
  scrape: boolean
  organize: boolean
  move: boolean
  /** 媒体库里的片目目录：整理走重新整理，移动是移出媒体库 */
  title: boolean
}

function actionsOf(it: FileItem): RowActions {
  const none = { scrape: false, organize: false, move: false, title: false }
  if (it.root) return none // 工作区根目录本身
  const media = it.is_dir || isVideo(it)
  if (libRel.value !== null) {
    const title = it.is_dir && isTitleRel(libRel.value ? `${libRel.value}/${it.name}` : it.name)
    if (title) return { scrape: true, organize: true, move: true, title: true }
    return { ...none, scrape: insideTitle.value && media }
  }
  // 媒体库外不刮削：那里的内容还没整理，片名 / 目录结构都不规整，刮出来的元数据
  // 随整理搬走、改名就作废了；要刮先整理进媒体库
  return { scrape: false, organize: media, move: true, title: false }
}

const rowMenu = (a: RowActions) =>
  [
    a.scrape && { key: 'scrape', label: '刮削', icon: Images },
    a.organize && { key: 'organize', label: '整理', icon: Wand2 },
    a.move && { key: 'move', label: '移动', icon: FolderInput },
  ].filter((o) => !!o)

// ---- 列表与勾选 ----

const filtered = computed(() => {
  const q = keyword.value.trim().toLowerCase()
  return q ? items.value.filter((it) => it.name.toLowerCase().includes(q)) : items.value
})
const shown = computed(() => filtered.value.slice(0, limit.value))

const selectedItems = computed(() => items.value.filter((it) => selected.value.has(it.id)))
const allChecked = computed(() => filtered.value.length > 0 && filtered.value.every((it) => selected.value.has(it.id)))
const someChecked = computed(() => !allChecked.value && filtered.value.some((it) => selected.value.has(it.id)))

function toggle(it: FileItem, v: boolean) {
  const s = new Set(selected.value)
  if (v) s.add(it.id)
  else s.delete(it.id)
  selected.value = s
}

function toggleAll(v: boolean) {
  const s = new Set(selected.value)
  for (const it of filtered.value) {
    if (v) s.add(it.id)
    else s.delete(it.id)
  }
  selected.value = s
}

function bodyOf(list: FileItem[]): FileJobBody {
  return {
    cid: cid.value,
    chain: trail.value,
    items: list.map(({ id, name, is_dir, pickcode }) => ({ id, name, is_dir, pickcode })),
  }
}

/** 批量刮削为什么不能点（空 = 能点）；媒体库外整个按钮不显示 */
const batchScrapeBlock = computed(() => {
  const list = selectedItems.value
  if (!list.length) return '先勾选要刮削的条目'
  const bad = list.find((it) => !actionsOf(it).scrape)
  return bad ? `「${bad.name}」不能刮削：媒体库里只有片目目录与片目里的季目录、视频能刮削` : ''
})

/** 批量移动为什么不能点（空 = 能点） */
const batchMoveBlock = computed(() => {
  const list = selectedItems.value
  if (!list.length) return '先勾选要移动的条目'
  if (list.length > MOVE_MAX) return `一次最多移动 ${MOVE_MAX} 项`
  const bad = list.find((it) => !actionsOf(it).move)
  if (bad) {
    return libRel.value !== null
      ? `「${bad.name}」不是片目目录：媒体库里只有分类目录下的片目目录能移动`
      : `「${bad.name}」是整理工作区目录，不能移动`
  }
  return ''
})

// ---- 弹窗 ----

const dialogBody = ref<FileJobBody | null>(null)
const scrapeInLibrary = ref(false)
const organizeInLibrary = ref(false)
const showScrape = ref(false)
const showOrganize = ref(false)
const showMove = ref(false)

/** 刮削：所选是否都在媒体库里（本地有对应片目，可以只写本地） */
function inLibrary(list: FileItem[]) {
  return libRel.value !== null || (list.length > 0 && list.every((it) => it.root === 'library'))
}

function openScrape(list: FileItem[]) {
  dialogBody.value = bodyOf(list)
  scrapeInLibrary.value = inLibrary(list)
  showScrape.value = true
}
function openOrganize(it: FileItem) {
  dialogBody.value = bodyOf([it])
  organizeInLibrary.value = actionsOf(it).title
  showOrganize.value = true
}
function openMove(list: FileItem[]) {
  dialogBody.value = bodyOf(list)
  showMove.value = true
}

function onRowAction(it: FileItem, key: string) {
  if (key === 'scrape') openScrape([it])
  else if (key === 'organize') openOrganize(it)
  else if (key === 'move') openMove([it])
}

function humanSize(n?: number) {
  if (!n) return ''
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = n
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(i >= 3 ? 2 : i ? 1 : 0)} ${u[i]}`
}

// 本页发起的任务跑完：网盘内容变了，重新列一次（跳过缓存），并清掉已经处理掉的勾选
const PAGE_KINDS = new Set(['scrape', 'orgpick', 'libredo', 'filemove'])
const offFinished = queue.onFinished((j) => {
  if (PAGE_KINDS.has(j.kind)) {
    selected.value = new Set()
    void load(true)
  }
})
onBeforeUnmount(offFinished)

onMounted(() => load())
</script>

<template>
  <div class="page">
    <SectionCard title="网盘文件" hint="浏览 115 网盘，对文件 / 文件夹刮削、整理或移动">
      <template #extra>
        <HButton variant="ghost" size="sm" :loading="loading" @click="load(true)">
          <RefreshCw :size="14" />刷新
        </HButton>
      </template>

      <!-- 面包屑 -->
      <nav class="crumbs" aria-label="当前位置">
        <button type="button" class="crumb" :class="{ current: !trail.length }" @click="go([])">
          <House :size="14" />根目录
        </button>
        <template v-for="(c, i) in trail" :key="c.cid">
          <ChevronRight :size="13" class="crumb-sep" />
          <button
            type="button"
            class="crumb"
            :class="{ current: i === trail.length - 1 }"
            @click="go(trail.slice(0, i + 1))"
          >
            {{ c.name }}
          </button>
        </template>
        <HChip v-if="zone" :color="ROLE_TONE[zone]" class="zone">位于{{ ROLE_TEXT[zone] }}</HChip>
      </nav>

      <div class="toolbar">
        <HButton variant="tertiary" size="sm" :disabled="!trail.length" @click="goUp"><CornerLeftUp :size="14" />上一级</HButton>
        <HSearchField v-model="keyword" class="filter" placeholder="筛选当前目录" />
        <div class="actions">
          <span class="sel-count">已选 {{ selectedItems.length }} 项</span>
          <HButton variant="tertiary" size="sm" :disabled="!selectedItems.length" @click="selected = new Set()">清除</HButton>
          <HButton
            v-if="libRel !== null"
            variant="secondary"
            size="sm"
            :disabled="!!batchScrapeBlock"
            :title="selectedItems.length ? batchScrapeBlock || undefined : undefined"
            @click="openScrape(selectedItems)"
          >
            <Images :size="14" />批量刮削
          </HButton>
          <HButton
            variant="secondary"
            size="sm"
            :disabled="!!batchMoveBlock"
            :title="selectedItems.length ? batchMoveBlock || undefined : undefined"
            @click="openMove(selectedItems)"
          >
            <FolderInput :size="14" />批量移动
          </HButton>
        </div>
      </div>

      <HAlert v-if="libRel !== null" status="accent" class="tip">
        媒体库里只有分类目录下的片目目录能整理（按当前模板与分类规则重新规整）和移动（移出媒体库）；
        片目里的季目录与视频只能刮削。
        <template #actions>
          <HButton variant="tertiary" size="sm" @click="router.push({ name: 'tasks', query: { tab: 'records' } })">打开整理记录</HButton>
        </template>
      </HAlert>
      <HAlert v-if="error" status="danger" class="tip">{{ error }}</HAlert>
      <HAlert v-if="truncated" status="warning" class="tip">目录太大，只列出了前几千项；要找的条目不在里面时请到 115 里整理一下目录。</HAlert>

      <div ref="listEl" class="list" role="table" aria-label="目录内容">
        <div class="row head" role="row">
          <HCheckbox
            :checked="allChecked"
            :indeterminate="someChecked"
            :disabled="!filtered.length"
            aria-label="全选"
            @update:checked="toggleAll"
          />
          <span class="name-col">名称</span>
          <span class="size-col">大小</span>
          <span class="ops-col" aria-hidden="true" />
        </div>

        <template v-if="loading && !items.length">
          <div v-for="i in 6" :key="i" class="row">
            <HSkeleton width="18px" height="18px" radius="6px" />
            <HSkeleton width="45%" height="13px" radius="999px" />
          </div>
        </template>
        <EmptyState v-else-if="!filtered.length && !error" :text="keyword ? '没有匹配的条目' : '这个目录是空的'" />

        <div
          v-for="it in shown"
          :key="it.id"
          class="row"
          role="row"
          :class="{ checked: selected.has(it.id) }"
        >
          <HCheckbox :checked="selected.has(it.id)" :aria-label="`选择 ${it.name}`" @update:checked="(v) => toggle(it, v)" />
          <div class="name-col">
            <button v-if="it.is_dir" type="button" class="name dir" :title="it.name" @click="enter(it)">
              <Folder :size="16" class="ic ic-dir" />
              <span class="text">{{ it.name }}</span>
            </button>
            <span v-else class="name" :title="it.name">
              <FileVideo v-if="isVideo(it)" :size="16" class="ic ic-video" />
              <File v-else :size="16" class="ic" />
              <span class="text">{{ it.name }}</span>
            </span>
            <HChip v-if="it.root" :color="ROLE_TONE[it.root]" size="sm">{{ ROLE_TEXT[it.root] }}</HChip>
            <HChip v-else-if="actionsOf(it).title" color="success" size="sm">片目</HChip>
            <HChip
              v-else-if="containedRoles(it).length"
              size="sm"
              :title="`这个目录里有工作区：${containedRoles(it).map((r) => ROLE_TEXT[r]).join('、')}`"
            >
              含{{ containedRoles(it).map((r) => ROLE_TEXT[r]).join(' · ') }}
            </HChip>
          </div>
          <span class="size-col">{{ humanSize(it.size) }}</span>
          <div class="ops-col">
            <template v-if="rowMenu(actionsOf(it)).length">
              <div class="ops-inline">
                <HButton
                  v-for="o in rowMenu(actionsOf(it))"
                  :key="o.key"
                  variant="ghost"
                  size="sm"
                  class="op"
                  :class="`op-${o.key}`"
                  @click="onRowAction(it, o.key)"
                >
                  <component :is="o.icon" :size="14" />{{ o.label }}
                </HButton>
              </div>
              <!-- HDropdown 的根是 Reka 的无渲染组件，类名挂不上去，外面包一层 -->
              <div class="ops-menu">
                <HDropdown :options="rowMenu(actionsOf(it))" align="end" @select="(k) => onRowAction(it, k)">
                  <HButton variant="ghost" size="sm" icon-only :aria-label="`${it.name} 的操作`"><Ellipsis :size="16" /></HButton>
                </HDropdown>
              </div>
            </template>
          </div>
        </div>

        <div v-if="filtered.length > shown.length" class="more">
          <HButton variant="tertiary" size="sm" @click="limit += PAGE">
            再显示 {{ Math.min(PAGE, filtered.length - shown.length) }} 项（共 {{ filtered.length }} 项）
          </HButton>
        </div>
      </div>
    </SectionCard>

    <ScrapeDialog v-model:show="showScrape" :body="dialogBody" :in-library="scrapeInLibrary" />
    <OrganizeDialog v-model:show="showOrganize" :body="dialogBody" :library="organizeInLibrary" />
    <MoveDialog v-model:show="showMove" :body="dialogBody" :zone="zone" :configured="configured" />
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 16px;
  min-width: 0;
}
.crumbs {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 2px;
  min-width: 0;
}
.crumb {
  all: unset;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  max-width: 16em;
  padding: 3px 8px;
  border-radius: var(--r-sm);
  font-size: 13px;
  color: var(--muted);
  cursor: pointer;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
@media (hover: hover) {
  .crumb:hover {
    background: var(--surface-secondary);
    color: var(--foreground);
  }
}
.crumb:focus-visible {
  box-shadow: 0 0 0 2px var(--focus);
}
.crumb.current {
  color: var(--foreground);
  font-weight: 600;
}
.crumb-sep {
  color: var(--muted);
  flex: none;
}
.zone {
  margin-left: 8px;
}

.toolbar {
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
.actions {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  margin-left: auto;
}
.sel-count {
  font-size: 12.5px;
  color: var(--muted);
}
.tip {
  margin: 0;
}

/* 列表单独滚动、表头吸顶：几百项的目录不用滚整页才能回到批量按钮。
   高度扣掉顶栏、面包屑与工具栏（与日志页同一做法），矮屏上至少留 320px */
.list {
  display: flex;
  flex-direction: column;
  min-width: 0;
  max-height: calc(100dvh - 300px);
  min-height: 320px;
  overflow-y: auto;
  overscroll-behavior: contain;
}
.row.head {
  position: sticky;
  top: 0;
  z-index: 1;
  flex: none;
  background: var(--surface);
  box-shadow: 0 1px 0 var(--separator);
}
.row {
  display: flex;
  align-items: center;
  gap: 12px;
  min-height: 40px;
  padding: 4px 10px;
  border-radius: var(--r-sm);
}
.row.head {
  min-height: 32px;
  font-size: 12px;
  color: var(--muted);
}
.row:not(.head):nth-child(even) {
  background: color-mix(in oklab, var(--surface-secondary) 45%, transparent);
}
.row.checked {
  background: var(--accent-soft) !important;
}
.name-col {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 8px;
}
.size-col {
  flex: none;
  width: 90px;
  text-align: right;
  font-size: 12.5px;
  color: var(--muted);
  font-variant-numeric: tabular-nums;
}
/* 操作列定宽：三个按钮的位置在每一行都对齐，没有操作的行留空 */
.ops-col {
  flex: none;
  width: 222px;
  display: flex;
  justify-content: flex-end;
}
.ops-inline {
  display: flex;
  gap: 4px;
}
/* 三个动作各一个颜色：刮削（只写元数据）用强调色、整理（会搬移改名）用绿、
   移动（挪出当前位置）用黄，扫一眼就能分清，不用读字 */
.op {
  gap: 4px;
  --op-c: var(--accent);
  color: var(--op-c);
  background: color-mix(in oklab, var(--op-c) 10%, transparent);
}
.op-organize {
  --op-c: var(--success);
}
.op-move {
  --op-c: var(--warning);
}
@media (hover: hover) {
  .op:hover {
    color: var(--op-c);
    background: color-mix(in oklab, var(--op-c) 18%, transparent);
  }
}
.ops-menu {
  display: none;
}
.name {
  all: unset;
  display: inline-flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  font-size: 13.5px;
  color: var(--foreground);
}
.name.dir {
  cursor: pointer;
}
@media (hover: hover) {
  .name.dir:hover .text {
    color: var(--accent);
    text-decoration: underline;
    text-underline-offset: 3px;
  }
}
.name.dir:focus-visible {
  border-radius: 4px;
  box-shadow: 0 0 0 2px var(--focus);
}
.text {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ic {
  flex: none;
  color: var(--muted);
}
.ic-dir {
  color: var(--warning);
}
.ic-video {
  color: var(--accent);
}
.more {
  display: flex;
  justify-content: center;
  padding: 10px 0 2px;
}

/* 手机：行内三个按钮会把文件名挤没，收成一个「⋯」菜单；批量按钮整行 */
@media (max-width: 720px) {
  .filter {
    max-width: none;
    flex-basis: 100%;
    order: 3;
  }
  .actions {
    width: 100%;
    margin-left: 0;
  }
  .size-col {
    width: 64px;
    font-size: 11.5px;
  }
  .ops-col {
    width: 36px;
  }
  .list {
    max-height: calc(100dvh - 360px - var(--tabbar-h));
  }
  .ops-inline {
    display: none;
  }
  .ops-menu {
    display: inline-flex;
  }
}
</style>
