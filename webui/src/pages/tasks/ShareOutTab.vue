<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ExternalLink, ListTree, RefreshCw, RotateCcw, Trash2 } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import PosterImage from '@/components/PosterImage.vue'
import MediaTypeChip from '@/components/MediaTypeChip.vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HPagination from '@/components/hero/HPagination.vue'
import HTabs from '@/components/hero/HTabs.vue'
import HTooltip from '@/components/hero/HTooltip.vue'
import { resourcesApi } from '@/api'
import type { ShareOutRecord } from '@/api/resources'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { fullTime, relTime } from '@/utils/time'

/**
 * 任务中心 · 分享记录页签（GET /shareout/records）：贴链接转存的分享发到 RE0 / 癫影的结果，一站一行。
 * 只读本地记账，不请求两个站；「打开」的地址也是按记账拼的（RE0 有 slug 直达那条分享，否则打开这部片在站上的页面）。
 * 失败 / 没分享的可以手动重试一次（后台立刻发，不等定时那一轮）；再失败就停在失败，不再自动重试。
 * 没分享的也放开：站点拒收的原因可能已经不在了，原因还在的重试也只是再记一次跳过
 * 「删除」在后端只是隐藏：那一行是防重发的记账，删掉的失败记录也不再自动重试
 */
const emit = defineEmits<{ changed: [] }>()
const router = useRouter()
const { message, dialog } = useFeedback()

const STATUS_TABS = ['all', 'done', 'failed', 'skipped'] as const
type StatusTab = (typeof STATUS_TABS)[number]
const STATUS_TEXT: Record<ShareOutRecord['status'], string> = { done: '已分享', failed: '失败', skipped: '没分享' }
const STATUS_COLOR: Record<ShareOutRecord['status'], 'success' | 'danger' | 'default'> = {
  done: 'success',
  failed: 'danger',
  skipped: 'default',
}

const rows = ref<ShareOutRecord[]>([])
const total = ref(0)
const counts = ref<Record<string, number>>({})
const page = ref(1)
const size = ref(20)
const status = ref<StatusTab>('all')
const loading = ref(false)
const retrying = ref(new Set<number>())

const statusTabs = computed(() =>
  STATUS_TABS.map((k) => ({
    value: k,
    label: k === 'all' ? '全部' : STATUS_TEXT[k],
    count: counts.value[k] ?? 0,
    countTone: k === 'failed' ? ('danger' as const) : ('accent' as const),
  })),
)

async function load() {
  loading.value = true
  try {
    const d = await resourcesApi.shareOutRecords({
      status: status.value === 'all' ? undefined : status.value,
      page: page.value,
      size: size.value,
    })
    rows.value = d.data ?? []
    total.value = d.total
    counts.value = d.counts ?? {}
  } catch (e) {
    toastError(e, '读取分享记录失败')
  } finally {
    loading.value = false
  }
}

function pickStatus(v: string | number) {
  status.value = v as StatusTab
  page.value = 1
  void load()
}
function onPage(p: number) {
  page.value = p
  void load()
}
function onSize(s: number) {
  size.value = s
  page.value = 1
  void load()
}

const siteName = (s: ShareOutRecord['site']) => (s === 'dian' ? '癫影' : 'RE0')

function titleOf(r: ShareOutRecord) {
  return (r.title || r.share_name || '（没有片名）') + (r.year ? ` (${r.year})` : '')
}

/** 状态后面那句说明：失败写原因与下次自动重试，发成写奖励 */
function detailOf(r: ShareOutRecord) {
  if (r.status === 'done') return r.points > 0 ? `首次发布奖励 ${r.points} 积分` : ''
  if (r.status === 'failed') {
    const next = r.next_retry ? `，${relTime(r.next_retry)}自动再试` : '，不再自动重试'
    return `${r.message || '未说明原因'}（已试 ${r.attempts} 次${next}）`
  }
  return r.message
}

async function retry(r: ShareOutRecord) {
  const s = new Set(retrying.value)
  s.add(r.id)
  retrying.value = s
  try {
    const d = await resourcesApi.shareOutRetry(r.id)
    message.success(d.message)
    // 后台发：浏览器通道可能要一两分钟，先刷一次，之后用户自己点刷新
    setTimeout(() => void load().then(() => emit('changed')), 3000)
  } catch (e) {
    toastError(e, '重试失败')
  } finally {
    const t = new Set(retrying.value)
    t.delete(r.id)
    retrying.value = t
  }
}

const clearing = ref(false)

async function doDelete(body: { ids?: number[]; status?: string }) {
  try {
    const d = await resourcesApi.shareOutDelete(body)
    message.success(d.message)
    if (rows.value.length <= (body.ids?.length ?? rows.value.length) && page.value > 1) page.value--
    await load()
    emit('changed')
  } catch (e) {
    toastError(e, '删除失败')
  }
}

function removeOne(r: ShareOutRecord) {
  void doDelete({ ids: [r.id] })
}

async function clearAll() {
  const n = status.value === 'all' ? counts.value.all ?? 0 : counts.value[status.value] ?? 0
  if (!n) return
  const what = status.value === 'all' ? '全部' : `「${STATUS_TEXT[status.value]}」的`
  const ok = await dialog.confirm({
    title: '清空分享记录',
    content: `删除${what} ${n} 条分享记录。只删本站的记录，站上已经发出去的分享不受影响。`,
    tone: 'warning',
    actions: [
      { label: '取消', value: false, variant: 'tertiary' },
      { label: `删除 ${n} 条`, value: true, variant: 'danger' },
    ],
  })
  if (!ok) return
  clearing.value = true
  try {
    page.value = 1
    await doDelete({ status: status.value })
  } finally {
    clearing.value = false
  }
}

