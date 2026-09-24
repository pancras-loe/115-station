<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import {
  Ban,
  Check,
  ChevronDown,
  File as FileIcon,
  Folder,
  PencilLine,
  RefreshCw,
  RotateCcw,
  Trash2,
} from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HCheckbox from '@/components/hero/HCheckbox.vue'
import HChip from '@/components/hero/HChip.vue'
import HPagination from '@/components/hero/HPagination.vue'
import HPopconfirm from '@/components/hero/HPopconfirm.vue'
import HSearchField from '@/components/hero/HSearchField.vue'
import HSelect from '@/components/hero/HSelect.vue'
import HSkeleton from '@/components/hero/HSkeleton.vue'
import HTabs from '@/components/hero/HTabs.vue'
import HTooltip from '@/components/hero/HTooltip.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import SourceLink from '@/components/ui/SourceLink.vue'
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

/** 筛选栏用页签的样子：每档带条数，「待确认」「需要处理」的角标用实色，有货时一眼看得到 */
const filterTabs = computed(() =>
  FILTERS.map((f) => ({
    value: f.key,
    label: f.label,
    count: recordStats.value[f.key] ?? 0,
    countTone:
      f.key === 'awaiting' ? ('warning' as const) : f.key === 'problem' ? ('danger' as const) : ('accent' as const),
  })),
)

/** 状态语义 → HeroUI chip 颜色 */
function chipColor(t?: TagType) {
  return ({ success: 'success', warning: 'warning', error: 'danger', info: 'accent' } as const)[
    t as 'success' | 'warning' | 'error' | 'info'
  ] ?? 'default'
}

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

