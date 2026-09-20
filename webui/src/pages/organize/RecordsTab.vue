<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import {
  NButton,
  NInput,
  NPagination,
  NPopconfirm,
  NSelect,
  NSpin,
  NTag,
  NTooltip,
} from 'naive-ui'
import { RotateCcw, Search, Trash2 } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import SourceLink from '@/components/ui/SourceLink.vue'
import RedoDialog from '@/components/organize/RedoDialog.vue'
import { organizeApi, resourcesApi } from '@/api'
import type { OrganizeRecord } from '@/api/organize'
import type { TmdbCandidate } from '@/api/resources'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { useTaskStore } from '@/stores/task'

const { message } = useFeedback()
const task = useTaskStore()

const rows = ref<OrganizeRecord[]>([])
const total = ref(0)
const page = ref(1)
const size = ref(20)
const status = ref('all')
const keyword = ref('')
const loading = ref(false)

const STATUS_OPTIONS = [
  { label: '全部', value: 'all' },
  { label: '需要处理（失败 + 未识别）', value: 'problem' },
  { label: '成功', value: 'success' },
  { label: '已存在', value: 'exists' },
  { label: '识别失败', value: 'unrecognized' },
  { label: '执行失败', value: 'failed' },
]

const STATUS_META: Record<string, { text: string; type: 'success' | 'warning' | 'error' | 'info' }> = {
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
}

async function load() {
  loading.value = true
  try {
    const d = await organizeApi.listRecords({
      status: status.value,
      q: keyword.value.trim(),
      page: page.value,
      size: size.value,
    })
    rows.value = d.data ?? []
    total.value = d.total ?? 0
  } catch (e) {
    toastError(e, '读取整理记录失败')
  } finally {
    loading.value = false
  }
}

function refilter() {
  page.value = 1
  load()
}

onMounted(load)

