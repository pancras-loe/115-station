<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import {
  NAlert,
  NButton,
  NCheckbox,
  NInput,
  NPagination,
  NPopconfirm,
  NSelect,
  NSpin,
  NTag,
  NTooltip,
} from 'naive-ui'
import {
  Ban,
  Check,
  ChevronDown,
  File as FileIcon,
  Folder,
  PencilLine,
  RefreshCw,
  RotateCcw,
  Search,
  Trash2,
} from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import RedoDialog from '@/components/organize/RedoDialog.vue'
import { organizeApi, resourcesApi } from '@/api'
import type { OrganizeRecord } from '@/api/organize'
import type { TmdbCandidate } from '@/api/resources'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { useTaskStore } from '@/stores/task'
import { recordStats, refreshRecordStats } from './recordStats'

const { message } = useFeedback()
const task = useTaskStore()
const route = useRoute()

const rows = ref<OrganizeRecord[]>([])
const total = ref(0)
const page = ref(1)
const size = ref(20)
/** 从「开始整理」跳过来时带着 ?status=awaiting，直接落在待确认那一档 */
const status = ref((route.query.status as string) || 'all')
const mediaType = ref('all')
const keyword = ref('')
const loading = ref(false)

type TagType = 'success' | 'warning' | 'error' | 'info' | 'default'

/** 筛选栏：顺序即显示顺序。「需要处理」「待确认」是用户最常点的两档，排在最前 */
const FILTERS: { key: string; label: string; type: TagType }[] = [
  { key: 'all', label: '全部', type: 'default' },
  { key: 'awaiting', label: '待确认', type: 'warning' },
  { key: 'problem', label: '需要处理', type: 'error' },
  { key: 'success', label: '成功', type: 'success' },
  { key: 'exists', label: '已存在', type: 'info' },
  { key: 'unrecognized', label: '未识别', type: 'warning' },
  { key: 'failed', label: '失败', type: 'error' },
]

const TYPE_OPTIONS = [
  { label: '全部类型', value: 'all' },
  { label: '电影', value: 'movie' },
  { label: '剧集', value: 'tv' },
]

const SIZE_OPTIONS = [20, 50, 100]

const STATUS_META: Record<string, { text: string; type: TagType }> = {
  awaiting: { text: '待确认', type: 'warning' },
  success: { text: '成功', type: 'success' },
  exists: { text: '已存在', type: 'info' },
  unrecognized: { text: '未识别', type: 'warning' },
  failed: { text: '失败', type: 'error' },
}

const STAGE_TEXT: Record<string, string> = {
  recognize: '识别阶段',
  move: '搬移阶段',
  strm: 'STRM 阶段',
  scrape: '刮削阶段',
  confirm: '人工确认',
}

const KIND_TEXT: Record<string, string> = {
  video: '视频',
  subtitle: '字幕',
  meta: '元数据',
  junk: '其他',
}

async function load() {
  loading.value = true
  try {
    const d = await organizeApi.listRecords({
      status: status.value,
      type: mediaType.value === 'all' ? '' : mediaType.value,
      q: keyword.value.trim(),
      page: page.value,
      size: size.value,
    })
    rows.value = d.data ?? []
    total.value = d.total ?? 0
    // 翻页/筛选后，已不在当前页的勾选没有意义，按当前页收敛
    const visible = new Set(rows.value.map((r) => r.id))
    selected.value = new Set([...selected.value].filter((id) => visible.has(id)))
  } catch (e) {
    toastError(e, '读取整理记录失败')
  } finally {
    loading.value = false
  }
}

/** 列表 + 角标一起刷：确认/忽略/删除都会改变各档的条数 */
async function reload() {
  await Promise.all([load(), refreshRecordStats()])
}

function refilter() {
  page.value = 1
  void load()
}

function pickStatus(key: string) {
  if (status.value === key) return
  status.value = key
  selected.value = new Set()
  refilter()
}

function onSizeChange(n: number) {
  size.value = n
  refilter()
}

onMounted(reload)