function onPageChange(n: number) {
  page.value = n
  void load()
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
      <HAlert
        v-if="(recordStats.awaiting || 0) > 0 && status !== 'awaiting'"
        status="warning"
        class="banner"
        :title="`有 ${recordStats.awaiting} 项等你确认后入库`"
      >
        识别已经完成，文件还留在待整理目录里，确认之前不会搬动。
        <template #actions>
          <HButton size="sm" variant="primary" @click="pickStatus('awaiting')">去确认</HButton>
        </template>
      </HAlert>

      <HTabs
        :model-value="status"
        :items="filterTabs"
        class="filters"
        @update:model-value="pickStatus"
      />

      <div class="toolbar">
        <div class="type">
          <HSelect v-model="mediaType" :options="TYPE_OPTIONS" aria-label="媒体类型" @update:model-value="refilter" />
        </div>
        <HSearchField
          v-model="keyword"
          class="kw"
          placeholder="原名 / 片名 / 入库目录 / 来源链接 / TMDB ID"
          @search="refilter"
        />
        <HButton variant="tertiary" class="query-btn" @click="refilter">查询</HButton>
        <HTooltip content="刷新">
          <HButton variant="ghost" icon-only :loading="loading" aria-label="刷新" @click="reload">
            <RefreshCw />
          </HButton>
        </HTooltip>
        <span class="grow" />
        <HPopconfirm danger confirm-text="清空" :disabled="!total" @confirm="clearAll">
          <HButton variant="danger-soft" :disabled="!total">
            <template #icon><Trash2 /></template>
            <span class="btn-label">清空</span>
          </HButton>
          <template #content>{{ clearHint }}</template>
        </HPopconfirm>
      </div>

      <div v-if="confirmable.length" class="batch">
        <HCheckbox :checked="allPicked" :indeterminate="somePicked" @update:checked="toggleAll">
          全选本页可确认的 {{ confirmable.length }} 项
        </HCheckbox>
        <span class="grow" />
        <span v-if="selected.size" class="dim">已选 {{ selected.size }} 项</span>
        <HButton
          variant="primary"
          size="sm"
          :disabled="!selected.size || busy"
          :loading="batching"
          @click="confirmSelected"
        >
          <template #icon><Check /></template>
          确认所选并入库
        </HButton>
      </div>

      <div class="list-wrap" :class="{ 'is-loading': loading && rows.length }" :aria-busy="loading">
        <div v-if="loading && !rows.length" class="list">
          <div v-for="i in 4" :key="i" class="row row-skel">
            <HSkeleton width="46px" height="69px" radius="10px" />
            <div class="skel-lines">
              <HSkeleton width="45%" height="14px" radius="999px" />
              <HSkeleton width="80%" height="11px" radius="999px" />
              <HSkeleton width="60%" height="11px" radius="999px" />
            </div>
          </div>
        </div>

        <EmptyState
          v-else-if="!rows.length"
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
              <HCheckbox
                :checked="selected.has(r.id)"
                :disabled="!r.tmdb_id"
                :aria-label="`选择 ${r.title || r.source}`"
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
                <HChip :color="chipColor(STATUS_META[r.status]?.type)">
                  {{ STATUS_META[r.status]?.text ?? r.status }}
                </HChip>
                <b v-if="r.title" class="title">{{ r.title }}</b>
                <b v-else class="title untitled">未识别</b>
                <span v-if="r.year" class="dim">{{ r.year }}</span>
                <HChip v-if="typeText(r.media_type)">{{ typeText(r.media_type) }}</HChip>
                <a
                  v-if="r.tmdb_id"
                  class="dim link"
                  :href="`https://www.themoviedb.org/${r.media_type}/${r.tmdb_id}`"
                  target="_blank"
                  rel="noopener"
                  >tmdb={{ r.tmdb_id }}</a
                >
                <HChip v-if="r.manual_tmdb" color="success">手动指定</HChip>
                <template v-else-if="r.recog_via">
                  <HTooltip v-if="r.ai_note" :content="r.ai_note">
                    <span tabindex="0" class="chip-trigger">
                      <HChip :color="(r.ai_score ?? 0) >= 80 ? 'accent' : 'warning'">
                        {{ r.recog_via === 'ai_pick' ? 'AI 选定' : 'AI 识别' }} {{ r.ai_score ?? 0 }} 分
                      </HChip>
                    </span>
                  </HTooltip>
                  <HChip v-else :color="(r.ai_score ?? 0) >= 80 ? 'accent' : 'warning'">
                    {{ r.recog_via === 'ai_pick' ? 'AI 选定' : 'AI 识别' }} {{ r.ai_score ?? 0 }} 分
                  </HChip>
                </template>
                <span v-if="r.stage && r.status !== 'success' && r.status !== 'awaiting'" class="dim">
                  {{ STAGE_TEXT[r.stage] ?? r.stage }}
                </span>
              </div>

              <div class="src" :title="r.source">
                <Folder v-if="r.source_kind === 'dir'" :size="13" class="src-icon" />
                <FileIcon v-else :size="13" class="src-icon" />
                <span class="src-text">{{ r.source }}</span>
              </div>
              <SourceLink v-if="r.link" class="from" :link="r.link.url" :kind="r.link.kind" />

              <!-- 待确认：最要紧的是「会被整理成什么、放到哪」，单独一行突出 -->
              <div v-if="r.status === 'awaiting'" class="plan" :class="{ 'plan-miss': !r.tmdb_id }">
                <template v-if="r.tmdb_id">
                  <span class="plan-label">将入库到</span>
                  <code class="plan-dir">{{ r.target_dir || '（确认时按模板生成）' }}</code>
                </template>
                <template v-else>{{ r.message || '未能自动识别，请重新指定 TMDB 条目' }}</template>
              </div>

              <div class="meta">
                <!-- 精确时间放 title：触屏没有 hover，长按也能看到 -->
                <span :title="fullTime(r.created_at)">{{ relTime(r.created_at) }}</span>
                <span v-if="r.target_dir && r.status !== 'awaiting'" class="meta-dir" :title="r.target_dir">
                  → {{ r.target_dir }}
                </span>
                <span v-if="r.category">{{ r.category }}</span>
                <span v-if="r.video_count">视频 {{ r.video_count }}</span>
                <span v-if="r.strm_created">STRM {{ r.strm_created }}</span>
                <span v-if="r.total_size">{{ humanSize(r.total_size) }}</span>
                <span v-if="r.redo_count">重做 {{ r.redo_count }} 次</span>
                <span v-if="r.link?.source">经 {{ r.link.source }} 提交</span>
                <button
                  v-if="r.file_list?.length"
                  type="button"
                  class="files-toggle"
                  :class="{ open: expanded.has(r.id) }"
                  :aria-expanded="expanded.has(r.id)"
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
                <HButton
                  size="sm"
                  variant="primary"
                  :disabled="!r.tmdb_id || busy"
                  :loading="acting === r.id"
                  :title="r.tmdb_id ? undefined : '没有识别结果，请先「重新指定」'"
                  @click="confirmOne(r)"
                >
                  <template #icon><Check /></template>
                  确认入库
                </HButton>
                <HButton size="sm" variant="tertiary" :disabled="busy" @click="openPick(r)">
                  <template #icon><PencilLine /></template>
                  重新指定
                </HButton>
                <HPopconfirm confirm-text="忽略" :disabled="busy" @confirm="ignoreOne(r)">
                  <HButton size="sm" variant="ghost" :disabled="busy">
                    <template #icon><Ban /></template>
                    忽略
                  </HButton>
                  <template #content>
                    不整理这一项：移到冗余目录，记录改为「未识别」。<br />之后仍可在记录上重新整理捞回来。
                  </template>
                </HPopconfirm>
              </template>

              <template v-else>
                <HButton
                  v-if="r.file_list?.length"
                  size="sm"
                  variant="tertiary"
                  :disabled="busy"
                  :loading="acting === r.id"
                  :title="`指定正确的 TMDB 条目，把这 ${r.file_list.length} 个文件从当前位置改名并搬到正确目录`"
                  @click="openPick(r)"
                >
                  <template #icon><RotateCcw /></template>
                  重新整理
                </HButton>
                <!-- 两个删除按钮差别极大，必须让人一眼分清：
                     这个删网盘真文件（红色实底 + 文字），下面那个只删记录（弱化的图标） -->
                <HPopconfirm
                  v-if="r.file_list?.length"
                  danger
                  confirm-text="深度删除"
                  :disabled="busy"
                  @confirm="deepDeleteRecord(r)"
                >
                  <HButton size="sm" variant="danger-soft" :disabled="busy">
                    <template #icon><Trash2 /></template>
                    深度删除
                  </HButton>
                  <template #content>
                    连同 <b>115 网盘上的源文件</b>一起删掉《{{ r.title || r.source }}》的
                    {{ r.file_list.length }} 个文件，本地 STRM 与台账一并清理。<br />
                    文件进入 115 回收站，可以还原。
                  </template>
                </HPopconfirm>
              </template>

              <HPopconfirm confirm-text="删除记录" :disabled="acting === r.id" @confirm="removeRecord(r)">
                <HButton
                  size="sm"
                  variant="ghost"
                  icon-only
                  class="rm-record"
                  :disabled="acting === r.id"
                  aria-label="删除这条记录"
                  title="只删记录"
                >
                  <Trash2 />
                </HButton>
                <template #content>
                  删除这条记录？只删记录，网盘与本地文件不受影响。
                  <template v-if="r.status === 'awaiting'">
                    <br />条目仍在待整理目录里，下次整理会重新识别。
                  </template>
                </template>
              </HPopconfirm>
            </div>
          </li>
        </ul>
      </div>

      <HPagination
        v-if="total > 0"
        :page="page"
        :page-size="size"
        class="pager"
        :total="total"
        :page-sizes="SIZE_OPTIONS"
        @update:page="onPageChange"
        @update:page-size="onSizeChange"
      />
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
  margin-bottom: 14px;
  background: var(--warning-soft);
  box-shadow: none;
}

