<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import {
  ChevronRight,
  CornerLeftUp,
  Ellipsis,
  File,
  FileVideo,
  Folder,
  FolderInput,
  FolderOutput,
  History,
  House,
  Info,
  LibraryBig,
  RefreshCw,
  Wand2,
} from '@lucide/vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HCheckbox from '@/components/hero/HCheckbox.vue'
import HChip from '@/components/hero/HChip.vue'
import HDropdown from '@/components/hero/HDropdown.vue'
import type { MenuOption } from '@/components/hero/HDropdown.vue'
import HSearchField from '@/components/hero/HSearchField.vue'
import HSkeleton from '@/components/hero/HSkeleton.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import OrganizeDialog from '@/components/files/OrganizeDialog.vue'
import MoveDialog from '@/components/files/MoveDialog.vue'
import { filesApi } from '@/api'
import type { Crumb, FileItem, FileJobBody, WorkspaceRole } from '@/api/files'
import { useQueueStore } from '@/stores/queue'

/**
 * 网盘文件：逐级浏览 115 网盘，对条目「整理 / 移动」（都进任务队列）。
 *
 * 每一行能做什么由后端列目录时算好（organize / move / title / block），用的是入队时同一套校验；
 * 前端不再自己抄视频后缀与片目判定 —— 以前两边对不上，按钮点下去就是 400。
 * 做不了任何事的行不能勾选（悬停说明原因），全选只选能处理的。
 * 媒体库里只有片目目录能动：整理 = 重新整理（一次一部），移动 = 移出媒体库；另有跳到本地详情 / 整理记录。
 * 刮削在本地文件页（LocalFilesPage）。
 *
 * 115 只有 cid 没有父目录概念，面包屑就是一路点进来的栈；列目录与提交都带上它，
 * 后端优先用 Cookie 通道查真实祖先链，查不到（OpenAPI 独立模式）才用它定位。
 * 面包屑存 sessionStorage：切到别的页再回来还在原目录（只是本标签页的便利，丢了就回根目录）。
 */
const queue = useQueueStore()
const router = useRouter()

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
/** 批量整理一次最多几项（与后端 fileJobMaxItems 一致） */
const ORGANIZE_MAX = 200

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
    const d = await filesApi.list(cid.value, trail.value, refresh)
    items.value = d.data ?? []
    roots.value = d.roots ?? {}
    rootPaths.value = d.root_paths ?? {}
    truncated.value = !!d.truncated
    // 刷新后变得不能处理的（被挪走、配置改了）不留在勾选里
    const ok = new Set(items.value.filter(actionable).map((it) => it.id))
    selected.value = new Set([...selected.value].filter((id) => ok.has(id)))
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
/** 在媒体库里（不管多深）：这里能做的事很少，顶上一行说明 */
const inLibrary = computed(() => trail.value.some((c) => roots.value[c.cid] === 'library'))

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

// ---- 每一行的操作（全看后端给的 organize / move / title） ----

function actionable(it: FileItem) {
  return !!(it.organize || it.move)
}

/** 行内悬停时露出的那一个快捷按钮：能整理就是整理，否则没有（移动在「⋯」里） */
function quickOf(it: FileItem) {
  if (!it.organize) return null
  return it.title ? { key: 'organize', label: '重新整理', icon: Wand2 } : { key: 'organize', label: '整理', icon: Wand2 }
}

function menuOf(it: FileItem): MenuOption[] {
  if (it.title) {
    return [
      { key: 'organize', label: '重新整理', icon: Wand2 },
      { key: 'local', label: '本地详情', icon: LibraryBig },
      { key: 'records', label: '整理记录', icon: History },
      { key: 'move', label: '移出媒体库…', icon: FolderOutput, danger: true },
    ]
  }
  const out: MenuOption[] = []
  if (it.organize) out.push({ key: 'organize', label: '整理', icon: Wand2 })
  if (it.move) out.push({ key: 'move', label: '移动到…', icon: FolderInput })
  return out
}

