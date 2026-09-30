<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import HButton from '@/components/hero/HButton.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSelect from '@/components/hero/HSelect.vue'
import HSpinner from '@/components/hero/HSpinner.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import HTooltip from '@/components/hero/HTooltip.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import ResourceRow from '@/components/transfer/ResourceRow.vue'
import { transferApi } from '@/api'
import type { ResourceItem, ResourceQuery, SourceKey, TransferSource } from '@/api/transfer'
import { useFeedback } from '@/composables/useFeedback'
import { type KindGroup, type SortKey, kindGroup, pixGroup, resourceKey, sortResources } from '@/utils/transferSort'
import { RefreshCw } from '@lucide/vue'

/**
 * 一部片的资源面板：每个来源各发一次请求，谁先回来谁先显示（盘搜要好几秒，
 * 不能让它拖住观影）。结果在前端合并，按与后端相同的「推荐」口径重排。
 */
const props = defineProps<{
  query: ResourceQuery
  sources: TransferSource[]
}>()

const router = useRouter()
const { message, dialog } = useFeedback()

type SourceState =
  | { status: 'loading' }
  | { status: 'ok'; items: ResourceItem[]; note?: string }
  | { status: 'error'; error: string }
  | { status: 'unavailable'; reason: string }
  | { status: 'off' }

const perSource = ref<Record<string, SourceState>>({})
const rowState = ref<Record<string, { tone: 'busy' | 'ok' | 'err'; text: string }>>({})

// 只看某个来源。必须声明在下面那个 immediate watch 之前：回调在 setup 里当场执行，
// 声明在后就是 TDZ 报错，loadAll 一次都没跑 —— 选完片直接显示「各来源都没有搜到资源」，
// 要点「重新搜索」才有结果
const onlySource = ref<SourceKey | ''>('')

// 过期请求的结果丢掉：换了一部片之后，上一部片慢吞吞回来的盘搜结果不能混进来
let generation = 0

async function loadSource(key: SourceKey, gen: number, refresh: boolean) {
  perSource.value[key] = { status: 'loading' }
  try {
    const d = await transferApi.resources(key, props.query, refresh)
    if (gen !== generation) return
    perSource.value[key] = d.unavailable
      ? { status: 'unavailable', reason: d.unavailable }
      : { status: 'ok', items: d.items ?? [], note: d.note }
  } catch (e) {
    if (gen !== generation) return
    perSource.value[key] = { status: 'error', error: e instanceof Error ? e.message : '搜索失败' }
  }
}

function loadAll(refresh = false) {
  const gen = ++generation
  perSource.value = {}
  for (const s of props.sources) {
    if (!s.enabled) perSource.value[s.key] = { status: 'off' }
    else if (s.reason) perSource.value[s.key] = { status: 'unavailable', reason: s.reason }
    else void loadSource(s.key, gen, refresh)
  }
}

watch(
  () => [props.query.tmdb_id, props.query.type, props.query.title] as const,
  () => {
    rowState.value = {}
    onlySource.value = ''
    loadAll()
  },
  { immediate: true },
)

// 来源在设置页里刚登录好 / 刚打开：只补请求这一个，不重搜全部
watch(
  () => props.sources.map((s) => `${s.key}:${s.enabled}:${s.reason}`).join('|'),
  () => {
    for (const s of props.sources) {
      const cur = perSource.value[s.key]
      if (!s.enabled) perSource.value[s.key] = { status: 'off' }
      else if (s.reason) perSource.value[s.key] = { status: 'unavailable', reason: s.reason }
      else if (!cur || cur.status === 'off' || cur.status === 'unavailable') void loadSource(s.key, generation, false)
    }
  },
)

const notes = computed(() => {
  const out: { key: string; text: string; err: boolean }[] = []
  for (const s of props.sources) {
    const st = perSource.value[s.key]
    if (st?.status === 'ok' && st.note) out.push({ key: s.key, text: `${s.label}：${st.note}`, err: false })
    if (st?.status === 'error') out.push({ key: s.key, text: `${s.label}搜索失败：${st.error}`, err: true })
  }
  return out
})