.filters {
  margin-bottom: 12px;
}

.toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
  flex-wrap: wrap;
}
.type {
  width: 124px;
}
.kw {
  width: 300px;
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
  padding: 10px 14px;
  margin-bottom: 12px;
  border-radius: 16px;
  background: var(--warning-soft);
}

/* ---- 列表 ---- */
.list-wrap {
  transition: opacity 150ms ease;
}
/* 翻页/筛选时保留旧列表淡出，而不是闪成骨架：页面高度不跳 */
.list-wrap.is-loading {
  opacity: 0.55;
  pointer-events: none;
}
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
  gap: 14px;
  padding: 12px;
  border-radius: 20px;
  background: var(--surface-secondary);
  transition: background-color 150ms ease;
}
@media (hover: hover) {
  .row:hover {
    background: color-mix(in oklab, var(--surface-secondary) 70%, var(--surface-tertiary));
  }
}
/* 待确认：左侧一道警示色，列表里一眼能挑出来 */
.row-awaiting {
  box-shadow: inset 3px 0 0 var(--warning);
}
.row.picked {
  background: var(--accent-soft);
}
.row-skel {
  align-items: center;
}
.skel-lines {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.pick {
  display: flex;
  align-items: flex-start;
  padding-top: 4px;
}
.poster {
  width: 46px;
  height: 69px;
  flex: none;
  object-fit: cover;
  border-radius: 10px;
  background: var(--default);
}
.poster-none {
  display: grid;
  place-items: center;
  color: var(--muted);
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
.chip-trigger {
  display: inline-flex;
  border-radius: 999px;
  outline: none;
}
.chip-trigger:focus-visible {
  box-shadow: 0 0 0 2px var(--focus);
}
.title {
  font-size: 14px;
  font-weight: 600;
  color: var(--foreground);
}
.untitled {
  color: var(--muted);
  font-weight: normal;
}
.dim {
  font-size: 12px;
  color: var(--muted);
}
.link {
  text-decoration: none;
}
.link:hover {
  color: var(--accent);
}
.from {
  margin-top: 4px;
  max-width: 560px;
}
.src {
  margin-top: 6px;
  display: flex;
  align-items: center;
  gap: 5px;
  font-size: 12.5px;
  color: color-mix(in oklab, var(--foreground) 70%, var(--muted));
  min-width: 0;
}
.src-icon {
  flex: none;
  color: var(--muted);
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
  color: color-mix(in oklab, var(--foreground) 70%, var(--muted));
}
.plan-label {
  color: var(--muted);
}
.plan-dir {
  padding: 2px 8px;
  border-radius: 8px;
  background: var(--surface);
  color: var(--foreground);
  font-size: 12px;
  word-break: break-all;
}
.plan-miss {
  color: var(--warning-soft-foreground);
}

.meta {
  margin-top: 6px;
  display: flex;
  align-items: center;
  gap: 4px 12px;
  flex-wrap: wrap;
  font-size: 12px;
  color: var(--muted);
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
  color: var(--accent);
  font-weight: 500;
}
.files-toggle :deep(svg) {
  transition: transform 0.15s;
}
.files-toggle.open :deep(svg) {
  transform: rotate(180deg);
}
.files-toggle:focus-visible {
  border-radius: 4px;
  box-shadow: 0 0 0 2px var(--focus);
}

.msg {
  margin: 6px 0 0;
  font-size: 12.5px;
  color: color-mix(in oklab, var(--foreground) 70%, var(--muted));
  word-break: break-all;
}
.msg-failed {
  color: var(--danger-soft-foreground);
}
.msg-unrecognized {
  color: var(--warning-soft-foreground);
}

.files {
  list-style: none;
  margin: 10px 0 0;
  padding: 6px 12px;
  border-radius: 14px;
  background: var(--surface);
  max-height: 260px;
  overflow-y: auto;
}
.file {
  display: flex;
  align-items: baseline;
  gap: 8px;
  padding: 5px 0;
  font-size: 12px;
  color: color-mix(in oklab, var(--foreground) 70%, var(--muted));
}
.file + .file {
  border-top: 1px solid var(--separator);
}
.file-kind {
  flex: none;
  width: 42px;
  color: var(--muted);
}
.k-video {
  color: var(--accent);
}
.file-name {
  flex: 1;
  min-width: 0;
  word-break: break-all;
}
.file-orig {
  display: block;
  color: var(--muted);
}
.file-size {
  flex: none;
  color: var(--muted);
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
.rm-record {
  color: var(--muted);
}
@media (hover: hover) {
  .rm-record:hover {
    color: var(--danger);
  }
}

.pager {
  margin-top: 16px;
}

@media (max-width: 720px) {
  /* 手机上：搜索框独占第一行，类型 / 刷新 / 清空排第二行 */
  .kw {
    order: -1;
    flex: 1 1 100%;
    width: auto;
  }
  .type {
    width: 116px;
  }
  .query-btn {
    display: none;
  }
  .btn-label {
    display: none;
  }

  .row {
    position: relative;
    flex-wrap: wrap;
    gap: 12px;
    padding: 12px;
    border-radius: 18px;
  }
  .main {
    flex-basis: calc(100% - 70px);
  }
  .row-awaiting .main {
    flex-basis: calc(100% - 100px);
  }
  /* 「只删记录」挪到卡片右上角：操作行留给真正的动作，三个按钮一排放得下 */
  .head {
    padding-right: 28px;
  }
  .rm-record {
    position: absolute;
    top: 6px;
    right: 6px;
  }
  .meta-dir {
    max-width: 100%;
  }
  .ops {
    max-width: none;
    width: 100%;
    justify-content: flex-start;
    padding-top: 10px;
    border-top: 1px solid var(--separator);
  }
}
</style>
