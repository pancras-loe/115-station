<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ChevronRight, CornerLeftUp, File, FileVideo, Folder, House, Images, RefreshCw, Wand2 } from '@lucide/vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HCheckbox from '@/components/hero/HCheckbox.vue'
import HChip from '@/components/hero/HChip.vue'
import HSearchField from '@/components/hero/HSearchField.vue'
import HSkeleton from '@/components/hero/HSkeleton.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import ScrapeDialog from '@/components/files/ScrapeDialog.vue'
import OrganizeDialog from '@/components/files/OrganizeDialog.vue'
import { filesApi } from '@/api'
import type { Crumb, FileItem, FileJobBody, WorkspaceRole } from '@/api/files'
import { useQueueStore } from '@/stores/queue'

/**
 * 网盘文件：逐级浏览 115 网盘，勾选文件 / 文件夹后「刮削」或「整理」（都进任务队列）。
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
const truncated = ref(false)
const loading = ref(false)
const error = ref('')
const keyword = ref('')
const selected = ref(new Set<string>())

/** 渲染上限：几千个文件的目录一次铺满会卡，按需再展开 */
const PAGE = 300
const limit = ref(PAGE)

async function load(refresh = false) {
  loading.value = true
  error.value = ''
  try {
    const d = await filesApi.list(cid.value, refresh)
    items.value = d.data ?? []
    roots.value = d.roots ?? {}
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

const jobBody = computed<FileJobBody | null>(() =>
  selectedItems.value.length
    ? {
        cid: cid.value,
        chain: trail.value,
        items: selectedItems.value.map(({ id, name, is_dir, pickcode }) => ({ id, name, is_dir, pickcode })),
      }
    : null,
)

/** 所选是否都在媒体库里（本地有对应片目，可以只写本地） */
const selectionInLibrary = computed(
  () => zone.value === 'library' || (selectedItems.value.length > 0 && selectedItems.value.every((it) => it.root === 'library')),
)

/** 整理按钮为什么不能点（空 = 能点） */
const organizeBlock = computed(() => {
  if (zone.value === 'library') return '媒体库里的内容已经整理过，要改识别或位置请到整理记录里「重新整理」'
  const root = selectedItems.value.find((it) => it.root)
  if (root) return `「${root.name}」是整理工作区目录，不能当作影视条目整理`
  return ''
})

const showScrape = ref(false)
const showOrganize = ref(false)

const VIDEO_RE = /\.(mkv|mp4|avi|ts|m2ts|mov|wmv|flv|rmvb|iso|webm|mpg|mpeg|m4v|3gp|vob|strm)$/i
function isVideo(it: FileItem) {
  return !it.is_dir && VIDEO_RE.test(it.name)
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

// 本页发起的刮削 / 整理跑完：网盘内容变了，重新列一次（跳过缓存），并清掉已经处理掉的勾选
const offFinished = queue.onFinished((j) => {
  if (j.kind === 'scrape' || j.kind === 'orgpick') {
    selected.value = new Set()
    void load(true)
  }
})
onBeforeUnmount(offFinished)

onMounted(() => load())
</script>

<template>
  <div class="page">
    <SectionCard title="网盘文件" hint="浏览 115 网盘，对勾选的文件 / 文件夹手动刮削或整理">
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
          <HButton variant="secondary" size="sm" :disabled="!selectedItems.length" @click="showScrape = true">
            <Images :size="14" />刮削
          </HButton>
          <HButton
            variant="primary"
            size="sm"
            :disabled="!selectedItems.length || !!organizeBlock"
            :title="organizeBlock || undefined"
            @click="showOrganize = true"
          >
            <Wand2 :size="14" />整理
          </HButton>
        </div>
      </div>

      <HAlert v-if="zone === 'library'" status="accent" class="tip">
        媒体库里的条目可以刮削（写本地媒体库，可选同时上传网盘）；要改识别结果或位置，请到整理记录里「重新整理」。
        <template #actions>
          <HButton variant="tertiary" size="sm" @click="router.push({ name: 'tasks', query: { tab: 'records' } })">打开整理记录</HButton>
        </template>
      </HAlert>
      <HAlert v-if="error" status="danger" class="tip">{{ error }}</HAlert>
      <HAlert v-if="truncated" status="warning" class="tip">目录太大，只列出了前几千项；要找的条目不在里面时请到 115 里整理一下目录。</HAlert>

      <div class="list" role="table" aria-label="目录内容">
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
          </div>
          <span class="size-col">{{ humanSize(it.size) }}</span>
        </div>

        <div v-if="filtered.length > shown.length" class="more">
          <HButton variant="tertiary" size="sm" @click="limit += PAGE">
            再显示 {{ Math.min(PAGE, filtered.length - shown.length) }} 项（共 {{ filtered.length }} 项）
          </HButton>
        </div>
      </div>
    </SectionCard>

    <ScrapeDialog v-model:show="showScrape" :body="jobBody" :in-library="selectionInLibrary" />
    <OrganizeDialog v-model:show="showOrganize" :body="jobBody" />
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

.list {
  display: flex;
  flex-direction: column;
  min-width: 0;
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

/* 手机：动作按钮整行，大小列收窄 */
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
}
</style>