function humanSize(n?: number) {
  if (!n) return ''
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(v >= 100 || i === 0 ? 0 : 1)} ${units[i]}`
}

function fullTime(s: string) {
  if (!s) return ''
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return s
  return d.toLocaleString('zh-CN', { hour12: false })
}

/** 列表里用相对时间，精确时间放在悬浮提示里 */
function relTime(s: string) {
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return s
  const diff = (Date.now() - d.getTime()) / 1000
  if (diff < 60) return '刚刚'
  if (diff < 3600) return `${Math.floor(diff / 60)} 分钟前`
  if (diff < 86400) return `${Math.floor(diff / 3600)} 小时前`
  const hm = d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false })
  if (diff < 2 * 86400) return `昨天 ${hm}`
  const md = `${d.getMonth() + 1}-${String(d.getDate()).padStart(2, '0')}`
  return d.getFullYear() === new Date().getFullYear() ? `${md} ${hm}` : `${d.getFullYear()}-${md}`
}

/** 成功记录的消息就是「→ 入库目录」，与上面那行重复；待确认的原因已经在 plan 那行 */
function showMsg(r: OrganizeRecord) {
  if (!r.message || r.status === 'awaiting') return false
  return !(r.status === 'success' && r.message.startsWith('→'))
}

function typeText(t: string) {
  return t === 'tv' ? '剧集' : t === 'movie' ? '电影' : ''
}

// ---- 文件清单展开 ----
const expanded = ref(new Set<number>())
function toggleFiles(id: number) {
  const s = new Set(expanded.value)
  if (s.has(id)) s.delete(id)
  else s.add(id)
  expanded.value = s
}

const busy = computed(() => acting.value !== 0 || batching.value || task.status.running)

// ---- 待确认：勾选与批量确认 ----
const selected = ref(new Set<number>())
/** 能直接确认的：已经识别出 TMDB 条目的待确认记录 */
const confirmable = computed(() => rows.value.filter((r) => r.status === 'awaiting' && r.tmdb_id > 0))
const allPicked = computed(
  () => confirmable.value.length > 0 && confirmable.value.every((r) => selected.value.has(r.id)),
)
const somePicked = computed(() => selected.value.size > 0 && !allPicked.value)

function toggleSelect(id: number, on: boolean) {
  const s = new Set(selected.value)
  if (on) s.add(id)
  else s.delete(id)
  selected.value = s
}
function toggleAll(on: boolean) {
  selected.value = on ? new Set(confirmable.value.map((r) => r.id)) : new Set()
}

const batching = ref(false)
async function confirmSelected() {
  const ids = [...selected.value]
  if (!ids.length) return
  batching.value = true
  message.info(`正在确认 ${ids.length} 项并入库…`)
  task.poll()
  try {
    const d = await organizeApi.confirmRecords(ids)
    message.success(d.message || '确认完成')
    selected.value = new Set()
    await reload()
  } catch (e) {
    toastError(e, '批量确认失败')
  } finally {
    batching.value = false
    task.poll()
  }
}

/** 正在处理的那一行（确认 / 忽略 / 深度删除），用来给按钮上 loading */
const acting = ref(0)

async function confirmOne(r: OrganizeRecord) {
  acting.value = r.id
  message.info(`正在按《${r.title}》入库…`)
  task.poll()
  try {
    const d = await organizeApi.confirmRecord(r.id)
    message.success(d.message || '已入库')
    await reload()
  } catch (e) {
    toastError(e, '确认失败')
    await reload() // 失败的会被后端改成「失败」，刷新好让用户看到原因
  } finally {
    acting.value = 0
    task.poll()
  }
}

async function ignoreOne(r: OrganizeRecord) {
  acting.value = r.id
  try {
    const d = await organizeApi.ignoreRecord(r.id)
    message.success(d.message || '已忽略')
    await reload()
  } catch (e) {
    toastError(e, '忽略失败')
  } finally {
    acting.value = 0
  }
}

// ---- 指定 TMDB 条目：已整理的走「重新整理」，待确认的走「按指定条目入库」 ----
const pickShow = ref(false)
const pickTarget = ref<OrganizeRecord | null>(null)
const pickMode = computed(() => (pickTarget.value?.status === 'awaiting' ? 'confirm' : 'redo'))

function openPick(r: OrganizeRecord) {
  pickTarget.value = r
  pickShow.value = true
}

async function doPick(pick: TmdbCandidate) {
  const target = pickTarget.value
  if (!target) return
  pickShow.value = false
  acting.value = target.id
  const confirming = target.status === 'awaiting'
  message.info(confirming ? `正在按《${pick.title}》入库…` : `正在按《${pick.title}》重新整理…`)
  task.poll()
  try {
    const d = confirming
      ? await organizeApi.confirmRecord(target.id, { tmdbId: pick.id, mediaType: pick.media_type })
      : await organizeApi.redoRecord(target.id, pick.id, pick.media_type)
    message.success(d.message || (confirming ? '已入库' : '重新整理完成'))
    await reload()
  } catch (e) {
    toastError(e, confirming ? '入库失败' : '重新整理失败')
    if (confirming) await reload()
  } finally {
    acting.value = 0
    task.poll()
  }
}

/**
 * 深度删除：删这条记录整理出来的**网盘源文件**，与下面 removeRecord（只删记录）
 * 完全两回事，所以按钮文案、颜色、确认框都要把差别摆明。
 */
async function deepDeleteRecord(r: OrganizeRecord) {
  acting.value = r.id
  try {
    const d = await organizeApi.deepDeleteRecord(r.id)
    message.success(d.message || '深度删除完成')
    await reload()
  } catch (e) {
    toastError(e, '深度删除失败')
  } finally {
    acting.value = 0
  }
}

async function removeRecord(r: OrganizeRecord) {
  try {
    await organizeApi.deleteRecord(r.id)
    await reload()
  } catch (e) {
    toastError(e, '删除失败')
  }
}

const clearHint = computed(() => {
  const label = FILTERS.find((f) => f.key === status.value)?.label ?? '全部'
  const tail =
    status.value === 'awaiting'
      ? '条目仍留在待整理目录里，下次整理会重新识别并再次等待确认。'
      : '只删记录，网盘与本地文件不受影响。'
  return `清空「${label}」下的所有记录？${tail}`
})

async function clearAll() {
  try {
    const d = await organizeApi.clearRecords(status.value)
    message.success(d.message || '已清空')
    page.value = 1
    await reload()
  } catch (e) {
    toastError(e, '清空失败')
  }
}
</script>

<template>
  <div class="stack">
    <SectionCard title="整理记录" hint="每次整理的留痕；待确认的条目在这里确认或改指定后入库">
      <NAlert
        v-if="(recordStats.awaiting || 0) > 0 && status !== 'awaiting'"
        type="warning"
        :bordered="false"
        class="banner"
      >
        有 <b>{{ recordStats.awaiting }}</b> 项识别完成、等你确认后入库。
        <NButton size="tiny" type="warning" secondary class="banner-btn" @click="pickStatus('awaiting')">
          去确认
        </NButton>
      </NAlert>

      <div class="chips" role="tablist">
        <button
          v-for="f in FILTERS"
          :key="f.key"
          type="button"
          role="tab"
          class="chip"
          :class="[`chip-${f.type}`, { active: status === f.key }]"
          :aria-selected="status === f.key"
          @click="pickStatus(f.key)"
        >
          <span>{{ f.label }}</span>
          <span class="chip-n">{{ recordStats[f.key] ?? 0 }}</span>
        </button>
      </div>

      <div class="toolbar">
        <NSelect
          v-model:value="mediaType"
          :options="TYPE_OPTIONS"
          class="type"
          @update:value="refilter"
        />
        <NInput
          v-model:value="keyword"
          placeholder="原名 / 片名 / 入库目录 / TMDB ID"
          clearable
          class="kw"
          @keyup.enter="refilter"
          @clear="refilter"
        >
          <template #prefix><Search :size="14" /></template>
        </NInput>
        <NButton @click="refilter">查询</NButton>
        <NTooltip>
          <template #trigger>
            <NButton quaternary :loading="loading" @click="reload"><RefreshCw :size="14" /></NButton>
          </template>
          刷新
        </NTooltip>
        <span class="grow" />
        <NPopconfirm @positive-click="void clearAll()">
          <template #trigger>
            <NButton quaternary type="error" :disabled="!total">
              <Trash2 :size="14" /><span class="btn-label">清空</span>
            </NButton>
          </template>
          {{ clearHint }}
        </NPopconfirm>
      </div>

      <div v-if="confirmable.length" class="batch">
        <NCheckbox
          :checked="allPicked"
          :indeterminate="somePicked"
          @update:checked="toggleAll"
        >
          全选本页可确认的 {{ confirmable.length }} 项
        </NCheckbox>
        <span class="grow" />
        <span v-if="selected.size" class="dim">已选 {{ selected.size }} 项</span>
        <NButton
          type="primary"
          size="small"
          :disabled="!selected.size || busy"
          :loading="batching"
          @click="confirmSelected"
        >
          <Check :size="14" /><span class="btn-label">确认所选并入库</span>
        </NButton>
      </div>

      <NSpin :show="loading">
        <EmptyState
          v-if="!rows.length && !loading"
          :text="
            status === 'awaiting'
              ? '没有待确认的条目'
              : status === 'all' && !keyword && mediaType === 'all'
                ? '还没有整理记录，跑一次自动整理就会出现'
                : '当前筛选下没有记录'
          "
        />

        <ul v-else class="list">
          <li
            v-for="r in rows"
            :key="r.id"
            class="row"
            :class="{ 'row-awaiting': r.status === 'awaiting', picked: selected.has(r.id) }"
          >
            <div v-if="r.status === 'awaiting'" class="pick">
              <NCheckbox
                :checked="selected.has(r.id)"
                :disabled="!r.tmdb_id"
                @update:checked="(v: boolean) => toggleSelect(r.id, v)"
              />
            </div>

            <img
              v-if="r.poster_path"
              :src="resourcesApi.tmdbImageUrl(r.poster_path)"
              class="poster"
              loading="lazy"
              :alt="r.title"
            />
            <div v-else class="poster poster-none">
              <Folder v-if="r.source_kind === 'dir'" :size="18" />
              <FileIcon v-else :size="18" />
            </div>

            <div class="main">
              <div class="head">
                <NTag size="small" :bordered="false" :type="STATUS_META[r.status]?.type ?? 'default'">
                  {{ STATUS_META[r.status]?.text ?? r.status }}
                </NTag>
                <b v-if="r.title" class="title">{{ r.title }}</b>
                <b v-else class="title untitled">未识别</b>
                <span v-if="r.year" class="dim">{{ r.year }}</span>
                <NTag v-if="typeText(r.media_type)" size="small" :bordered="false">
                  {{ typeText(r.media_type) }}
                </NTag>
                <a
                  v-if="r.tmdb_id"
                  class="dim link"
                  :href="`https://www.themoviedb.org/${r.media_type}/${r.tmdb_id}`"
                  target="_blank"
                  rel="noopener"
                  >tmdb={{ r.tmdb_id }}</a
                >
                <NTag v-if="r.manual_tmdb" size="small" :bordered="false" type="success">手动指定</NTag>
                <span v-if="r.stage && r.status !== 'success' && r.status !== 'awaiting'" class="dim">
                  {{ STAGE_TEXT[r.stage] ?? r.stage }}
                </span>
              </div>

              <div class="src" :title="r.source">
                <Folder v-if="r.source_kind === 'dir'" :size="13" class="src-icon" />
                <FileIcon v-else :size="13" class="src-icon" />
                <span class="src-text">{{ r.source }}</span>
              </div>

              <!-- 待确认：最要紧的是「会被整理成什么、放到哪」，单独一行突出 -->
              <div v-if="r.status === 'awaiting'" class="plan" :class="{ 'plan-miss': !r.tmdb_id }">
                <template v-if="r.tmdb_id">
                  <span class="plan-label">将入库到</span>
                  <code class="plan-dir">{{ r.target_dir || '（确认时按模板生成）' }}</code>
                </template>
                <template v-else>{{ r.message || '未能自动识别，请重新指定 TMDB 条目' }}</template>
              </div>

              <div class="meta">
                <NTooltip>
                  <template #trigger><span>{{ relTime(r.created_at) }}</span></template>
                  {{ fullTime(r.created_at) }}
                </NTooltip>
                <span v-if="r.target_dir && r.status !== 'awaiting'" class="meta-dir" :title="r.target_dir">
                  → {{ r.target_dir }}
                </span>
                <span v-if="r.category">{{ r.category }}</span>
                <span v-if="r.video_count">视频 {{ r.video_count }}</span>
                <span v-if="r.strm_created">STRM {{ r.strm_created }}</span>
                <span v-if="r.total_size">{{ humanSize(r.total_size) }}</span>
                <span v-if="r.redo_count">重做 {{ r.redo_count }} 次</span>
                <button
                  v-if="r.file_list?.length"
                  type="button"
                  class="files-toggle"
                  :class="{ open: expanded.has(r.id) }"
                  @click="toggleFiles(r.id)"
                >
                  文件 {{ r.file_list.length }}<ChevronDown :size="13" />
                </button>
              </div>

              <p v-if="showMsg(r)" class="msg" :class="`msg-${r.status}`">
                {{ r.message }}
              </p>

              <ul v-if="expanded.has(r.id)" class="files">
                <li v-for="f in r.file_list" :key="f.fid" class="file">
                  <span class="file-kind" :class="`k-${f.kind}`">{{ KIND_TEXT[f.kind] ?? f.kind }}</span>
                  <span class="file-name" :title="f.name">
                    {{ f.name }}
                    <span v-if="f.orig" class="file-orig" :title="f.orig">← {{ f.orig }}</span>
                  </span>
                  <span v-if="f.size" class="file-size">{{ humanSize(f.size) }}</span>
                </li>
              </ul>
            </div>

            <div class="ops">
              <template v-if="r.status === 'awaiting'">
                <NTooltip :disabled="!!r.tmdb_id">
                  <template #trigger>
                    <NButton
                      size="small"
                      type="primary"
                      :disabled="!r.tmdb_id || busy"
                      :loading="acting === r.id"
                      @click="confirmOne(r)"
                    >
                      <Check :size="14" /><span class="btn-label">确认入库</span>
                    </NButton>
                  </template>
                  没有识别结果，请先「重新指定」
                </NTooltip>
                <NButton size="small" :disabled="busy" @click="openPick(r)">
                  <PencilLine :size="14" /><span class="btn-label">重新指定</span>
                </NButton>
                <NPopconfirm @positive-click="void ignoreOne(r)">
                  <template #trigger>
                    <NButton size="small" quaternary :disabled="busy">
                      <Ban :size="14" /><span class="btn-label">忽略</span>
                    </NButton>
                  </template>
                  不整理这一项：移到冗余目录，记录改为「未识别」。<br />之后仍可在记录上重新整理捞回来。
                </NPopconfirm>
              </template>

              <template v-else>
                <NTooltip v-if="r.file_list?.length">
                  <template #trigger>
                    <NButton size="small" :disabled="busy" :loading="acting === r.id" @click="openPick(r)">
                      <RotateCcw :size="14" /><span class="btn-label">重新整理</span>
                    </NButton>
                  </template>
                  指定正确的 TMDB 条目，把这 {{ r.file_list.length }} 个文件从当前位置改名并搬到正确目录
                </NTooltip>
                <!-- 两个删除按钮差别极大，必须让人一眼分清：
                     这个删网盘真文件（实心红 + 文字），下面那个只删记录（弱化图标） -->
                <NPopconfirm v-if="r.file_list?.length" @positive-click="void deepDeleteRecord(r)">
                  <template #trigger>
                    <NButton size="small" type="error" ghost :disabled="busy">
                      <Trash2 :size="14" /><span class="btn-label">深度删除</span>
                    </NButton>
                  </template>
                  连同 <b>115 网盘上的源文件</b>一起删掉《{{ r.title || r.source }}》的
                  {{ r.file_list.length }} 个文件，本地 STRM 与台账一并清理。<br />
                  文件进入 115 回收站，可以还原。
                </NPopconfirm>
              </template>

              <NPopconfirm @positive-click="void removeRecord(r)">
                <template #trigger>
                  <NButton size="small" quaternary type="error" :disabled="acting === r.id">
                    <Trash2 :size="14" />
                  </NButton>
                </template>
                删除这条记录？只删记录，网盘与本地文件不受影响。
                <template v-if="r.status === 'awaiting'">
                  <br />条目仍在待整理目录里，下次整理会重新识别。
                </template>
              </NPopconfirm>
            </div>
          </li>
        </ul>
      </NSpin>

      <div v-if="total > 0" class="pager">
        <span class="dim">共 {{ total }} 条</span>
        <NPagination
          v-model:page="page"
          :page-size="size"
          :item-count="total"
          :page-sizes="SIZE_OPTIONS"
          show-size-picker
          @update:page="load"
          @update:page-size="onSizeChange"
        />
      </div>
    </SectionCard>

    <RedoDialog v-model:show="pickShow" :record="pickTarget" :mode="pickMode" @confirm="doPick" />
  </div>