/** 行上的「排队整理 / 移动中」：任务状态跟着队列实时走，任务结束就不显示（列表随后会刷新） */
function busyText(it: FileItem) {
  if (!it.busy) return ''
  const j = it.busy_job ? queue.jobs.find((x) => x.id === it.busy_job) : undefined
  if (j && j.status !== 'queued' && j.status !== 'running') return ''
  const what = it.busy === 'move' ? '移动' : '整理'
  return j?.status === 'running' ? `${what}中` : `排队${what}`
}

// ---- 列表与勾选 ----

const filtered = computed(() => {
  const q = keyword.value.trim().toLowerCase()
  return q ? items.value.filter((it) => it.name.toLowerCase().includes(q)) : items.value
})
const shown = computed(() => filtered.value.slice(0, limit.value))
/** 全选只管能处理的：库根、分类、片目里的文件勾上也什么都做不了 */
const selectable = computed(() => filtered.value.filter(actionable))

const selectedItems = computed(() => items.value.filter((it) => selected.value.has(it.id)))
const allChecked = computed(
  () => selectable.value.length > 0 && selectable.value.every((it) => selected.value.has(it.id)),
)
const someChecked = computed(() => !allChecked.value && selectable.value.some((it) => selected.value.has(it.id)))

function toggle(it: FileItem, v: boolean) {
  if (!actionable(it)) return
  const s = new Set(selected.value)
  if (v) s.add(it.id)
  else s.delete(it.id)
  selected.value = s
}