function humanSize(n: number) {
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

function humanTime(s: string) {
  if (!s) return ''
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return s
  return d.toLocaleString('zh-CN', { hour12: false })
}

// ---- 重新整理 ----
const redoShow = ref(false)
const redoTarget = ref<OrganizeRecord | null>(null)
const redoing = ref(false)
const busy = computed(() => redoing.value || task.status.running)

function openRedo(r: OrganizeRecord) {
  redoTarget.value = r
  redoShow.value = true
}

async function doRedo(pick: TmdbCandidate) {
  const target = redoTarget.value
  if (!target) return
  redoShow.value = false
  redoing.value = true
  message.info(`正在按《${pick.title}》重新整理…`)
  task.poll()
  try {
    const d = await organizeApi.redoRecord(target.id, pick.id, pick.media_type)
    message.success(d.message || '重新整理完成')
    await load()
  } catch (e) {
    toastError(e, '重新整理失败')
  } finally {
    redoing.value = false
    task.poll()
  }
}

async function removeRecord(r: OrganizeRecord) {
  try {
    await organizeApi.deleteRecord(r.id)
    await load()
  } catch (e) {
    toastError(e, '删除失败')
  }
}

async function clearAll() {
  try {
    const d = await organizeApi.clearRecords(status.value)
    message.success(d.message || '已清空')
    page.value = 1
    await load()
  } catch (e) {
    toastError(e, '清空失败')
  }
}
</script>

<template>
  <div class="stack">
    <SectionCard title="整理记录" hint="每次整理的留痕，识别错了可以指定 TMDB 条目重做">
      <div class="toolbar">
        <NSelect
          v-model:value="status"
          :options="STATUS_OPTIONS"
          class="filter"
          @update:value="refilter"
        />
        <NInput
          v-model:value="keyword"
          placeholder="搜索原名或片名"
          clearable
          class="kw"
          @keyup.enter="refilter"
          @clear="refilter"
        >
          <template #prefix><Search :size="14" /></template>
        </NInput>
        <NButton @click="refilter">查询</NButton>
        <NPopconfirm @positive-click="void clearAll()">
          <template #trigger>
            <NButton quaternary type="error"><Trash2 :size="14" /></NButton>
          </template>
          清空当前筛选下的所有记录？只删记录，网盘与本地文件不受影响。
        </NPopconfirm>
      </div>

      <NSpin :show="loading">
        <EmptyState v-if="!rows.length && !loading" text="还没有整理记录，跑一次自动整理就会出现" />

        <ul v-else class="list">
          <li v-for="r in rows" :key="r.id" class="row">
            <img
              v-if="r.poster_path"
              :src="resourcesApi.tmdbImageUrl(r.poster_path)"
              class="poster"
              loading="lazy"
              :alt="r.title"
            />
            <div v-else class="poster poster-none">—</div>

            <div class="main">
              <div class="head">
                <NTag
                  size="small"
                  :bordered="false"
                  :type="STATUS_META[r.status]?.type ?? 'default'"
                >
                  {{ STATUS_META[r.status]?.text ?? r.status }}
                </NTag>
                <b v-if="r.title" class="title">{{ r.title }}</b>
                <span v-if="r.year" class="dim">{{ r.year }}</span>
                <a
                  v-if="r.tmdb_id"
                  class="dim link"
                  :href="`https://www.themoviedb.org/${r.media_type}/${r.tmdb_id}`"
                  target="_blank"
                  rel="noopener"
                  >tmdb={{ r.tmdb_id }}</a
                >
                <NTag v-if="r.manual_tmdb" size="small" :bordered="false" type="success">
                  手动指定
                </NTag>
                <span v-if="r.stage" class="dim">{{ STAGE_TEXT[r.stage] ?? r.stage }}</span>
              </div>

              <div class="src" :title="r.source">{{ r.source }}</div>

              <SourceLink
                v-if="r.source_link"
                class="from"
                :link="r.source_link"
                :kind="r.source_link_kind"
              />

              <div class="meta">
                <span>{{ humanTime(r.created_at) }}</span>
                <span v-if="r.target_dir">→ {{ r.target_dir }}</span>
                <span v-if="r.video_count">视频 {{ r.video_count }}</span>
                <span v-if="r.strm_created">STRM {{ r.strm_created }}</span>
                <span v-if="r.total_size">{{ humanSize(r.total_size) }}</span>
                <span v-if="r.redo_count">重做 {{ r.redo_count }} 次</span>
              </div>

              <p v-if="r.message" class="msg">{{ r.message }}</p>
            </div>

            <div class="ops">
              <NTooltip v-if="r.file_list?.length">
                <template #trigger>
                  <NButton size="small" :disabled="busy" @click="openRedo(r)">
                    <RotateCcw :size="14" /><span class="btn-label">重新整理</span>
                  </NButton>
                </template>
                指定正确的 TMDB 条目，把这 {{ r.file_list.length }} 个文件从当前位置改名并搬到正确目录
              </NTooltip>
              <NPopconfirm @positive-click="void removeRecord(r)">
                <template #trigger>
                  <NButton size="small" quaternary type="error"><Trash2 :size="14" /></NButton>
                </template>
                删除这条记录？只删记录，网盘与本地文件不受影响。
              </NPopconfirm>
            </div>
          </li>
        </ul>
      </NSpin>

      <div v-if="total > size" class="pager">
        <NPagination
          v-model:page="page"
          :page-size="size"
          :item-count="total"
          @update:page="load"
        />
      </div>
    </SectionCard>

    <RedoDialog v-model:show="redoShow" :record="redoTarget" @confirm="doRedo" />
  </div>
</template>

<style scoped>
.stack {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.toolbar {
  display: flex;
  gap: 8px;
  margin-bottom: 12px;
  flex-wrap: wrap;
}
.filter {
  width: 220px;
}
.kw {
  width: 240px;
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
  gap: 12px;
  padding: 10px;
  border: 1px solid var(--c-border);
  border-radius: var(--radius);
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
  font-size: 12px;
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
.from {
  margin-top: 2px;
  max-width: 520px;
}
.src {
  margin-top: 4px;
  font-size: 12.5px;
  color: var(--c-text-2);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.meta {
  margin-top: 4px;
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
  font-size: 12px;
  color: var(--c-text-3);
}
.msg {
  margin: 5px 0 0;
  font-size: 12.5px;
  color: var(--c-text-2);
}

.ops {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  flex: none;
}
.btn-label {
  margin-left: 4px;
}

.pager {
  display: flex;
  justify-content: flex-end;
  margin-top: 12px;
}
</style>