function openRecords(r: ShareOutRecord) {
  void router.push({ query: { tab: 'records', link_id: String(r.link_id) } })
}

onMounted(load)
defineExpose({ reload: load })
</script>

<template>
  <SectionCard
    title="分享记录"
    hint="贴链接转存的 115 分享，整理认出是哪一部之后发到 RE0 / 癫影的结果，一站一行。开关在「发现与订阅 → 设置」的 RE0 / 癫影里"
  >
    <template #extra>
      <HButton v-if="rows.length" size="sm" variant="ghost" :loading="clearing" @click="clearAll">
        <template #icon><Trash2 /></template>
        清空
      </HButton>
      <HTooltip content="刷新">
        <HButton variant="ghost" icon-only :loading="loading" aria-label="刷新" @click="load">
          <RefreshCw />
        </HButton>
      </HTooltip>
    </template>

    <HTabs :model-value="status" :items="statusTabs" variant="secondary" class="filters" @update:model-value="pickStatus" />

    <div class="list-wrap" :class="{ 'is-loading': loading }">
      <EmptyState
        v-if="!rows.length && !loading"
        :text="status !== 'all' ? '没有这个状态的分享' : '还没有分享过。在 RE0 / 癫影的设置里打开「转存后分享」，之后贴链接转存的分享整理完会发到站上'"
      />
      <ul class="list">
        <li v-for="r in rows" :key="r.id" class="row">
          <PosterImage
            class="poster"
            :src="r.poster_path ? resourcesApi.tmdbImageUrl(r.poster_path, 'w92') : null"
            :alt="r.title"
            :icon-size="18"
          />
          <div class="main">
            <div class="head">
              <HChip :color="STATUS_COLOR[r.status]">{{ STATUS_TEXT[r.status] }}</HChip>
              <span class="site">{{ siteName(r.site) }}</span>
              <span class="title" :title="titleOf(r)">{{ titleOf(r) }}</span>
              <MediaTypeChip :type="r.media_type" />
            </div>
            <div v-if="detailOf(r)" class="detail" :class="r.status">{{ detailOf(r) }}</div>
            <div class="meta">
              <span :title="fullTime(r.updated_at)">{{ relTime(r.updated_at) }}</span>
              <span v-if="r.share_name && r.share_name !== r.title" class="share-name" :title="r.share_name">
                分享名 {{ r.share_name }}
              </span>
              <span v-if="r.source">经 {{ r.source }} 提交</span>
            </div>
          </div>
          <div class="actions">
            <HButton
              v-if="r.can_retry"
              size="sm"
              variant="tertiary"
              :loading="retrying.has(r.id)"
              @click="retry(r)"
            >
              <template #icon><RotateCcw /></template>
              重试
            </HButton>
            <a v-if="r.site_url" :href="r.site_url" target="_blank" rel="noopener noreferrer" class="open">
              <ExternalLink :size="14" />在{{ siteName(r.site) }}打开
            </a>
            <HTooltip content="这条链接的整理记录">
              <HButton size="sm" variant="ghost" icon-only aria-label="整理记录" @click="openRecords(r)">
                <ListTree />
              </HButton>
            </HTooltip>
            <HTooltip content="删除这条记录（站上的分享不受影响）">
              <HButton size="sm" variant="ghost" icon-only aria-label="删除" @click="removeOne(r)">
                <Trash2 />
              </HButton>
            </HTooltip>
          </div>
        </li>
      </ul>
    </div>

    <HPagination
      v-if="total > size"
      :page="page"
      :page-size="size"
      :total="total"
      class="pager"
      @update:page="onPage"
      @update:page-size="onSize"
    />
  </SectionCard>
</template>

<style scoped>
.filters {
  margin-bottom: 12px;
}
.list-wrap.is-loading {
  opacity: 0.6;
}
.list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.row {
  display: flex;
  gap: 12px;
  align-items: flex-start;
  padding: 10px 0;
  border-bottom: 1px solid var(--separator);
}
.row:last-child {
  border-bottom: none;
}
.poster {
  width: 40px;
  height: 60px;
  flex-shrink: 0;
  border-radius: var(--r-sm);
}
.main {
  flex: 1;
  min-width: 0;
}
.head {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.site {
  font-size: 12px;
  color: var(--muted);
  flex-shrink: 0;
}
.title {
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  min-width: 0;
}
.detail {
  margin-top: 4px;
  font-size: 13px;
  color: var(--muted);
  word-break: break-all;
}
.detail.done {
  color: var(--success);
}
.detail.failed {
  color: var(--danger);
}
.meta {
  margin-top: 4px;
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
  font-size: 12px;
  color: var(--muted);
}
.share-name {
  max-width: 320px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.actions {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}
.open {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 13px;
  color: var(--accent);
  text-decoration: none;
  white-space: nowrap;
}
.open:hover {
  text-decoration: underline;
}
.pager {
  margin-top: 12px;
}
@media (max-width: 720px) {
  .row {
    flex-wrap: wrap;
  }
  .actions {
    width: 100%;
    justify-content: flex-end;
  }
}
</style>