const loadingCount = computed(() => Object.values(perSource.value).filter((s) => s.status === 'loading').length)

const allItems = computed(() => {
  const out: ResourceItem[] = []
  for (const s of Object.values(perSource.value)) if (s.status === 'ok') out.push(...s.items)
  return out
})

// ---- 筛选 ----
const kind = ref<KindGroup | 'all'>('all')
const pix = ref<'all' | '4k' | '1080' | 'other'>('all')
const season = ref('')
const zhOnly = ref(false)
const sortKey = ref<SortKey>('recommend')
const showIrrelevant = ref(false)

const isTv = computed(() => props.query.type === 'tv')

const seasonOptions = computed(() => {
  const set = new Set<string>()
  for (const it of allItems.value) if (it.tags.season) set.add(it.tags.season)
  const list = [...set].sort((a, b) => (a === '全集' ? 1 : b === '全集' ? -1 : a.localeCompare(b)))
  return [{ label: '全部季', value: '' }, ...list.map((s) => ({ label: s, value: s }))]
})

/** 除「来源」以外的条件都过一遍，来源条上的计数也用它 */
function passFilters(it: ResourceItem) {
  if (kind.value !== 'all' && kindGroup(it) !== kind.value) return false
  if (pix.value !== 'all' && pixGroup(it.tags.pix) !== pix.value) return false
  if (season.value && it.tags.season !== season.value) return false
  if (zhOnly.value && !it.tags.zh) return false
  return true
}

const filtered = computed(() =>
  sortResources(
    allItems.value.filter((it) => (!onlySource.value || it.source === onlySource.value) && passFilters(it)),
    sortKey.value,
  ),
)
const relevantList = computed(() => filtered.value.filter((it) => it.relevant))
const irrelevantList = computed(() => filtered.value.filter((it) => !it.relevant))

function countOf(key: SourceKey) {
  const s = perSource.value[key]
  return s?.status === 'ok' ? s.items.filter((it) => it.relevant && passFilters(it)).length : 0
}

const kindOptions = computed(() => {
  const n = (g: KindGroup) => allItems.value.filter((it) => it.relevant && kindGroup(it) === g).length
  return [
    { label: '全部', value: 'all' as const },
    { label: `115 分享 ${n('share115')}`, value: 'share115' as const },
    { label: `磁力 / ed2k ${n('download')}`, value: 'download' as const },
    { label: `其他网盘 ${n('pan')}`, value: 'pan' as const },
  ]
})
const PIX_OPTIONS = [
  { label: '全部', value: 'all' as const },
  { label: '4K', value: '4k' as const },
  { label: '1080P', value: '1080' as const },
  { label: '其他', value: 'other' as const },
]
const SORT_OPTIONS = [
  { label: '推荐排序', value: 'recommend' as const },
  { label: '体积从大到小', value: 'size' as const },
  { label: '最新发布', value: 'time' as const },
  { label: '做种最多', value: 'seeds' as const },
]

function toggleSource(key: SourceKey) {
  onlySource.value = onlySource.value === key ? '' : key
}

function sourceHint(s: TransferSource): string {
  const st = perSource.value[s.key]
  if (!st) return ''
  switch (st.status) {
    case 'loading':
      return '搜索中…'
    case 'ok':
      return st.note || `${st.items.length} 条结果，${st.items.filter((it) => it.relevant).length} 条对得上片名`
    case 'error':
      return st.error
    case 'unavailable':
      return `${st.reason}，点击去来源设置`
    case 'off':
      return '已在来源设置里关闭，点击去打开'
  }
  return ''
}