function toggleAll(v: boolean) {
  const s = new Set(selected.value)
  for (const it of selectable.value) {
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

// ---- 批量栏：按钮上写实际处理几项，处理不了的跳过并说明，不再整栏变灰 ----

const toOrganize = computed(() => selectedItems.value.filter((it) => it.organize))
const toMove = computed(() => selectedItems.value.filter((it) => it.move))

/** 批量整理按钮：媒体库里的重新整理一次只能一部，勾了多部就不给这个按钮 */
const batchOrganize = computed(() => {
  const n = toOrganize.value.length
  if (!n) return null
  if (inLibrary.value) return n === 1 && selectedItems.value.length === 1 ? { label: '重新整理', block: '' } : null
  return {
    label: n === selectedItems.value.length ? '整理' : `整理 ${n} 项`,
    block: n > ORGANIZE_MAX ? `一次最多整理 ${ORGANIZE_MAX} 项` : '',
  }
})
const batchMove = computed(() => {
  const n = toMove.value.length
  if (!n) return null
  const verb = inLibrary.value ? '移出媒体库' : '移动'
  return {
    label: n === selectedItems.value.length ? verb : `${verb} ${n} 项`,
    block: n > MOVE_MAX ? `一次最多移动 ${MOVE_MAX} 项` : '',
  }
})

/** 批量栏的说明：会跳过哪些、为什么（手机上也显示，换到第二行） */
const batchNote = computed(() => {
  const total = selectedItems.value.length
  const notes: string[] = []
  const block = batchOrganize.value?.block || batchMove.value?.block
  if (block) notes.push(block)
  if (inLibrary.value && toOrganize.value.length > 1) notes.push('重新整理一次只能一部')
  if (!inLibrary.value && batchOrganize.value && toOrganize.value.length < total) {
    notes.push(`整理跳过 ${total - toOrganize.value.length} 个非视频文件`)
  }
  if (batchMove.value && toMove.value.length < total) {
    notes.push(`移动跳过 ${total - toMove.value.length} 项（没有可移动到的目录）`)
  }
  return notes.join('；')
})

// ---- 弹窗 ----

const dialogBody = ref<FileJobBody | null>(null)
const organizeInLibrary = ref(false)
const showOrganize = ref(false)
const showMove = ref(false)

function openOrganize(list: FileItem[]) {
  dialogBody.value = bodyOf(list)
  organizeInLibrary.value = list.some((it) => it.title)
  showOrganize.value = true
}
function openMove(list: FileItem[]) {
  dialogBody.value = bodyOf(list)
  showMove.value = true
}

/**
 * 弹窗是不是鼠标点开的。是的话关闭时不把焦点还给那颗按钮（见 HModal 的 returnFocus）：
 * 行内按钮只在悬停 / 键盘聚焦时出现，焦点被还回去后那一行的按钮会一直亮着。
 * 键盘（回车 / 空格触发的 click，detail 为 0）和「⋯」菜单打开的照常还焦点
 */
const pointerOpened = ref(false)
function markOpener(e?: MouseEvent) {
  pointerOpened.value = !!e && e.detail > 0
}

function onRowAction(it: FileItem, key: string, e?: MouseEvent) {
  markOpener(e)
  if (key === 'organize') openOrganize([it])
  else if (key === 'move') openMove([it])
  else if (key === 'local' && it.title_key) void router.push({ name: 'local', query: { title: it.title_key } })
  else if (key === 'records' && it.title_rel) {
    void router.push({ name: 'tasks', query: { tab: 'records', target_dir: it.title_rel } })
  }
}

/** 底部一行：这个目录有几个文件夹、几个文件 */
const counts = computed(() => {
  const dirs = filtered.value.filter((it) => it.is_dir).length
  return { dirs, files: filtered.value.length - dirs }
})

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

// 本页发起的任务：新进队列时重列一次（走缓存，只为给行挂上「排队整理」），
// 跑完时网盘内容变了，跳过缓存重列，并清掉已经处理掉的勾选
const PAGE_KINDS = new Set(['orgpick', 'libredo', 'filemove'])
const activePageJobs = computed(() =>
  queue.active.filter((j) => PAGE_KINDS.has(j.kind)).map((j) => j.id),
)
watch(activePageJobs, (now, before) => {
  if (now.some((id) => !before.includes(id))) void load()
})
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
  <!-- 工具栏一行（上一级 / 面包屑 / 筛选 / 刷新）；列表撑满视口高度；
       每行常驻「⋯」，悬停再露出主操作；做不了事的行不能勾；批量栏只在有勾选时出现 -->
  <div class="page">
    <section class="card card--default browser">
      <div class="toolbar">
        <HButton
          variant="tertiary"
          size="sm"
          icon-only
          :disabled="!trail.length"
          aria-label="上一级"
          title="上一级"
          @click="goUp"
        >
          <CornerLeftUp :size="15" />
        </HButton>

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
          <HChip v-if="zone" :color="ROLE_TONE[zone]" size="sm" class="zone">位于{{ ROLE_TEXT[zone] }}</HChip>
        </nav>

        <HSearchField v-model="keyword" class="filter" placeholder="筛选当前目录" />
        <HButton variant="ghost" size="sm" icon-only :loading="loading" aria-label="刷新" title="刷新（跳过缓存）" @click="load(true)">
          <RefreshCw :size="15" />
        </HButton>
      </div>

      <!-- 媒体库里能做的事很少，一行说清，不再是一整块提示框 -->
      <p v-if="inLibrary" class="lib-note">
        <Info :size="14" />
        <span>
          媒体库里只有分类目录下的<b>片目目录</b>能操作：重新整理（按当前模板与分类规则重新规整）、移出媒体库，
          或跳到它的本地详情与整理记录。刮削到「<RouterLink :to="{ name: 'local' }">本地文件</RouterLink>」。
        </span>
      </p>
      <HAlert v-if="error" status="danger" class="tip">{{ error }}</HAlert>
      <HAlert v-if="truncated" status="warning" class="tip">目录太大，只列出了前几千项；要找的条目不在里面时请到 115 里整理一下目录。</HAlert>

      <div ref="listEl" class="list" role="table" aria-label="目录内容">
        <div class="row head" role="row">
          <HCheckbox
            :checked="allChecked"
            :indeterminate="someChecked"
            :disabled="!selectable.length"
            :aria-label="`全选可处理的 ${selectable.length} 项`"
            :title="selectable.length ? `全选可处理的 ${selectable.length} 项` : '这个目录里没有能处理的条目'"
            @update:checked="toggleAll"
          />
          <span class="name-col">名称</span>
          <span class="size-col">大小</span>
          <span class="ops-col" aria-hidden="true" />
        </div>

        <template v-if="loading && !items.length">
          <div v-for="i in 8" :key="i" class="row">
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
          :class="{ checked: selected.has(it.id), inert: !actionable(it) }"
        >
          <!-- 做不了事的行不给勾：悬停在勾选框上说明为什么 -->
          <span class="check" :title="it.block || undefined">
            <HCheckbox
              :checked="selected.has(it.id)"
              :disabled="!actionable(it)"
              :aria-label="`选择 ${it.name}`"
              @update:checked="(v) => toggle(it, v)"
            />
          </span>
          <div class="name-col">
            <button v-if="it.is_dir" type="button" class="name dir" :title="it.name" @click="enter(it)">
              <Folder :size="17" class="ic ic-dir" :class="it.root && `role-${it.root}`" />
              <span class="text">{{ it.name }}</span>
            </button>
            <span v-else class="name" :title="it.name">
              <FileVideo v-if="it.video" :size="17" class="ic ic-video" />
              <File v-else :size="17" class="ic" />
              <span class="text">{{ it.name }}</span>
            </span>
            <HChip v-if="it.root" :color="ROLE_TONE[it.root]" size="sm">{{ ROLE_TEXT[it.root] }}</HChip>
            <HChip v-else-if="it.title" color="success" size="sm">片目</HChip>
            <HChip
              v-else-if="containedRoles(it).length"
              size="sm"
              class="contain-chip"
              :title="`这个目录里有工作区：${containedRoles(it).map((r) => ROLE_TEXT[r]).join('、')}`"
            >
              含{{ containedRoles(it).map((r) => ROLE_TEXT[r]).join(' · ') }}
            </HChip>
            <HChip v-if="busyText(it)" color="accent" size="sm" class="busy-chip">{{ busyText(it) }}</HChip>
          </div>
          <span class="size-col">{{ humanSize(it.size) }}</span>
          <div class="ops-col">
            <HButton
              v-if="quickOf(it)"
              variant="ghost"
              size="sm"
              class="op"
              @click="(e: MouseEvent) => onRowAction(it, quickOf(it)!.key, e)"
            >
              <component :is="quickOf(it)!.icon" :size="14" />{{ quickOf(it)!.label }}
            </HButton>
            <!-- HDropdown 的根是 Reka 的无渲染组件，类名挂不上去，外面包一层 -->
            <div v-if="menuOf(it).length" class="ops-menu">
              <HDropdown :options="menuOf(it)" align="end" @select="(k: string) => onRowAction(it, k)">
                <HButton variant="ghost" size="sm" icon-only :aria-label="`${it.name} 的操作`"><Ellipsis :size="16" /></HButton>
              </HDropdown>
            </div>
          </div>
        </div>

        <div v-if="filtered.length > shown.length" class="more">
          <HButton variant="tertiary" size="sm" @click="limit += PAGE">
            再显示 {{ Math.min(PAGE, filtered.length - shown.length) }} 项（共 {{ filtered.length }} 项）
          </HButton>
        </div>
      </div>

      <!-- 底栏：平时是目录统计；有勾选时换成批量栏 -->
      <footer class="foot" :class="{ selecting: selectedItems.length > 0 }">
        <template v-if="selectedItems.length">
          <span class="foot-sel">已选 <b>{{ selectedItems.length }}</b> 项</span>
          <span v-if="batchNote" class="foot-block" :title="batchNote">{{ batchNote }}</span>
          <div class="foot-btns">
            <HButton variant="ghost" size="sm" @click="selected = new Set()">清除</HButton>
            <HButton
              v-if="batchMove"
              :variant="batchOrganize ? 'secondary' : 'primary'"
              size="sm"
              :disabled="!!batchMove.block"
              @click="(e: MouseEvent) => (markOpener(e), openMove(toMove))"
            >
              <component :is="inLibrary ? FolderOutput : FolderInput" :size="14" />{{ batchMove.label }}
            </HButton>
            <HButton
              v-if="batchOrganize"
              variant="primary"
              size="sm"
              :disabled="!!batchOrganize.block"
              @click="(e: MouseEvent) => (markOpener(e), openOrganize(toOrganize))"
            >
              <Wand2 :size="14" />{{ batchOrganize.label }}
            </HButton>
          </div>
        </template>
        <span v-else class="foot-count">
          {{ counts.dirs }} 个文件夹 · {{ counts.files }} 个文件
          <template v-if="keyword"> · 已按「{{ keyword }}」筛选</template>
        </span>
      </footer>
    </section>

    <!-- return-focus 透传给弹窗根上的 HModal -->
    <OrganizeDialog v-model:show="showOrganize" :body="dialogBody" :library="organizeInLibrary" :return-focus="!pointerOpened" />
    <MoveDialog v-model:show="showMove" :body="dialogBody" :zone="zone" :configured="configured" :return-focus="!pointerOpened" />
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  min-width: 0;
}

/* 整块撑满视口：扣掉顶栏（68）与内容区上下内边距（20 + 36） */
.browser {
  gap: 0;
  padding: 0;
  height: calc(100dvh - 68px - 56px);
  min-height: 420px;
  overflow: hidden;
}

/* ---- 工具栏 ---- */
.toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 12px 14px;
  border-bottom: 1px solid var(--separator);
}
.crumbs {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 2px;
  overflow-x: auto;
  scrollbar-width: none;
}
.crumbs::-webkit-scrollbar {
  display: none;
}
.crumb {
  all: unset;
  display: inline-flex;
  align-items: center;
  gap: 5px;
  flex: none;
  max-width: 16em;
  padding: 4px 8px;
  border-radius: var(--r-sm);
  font-size: 13.5px;
  color: var(--muted);
  cursor: pointer;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
@media (hover: hover) {
  .crumb:hover {
    background: var(--default);
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
  flex: none;
  color: var(--muted);
}
.zone {
  flex: none;
  margin-left: 6px;
}
.filter {
  flex: 0 1 240px;
  min-width: 140px;
}

.lib-note {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: 0;
  padding: 8px 16px;
  border-bottom: 1px solid var(--separator);
  font-size: 12.5px;
  line-height: 1.6;
  color: var(--muted);
}
.lib-note :deep(svg) {
  flex-shrink: 0;
  margin-top: 3px;
}
.lib-note b {
  color: var(--foreground);
  font-weight: 600;
}
.lib-note a {
  color: var(--accent);
  text-decoration: none;
}
.lib-note a:hover {
  text-decoration: underline;
}
.tip {
  margin: 10px 14px 0;
}

/* ---- 列表：占满剩余高度，表头吸顶 ---- */
.list {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  padding: 0 8px 8px;
  overflow-y: auto;
  overscroll-behavior: contain;
}
.row.head {
  position: sticky;
  top: 0;
  z-index: 1;
  flex: none;
  min-height: 36px;
  margin: 0 -8px 4px;
  padding: 4px 18px;
  border-radius: 0;
  background: var(--surface);
  box-shadow: 0 1px 0 var(--separator);
  font-size: 12px;
  color: var(--muted);
}
.row {
  display: flex;
  align-items: center;
  gap: 12px;
  min-height: 42px;
  padding: 3px 10px;
  border-radius: 10px;
  transition: background-color 120ms ease;
}
@media (hover: hover) {
  .row:not(.head):not(.inert):hover {
    background: var(--surface-secondary);
  }
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
.name-col > .chip {
  flex: none;
  white-space: nowrap;
}
/* 「含已存在 · 冗余 · 待整理」这种长标签在手机上比目录名还宽：让它先缩、省略号截断
   （完整内容在悬浮提示里），别把目录名挤没，也别折成两行压到名字上 */
.name-col > .contain-chip {
  flex: 0 1000 auto;
  min-width: 3em;
  overflow: hidden;
}
.contain-chip :deep(.chip__label) {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.size-col {
  flex: none;
  width: 90px;
  text-align: right;
  font-size: 12.5px;
  color: var(--muted);
  font-variant-numeric: tabular-nums;
}
/* 操作列定宽：「⋯」每一行都对齐在最右，没有操作的行留空 */
.ops-col {
  flex: none;
  width: 150px;
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 2px;
}
/* 桌面上主操作（整理 / 重新整理）平时藏着，悬停 / 键盘聚焦 / 勾选中才出现；「⋯」常驻，
   一眼看得出哪几行能操作。原来两颗彩色按钮全藏着，媒体库里得一行行悬停去试。
   只认 :focus-visible，不能用 :focus-within —— 弹窗关闭时会把焦点还给打开它的那颗按钮，
   鼠标点的也一样，于是那一行的按钮一直亮着、要点别处才消失。
   鼠标操作后还回来的焦点浏览器不算 focus-visible，Tab 过来的才算 */
@media (hover: hover) {
  .row:not(:hover):not(:has(:focus-visible)):not(.checked) .op {
    opacity: 0;
  }
}
.op {
  gap: 4px;
  color: var(--success);
  background: color-mix(in oklab, var(--success) 10%, transparent);
  transition: opacity 120ms ease;
}
@media (hover: hover) {
  .op:hover {
    color: var(--success);
    background: color-mix(in oklab, var(--success) 18%, transparent);
  }
}
.ops-menu {
  display: inline-flex;
}
.check {
  display: inline-flex;
  flex: none;
}
/* 做不了事的行：名字照常能点进去，只是勾选框灰掉、悬停不高亮 */
.row.inert:not(.head) .check {
  cursor: help;
}
.name-col > .busy-chip {
  flex: none;
}
.name {
  all: unset;
  display: inline-flex;
  align-items: center;
  gap: 10px;
  flex: 0 1 auto;
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
/* 工作区根目录的图标跟它的标签同色，一眼认出「这是整理用的目录」 */
.ic-dir.role-library {
  color: var(--success);
}
.ic-dir.role-pending,
.ic-dir.role-share {
  color: var(--accent);
}
.ic-dir.role-redundant {
  color: var(--muted);
}
.ic-video {
  color: var(--accent);
}
.more {
  display: flex;
  justify-content: center;
  padding: 10px 0 2px;
}

/* ---- 底栏 ---- */
.foot {
  flex: none;
  display: flex;
  align-items: center;
  gap: 10px;
  min-height: 48px;
  padding: 6px 10px 6px 18px;
  border-top: 1px solid var(--separator);
  font-size: 12.5px;
  color: var(--muted);
  transition: background-color 150ms ease;
}
.foot.selecting {
  background: color-mix(in oklab, var(--accent) 8%, transparent);
}
.foot-sel {
  color: var(--foreground);
}
.foot-sel b {
  color: var(--accent);
  font-size: 14px;
}
.foot-block {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--warning-soft-foreground);
}
.foot-btns {
  display: flex;
  gap: 6px;
  margin-left: auto;
  flex-shrink: 0;
}
.foot-count {
  font-variant-numeric: tabular-nums;
}

@media (max-width: 960px) {
  .browser {
    height: calc(100dvh - 60px - 36px);
  }
}
/* 手机：行内按钮会把文件名挤没，收成一个「⋯」菜单；筛选框换到第二行 */
@media (max-width: 720px) {
  .browser {
    height: calc(100dvh - 52px - env(safe-area-inset-top) - 28px - var(--tabbar-h) - env(safe-area-inset-bottom));
    min-height: 360px;
  }
  .toolbar {
    flex-wrap: wrap;
  }
  .filter {
    flex-basis: 100%;
    order: 3;
  }
  .size-col {
    width: 64px;
    font-size: 11.5px;
  }
  .ops-col {
    width: 36px;
  }
  .op {
    display: none;
  }
  /* 跳过说明不藏：换到第二行，否则手机上看不出按钮上的「整理 3 项」少了谁 */
  .foot {
    flex-wrap: wrap;
    row-gap: 2px;
  }
  .foot-block {
    order: 3;
    flex-basis: 100%;
    white-space: normal;
  }
}
</style>
