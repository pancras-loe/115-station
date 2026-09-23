<script setup lang="ts">
/**
 * 下载记录：提交过的离线（磁力/ed2k/HTTP）与 115 分享转存链接一条一行，
 * 内容被整理入库后同一行上带出识别结果（片名 / 年份 / TMDB / 落库目录）。
 * 全部数据来自本地库，不请求 115。
 */
import { onMounted, ref } from 'vue'
import { NButton, NInput, NPagination, NPopconfirm, NSelect, NSpin, NTag } from 'naive-ui'
import { Search, Trash2 } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import SourceLink from '@/components/ui/SourceLink.vue'
import { resourcesApi, transferApi } from '@/api'
import type { DownloadLink } from '@/api/transfer'
import { toastError, useFeedback } from '@/composables/useFeedback'

const { message } = useFeedback()

const rows = ref<DownloadLink[]>([])
const total = ref(0)
const page = ref(1)
const size = ref(20)
const status = ref('all')
const keyword = ref('')
const loading = ref(false)

const STATUS_OPTIONS = [
  { label: '全部', value: 'all' },
  { label: '已识别入库', value: 'organized' },
  { label: '等待整理', value: 'pending' },
  { label: '需要处理（下载或识别失败）', value: 'failed' },
]

const KIND_TEXT: Record<string, string> = {
  magnet: '磁力',
  ed2k: 'ed2k',
  http: 'HTTP',
  ftp: 'FTP',
  share: '115 分享',
}

/** 一行的总体状态：整理结果优先，没整理就看下载状态 */
function stateOf(r: DownloadLink): { text: string; type: 'success' | 'info' | 'warning' | 'error' | 'default' } {
  switch (r.organize_status) {
    case 'success':
      return { text: '已入库', type: 'success' }
    case 'exists':
      return { text: '已存在', type: 'info' }
    case 'awaiting':
      return { text: '待确认', type: 'warning' }
    case 'unrecognized':
      return { text: '未识别', type: 'warning' }
    case 'failed':
      return { text: '整理失败', type: 'error' }
  }
  switch (r.status) {
    case 'failed':
      return { text: '下载失败', type: 'error' }
    case 'done':
      return { text: '待整理', type: 'info' }
    case 'downloading':
      return { text: '下载中', type: 'info' }
    default:
      return { text: '已提交', type: 'default' }
  }
}

async function load() {
  loading.value = true
  try {
    const d = await transferApi.downloadLinks({
      status: status.value,
      q: keyword.value.trim(),
      page: page.value,
      size: size.value,
    })
    rows.value = d.data ?? []
    total.value = d.total ?? 0
  } catch (e) {
    toastError(e, '读取下载记录失败')
  } finally {
    loading.value = false
  }
}

function refilter() {
  page.value = 1
  load()
}

onMounted(load)

async function removeRow(id: number) {
  try {
    await transferApi.deleteDownloadLink(id)
    message.success('已删除')
    load()
  } catch (e) {
    toastError(e, '删除失败')
  }
}

async function clearAll() {
  try {
    const d = await transferApi.clearDownloadLinks(status.value)
    message.success(d.message ?? `已清空 ${d.removed} 条`)
    page.value = 1
    load()
  } catch (e) {
    toastError(e, '清空失败')
  }
}

function humanTime(s: string | null) {
  if (!s) return ''
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return s
  return d.toLocaleString('zh-CN', { hour12: false })
}
</script>

<template>
  <SectionCard
    title="下载记录"
    hint="提交过的离线 / 分享链接，整理入库后带出识别结果。数据全部取自本地库，不请求 115"
  >
    <div class="toolbar">
      <NSelect v-model:value="status" :options="STATUS_OPTIONS" class="filter" @update:value="refilter" />
      <NInput
        v-model:value="keyword"
        placeholder="搜索链接、任务名或片名"
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
      <EmptyState
        v-if="!rows.length && !loading"
        text="还没有下载记录，提交一条磁力或 115 分享链接就会出现"
      />

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
              <NTag size="small" :bordered="false" :type="stateOf(r).type">{{ stateOf(r).text }}</NTag>
              <b v-if="r.title" class="title">{{ r.title }}</b>
              <span v-else class="dim">{{ r.name || KIND_TEXT[r.kind] || r.kind }}</span>
              <span v-if="r.year" class="dim">{{ r.year }}</span>
              <a
                v-if="r.tmdb_id"
                class="dim link"
                :href="`https://www.themoviedb.org/${r.media_type}/${r.tmdb_id}`"
                target="_blank"
                rel="noopener"
                >tmdb={{ r.tmdb_id }}</a
              >
              <span v-if="r.category" class="dim">{{ r.category }}</span>
            </div>

            <SourceLink class="from" :link="r.url" :kind="r.kind" />

            <div class="meta">
              <span>{{ humanTime(r.created_at) }}</span>
              <span v-if="r.source">来源 {{ r.source }}</span>
              <span v-if="r.title && r.name" class="ellipsis">原名 {{ r.name }}</span>
              <span v-if="r.target_dir">→ {{ r.target_dir }}</span>
            </div>

            <p v-if="r.note" class="msg">{{ r.note }}</p>
          </div>

          <div class="ops">
            <NPopconfirm @positive-click="void removeRow(r.id)">
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
      <NPagination v-model:page="page" :page-size="size" :item-count="total" @update:page="load" />
    </div>
  </SectionCard>
</template>

<style scoped>
.toolbar {
  display: flex;
  gap: 8px;
  margin-bottom: 12px;
  flex-wrap: wrap;
}
.filter {
  width: 240px;
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
  margin-top: 4px;
  max-width: 560px;
}
.meta {
  margin-top: 4px;
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
  font-size: 12px;
  color: var(--c-text-3);
}
.ellipsis {
  max-width: 280px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.msg {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--c-text-3);
}
.ops {
  display: flex;
  align-items: flex-start;
}
.pager {
  margin-top: 12px;
  display: flex;
  justify-content: flex-end;
}
</style>