function onSourceClick(s: TransferSource) {
  const st = perSource.value[s.key]
  if (st?.status === 'unavailable' || st?.status === 'off') {
    router.push({ query: { tab: 'sources', focus: s.key } })
    return
  }
  if (st?.status === 'error') {
    void loadSource(s.key, generation, true)
    return
  }
  toggleSource(s.key)
}

// ---- 提交 ----
async function act(it: ResourceItem) {
  const k = resourceKey(it)
  if (rowState.value[k]?.tone === 'busy') return
  if (it.action === 'open') {
    if (it.url) window.open(it.url, '_blank', 'noopener')
    rowState.value[k] = { tone: 'ok', text: it.code ? `已打开，提取码 ${it.code}` : '已打开原链接，请在对应网盘里手动转存' }
    return
  }
  if (it.action === 'unlock') {
    const pts = it.points
    const ok = await dialog.confirm({
      title: '解锁 RE0 资源',
      content: it.owned
        ? '这条资源已经解锁过，再次获取不扣积分。拿到 115 分享后直接转存到转存目录。'
        : `解锁这条资源会消耗 ${pts != null ? `${pts} 个` : '若干'}站内积分，扣了不退。拿到 115 分享后直接转存到转存目录；不是 115 分享的会把链接交给你手动处理。`,
      actions: [
        { label: '取消', value: false, variant: 'tertiary' },
        { label: it.owned ? '获取并转存' : '确认解锁', value: true, variant: 'primary' },
      ],
    })
    if (!ok) return
  } else if (it.submitted_at && rowState.value[k]?.tone !== 'ok') {
    const ok = await dialog.confirm({
      title: '这条链接提交过',
      content: `${new Date(it.submitted_at * 1000).toLocaleString()} 提交过同一条链接。再提交一次会在转存目录里多出一份，确定吗？`,
      actions: [
        { label: '取消', value: false, variant: 'tertiary' },
        { label: '再提交一次', value: true, variant: 'primary' },
      ],
    })
    if (!ok) return
  }
  rowState.value[k] = {
    tone: 'busy',
    text: it.action === 'transfer' ? '转存中…' : it.action === 'unlock' ? '解锁中…' : '提交 115 离线下载中…',
  }
  try {
    const r = await transferApi.submit({
      source: it.source,
      action: it.action,
      url: it.url,
      code: it.code,
      ref: it.ref,
      title: it.title,
      confirm: it.action === 'unlock',
    })
    if (r.open_url) window.open(r.open_url, '_blank', 'noopener')
    const text = r.message + (r.code ? `（提取码 ${r.code}）` : '')
    rowState.value[k] = { tone: 'ok', text }
    if (it.action === 'unlock') {
      it.owned = true
    } else {
      it.submitted_at = Math.floor(Date.now() / 1000)
    }
  } catch (e) {
    const text = e instanceof Error ? e.message : '提交失败'
    rowState.value[k] = { tone: 'err', text }
    message.error(text)
  }
}
</script>