</template>

<style scoped>
.stack {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.banner {
  margin-bottom: 12px;
}
.banner-btn {
  margin-left: 8px;
}

/* ---- 状态筛选 ---- */
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 10px;
}
.chip {
  all: unset;
  box-sizing: border-box;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 28px;
  padding: 0 10px;
  border: 1px solid var(--c-border);
  border-radius: 999px;
  font-size: 12.5px;
  color: var(--c-text-2);
  cursor: pointer;
  transition:
    background-color 0.15s,
    border-color 0.15s,
    color 0.15s;
}
.chip:hover {
  background: var(--c-bg-hover);
}
.chip:focus-visible {
  outline: 2px solid var(--c-primary-border);
  outline-offset: 1px;
}
.chip-n {
  min-width: 18px;
  padding: 0 5px;
  border-radius: 999px;
  background: var(--c-bg-hover);
  color: var(--c-text-3);
  font-size: 11.5px;
  text-align: center;
  font-variant-numeric: tabular-nums;
}
.chip.active {
  border-color: var(--c-primary-border);
  background: var(--c-primary-soft);
  color: var(--c-primary);
}
.chip.active .chip-n {
  background: var(--c-primary);
  color: var(--c-text-inverse);
}
.chip-warning.active {
  border-color: var(--c-warning);
  background: var(--c-warning-soft);
  color: var(--c-warning);
}
.chip-warning.active .chip-n {
  background: var(--c-warning);
}
.chip-error.active {
  border-color: var(--c-danger);
  background: var(--c-danger-soft);
  color: var(--c-danger);
}
.chip-error.active .chip-n {
  background: var(--c-danger);
}
.chip-success.active {
  border-color: var(--c-success);
  background: var(--c-success-soft);
  color: var(--c-success);
}
.chip-success.active .chip-n {
  background: var(--c-success);
}

.toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
  flex-wrap: wrap;
}
.type {
  width: 120px;
}
.kw {
  width: 260px;
  max-width: 100%;
}
.grow {
  flex: 1;
}

.batch {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  padding: 8px 12px;
  margin-bottom: 10px;
  border-radius: var(--radius);
  background: var(--c-warning-soft);
}

/* ---- 列表 ---- */
.list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.row {
  display: flex;
  gap: 12px;
  padding: 10px;
  border: 1px solid var(--c-border);
  border-radius: var(--radius);
  transition: border-color 0.15s;
}
.row:hover {
  border-color: var(--c-border-strong);
}
.row-awaiting {
  border-left: 3px solid var(--c-warning);
}
.row.picked {
  background: var(--c-bg-raised);
}
.pick {
  display: flex;
  align-items: flex-start;
  padding-top: 2px;
}
.poster {
  width: 46px;
  height: 69px;
  flex: none;
  object-fit: cover;
  border-radius: var(--radius-sm);
  background: var(--c-bg-hover);
}
.poster-none {
  display: grid;
  place-items: center;
  color: var(--c-text-4);
}

.main {
  flex: 1;
  min-width: 0;
}
.head {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.title {
  font-size: 14px;
  color: var(--c-text-1);
}
.untitled {
  color: var(--c-text-3);
  font-weight: normal;
}
.dim {
  font-size: 12px;
  color: var(--c-text-3);
}
.link {
  text-decoration: none;
}
.link:hover {
  color: var(--c-primary);
}
.src {
  margin-top: 4px;
  display: flex;
  align-items: center;
  gap: 5px;
  font-size: 12.5px;
  color: var(--c-text-2);
  min-width: 0;
}
.src-icon {
  flex: none;
  color: var(--c-text-3);
}
.src-text {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.plan {
  margin-top: 6px;
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  font-size: 12.5px;
  color: var(--c-text-2);
}
.plan-label {
  color: var(--c-text-3);
}
.plan-dir {
  padding: 1px 6px;
  border-radius: var(--radius-sm);
  background: var(--c-bg-hover);
  color: var(--c-text-1);
  font-size: 12px;
  word-break: break-all;
}
.plan-miss {
  color: var(--c-warning);
}

.meta {
  margin-top: 5px;
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  font-size: 12px;
  color: var(--c-text-3);
}
.meta-dir {
  max-width: 420px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.files-toggle {
  all: unset;
  display: inline-flex;
  align-items: center;
  gap: 2px;
  cursor: pointer;
  color: var(--c-primary);
}
.files-toggle :deep(svg) {
  transition: transform 0.15s;
}
.files-toggle.open :deep(svg) {
  transform: rotate(180deg);
}
.files-toggle:focus-visible {
  outline: 2px solid var(--c-primary-border);
  border-radius: 2px;
}

.msg {
  margin: 5px 0 0;
  font-size: 12.5px;
  color: var(--c-text-2);
  word-break: break-all;
}
.msg-failed {
  color: var(--c-danger);
}
.msg-unrecognized {
  color: var(--c-warning);
}

.files {
  list-style: none;
  margin: 8px 0 0;
  padding: 6px 8px;
  border-radius: var(--radius-sm);
  background: var(--c-bg-raised);
  max-height: 260px;
  overflow-y: auto;
}
.file {
  display: flex;
  align-items: baseline;
  gap: 8px;
  padding: 3px 0;
  font-size: 12px;
  color: var(--c-text-2);
}
.file + .file {
  border-top: 1px dashed var(--c-border);
}
.file-kind {
  flex: none;
  width: 42px;
  color: var(--c-text-3);
}
.k-video {
  color: var(--c-primary);
}
.file-name {
  flex: 1;
  min-width: 0;
  word-break: break-all;
}
.file-orig {
  display: block;
  color: var(--c-text-3);
}
.file-size {
  flex: none;
  color: var(--c-text-3);
  font-variant-numeric: tabular-nums;
}

.ops {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  flex: none;
  flex-wrap: wrap;
  justify-content: flex-end;
  max-width: 380px;
}
.btn-label {
  margin-left: 4px;
}

.pager {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 12px;
  flex-wrap: wrap;
  margin-top: 12px;
}

@media (max-width: 720px) {
  .row {
    flex-wrap: wrap;
  }
  .main {
    flex-basis: calc(100% - 70px);
  }
  .ops {
    max-width: none;
    width: 100%;
    justify-content: flex-start;
  }
  .kw {
    width: 100%;
  }
}
</style>