<template>
  <div class="panel">
    <div class="src-bar">
      <HTooltip v-for="s in sources" :key="s.key" :content="sourceHint(s)">
        <button
          type="button"
          class="src"
          :class="[perSource[s.key]?.status, { on: onlySource === s.key }]"
          @click="onSourceClick(s)"
        >
          <HSpinner v-if="perSource[s.key]?.status === 'loading'" size="sm" />
          <span v-else class="dot" />
          {{ s.label }}
          <b v-if="perSource[s.key]?.status === 'ok'">{{ countOf(s.key) }}</b>
          <span v-else-if="perSource[s.key]?.status === 'error'" class="src-sub">失败，点击重试</span>
          <span v-else-if="perSource[s.key]?.status === 'unavailable'" class="src-sub">未配置</span>
          <span v-else-if="perSource[s.key]?.status === 'off'" class="src-sub">已关闭</span>
        </button>
      </HTooltip>
      <HButton variant="ghost" size="sm" class="refresh" :disabled="loadingCount > 0" @click="loadAll(true)">
        <template #icon><RefreshCw :size="13" /></template>
        重新搜索
      </HButton>
    </div>

    <!-- 触屏上没有悬停提示：来源的说明与失败原因要直接写出来 -->
    <p v-for="n in notes" :key="n.key" class="note" :class="{ err: n.err }">{{ n.text }}</p>

    <div class="filters">
      <HSegmented v-model="kind" size="sm" :options="kindOptions" aria-label="资源类型" />
      <HSegmented v-model="pix" size="sm" :options="PIX_OPTIONS" aria-label="分辨率" />
      <div v-if="isTv && seasonOptions.length > 1" class="w-season">
        <HSelect v-model="season" :options="seasonOptions" aria-label="季" />
      </div>
      <label class="zh">
        <HSwitch v-model="zhOnly" size="sm" aria-label="只看中字" />
        只看中字
      </label>
      <div class="w-sort">
        <HSelect v-model="sortKey" :options="SORT_OPTIONS" aria-label="排序" />
      </div>
    </div>

    <div class="list">
      <ResourceRow
        v-for="it in relevantList"
        :key="resourceKey(it)"
        :item="it"
        :state="rowState[resourceKey(it)]"
        @act="act(it)"
      />
      <div v-if="!relevantList.length && loadingCount" class="state"><HSpinner size="sm" /><span>各来源搜索中…</span></div>
      <EmptyState
        v-else-if="!relevantList.length"
        :text="allItems.length ? '没有符合筛选条件、且片名对得上的资源' : '各来源都没有搜到资源'"
      />

      <button v-if="irrelevantList.length" type="button" class="more" @click="showIrrelevant = !showIrrelevant">
        {{ showIrrelevant ? '收起' : '另有' }} {{ irrelevantList.length }} 条片名对不上的结果{{ showIrrelevant ? '' : '，展开看看' }}
      </button>
      <template v-if="showIrrelevant">
        <ResourceRow
          v-for="it in irrelevantList"
          :key="resourceKey(it)"
          :item="it"
          :state="rowState[resourceKey(it)]"
          @act="act(it)"
        />
      </template>
    </div>
  </div>
</template>

<style scoped>
.panel {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-width: 0;
}
.src-bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}
.src {
  all: unset;
  box-sizing: border-box;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 12px;
  border-radius: 999px;
  font-size: 13px;
  line-height: 20px;
  background: var(--default);
  color: var(--foreground);
  cursor: pointer;
  transition: background-color 0.15s, color 0.15s;
}
.src:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 1px;
}
.src b {
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}
.src.on {
  background: var(--accent);
  color: var(--accent-foreground);
}
.src .dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--success);
}
.src.error .dot {
  background: var(--danger);
}
.src.unavailable .dot,
.src.off .dot {
  background: var(--muted);
}
.src.unavailable,
.src.off {
  color: var(--muted);
}
.src-sub {
  font-size: 12px;
  color: var(--muted);
}
.src.error .src-sub {
  color: var(--danger);
}
.refresh {
  margin-left: auto;
}
.note {
  margin: -4px 0 0;
  font-size: 12px;
  color: var(--muted);
}
.note.err {
  color: var(--danger);
}
.filters {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 12px;
}
.w-season {
  width: 110px;
}
.w-sort {
  width: 140px;
  margin-left: auto;
}
.zh {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--foreground);
  cursor: pointer;
}
.list {
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.state {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 30px 0;
  font-size: 13px;
  color: var(--muted);
}
.more {
  all: unset;
  align-self: center;
  margin: 12px 0 4px;
  font-size: 12.5px;
  color: var(--accent);
  cursor: pointer;
}
.more:hover {
  text-decoration: underline;
}
@media (max-width: 720px) {
  .w-sort {
    margin-left: 0;
  }
  .refresh {
    margin-left: 0;
  }
}
</style>
