<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { RefreshCw, Trash2 } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import HButton from '@/components/hero/HButton.vue'
import HPagination from '@/components/hero/HPagination.vue'
import HPopconfirm from '@/components/hero/HPopconfirm.vue'
import HSearchField from '@/components/hero/HSearchField.vue'
import HSelect from '@/components/hero/HSelect.vue'
import HTabs from '@/components/hero/HTabs.vue'
import HTooltip from '@/components/hero/HTooltip.vue'
import { tasksApi } from '@/api'
import type { TaskJob } from '@/api/tasks'
import { toastError } from '@/composables/useFeedback'
import { useQueueStore } from '@/stores/queue'
import { JOB_KIND, JOB_SOURCE, JOB_STATUS } from '@/utils/jobStatus'
import HistoryRow from './HistoryRow.vue'

/**
 * 任务中心 · 历史页签（GET /tasks/history）。
 * 原来排在「当前状态 / 提前探测 / 进行中」三张卡片下面，数据一多要先滚过一屏才看得到；
 * 单独成页签后筛选栏就在页顶，列表按天分组、一行一个任务。
 * 地址栏 ?status= 只在打开时读一次（「进行中」页签里的「失败 N」链接直接跳到失败档）
 */
const emit = defineEmits<{ open: [id: number] }>()
const route = useRoute()
const queue = useQueueStore()

const STATUS_TABS = ['all', 'success', 'partial', 'failed', 'interrupted', 'canceled'] as const
type StatusTab = (typeof STATUS_TABS)[number]
const initial = String(route.query.status || '')

const rows = ref<TaskJob[]>([])
const total = ref(0)
const counts = ref<Record<string, number>>({})
const page = ref(1)
const size = ref(20)
const status = ref<StatusTab>((STATUS_TABS as readonly string[]).includes(initial) ? (initial as StatusTab) : 'all')
const kind = ref('')
const source = ref('')
const keyword = ref('')
const loading = ref(false)

const statusTabs = computed(() =>
  STATUS_TABS.map((k) => ({
    value: k,
    label: k === 'all' ? '全部' : JOB_STATUS[k].text,
    count: counts.value[k] ?? 0,
    countTone:
      k === 'failed' || k === 'interrupted'
        ? ('danger' as const)
        : k === 'partial'
          ? ('warning' as const)
          : ('accent' as const),
  })),
)
const KIND_OPTIONS = [{ label: '全部类型', value: '' }, ...Object.entries(JOB_KIND).map(([value, label]) => ({ label, value }))]
const SOURCE_OPTIONS = [
  { label: '全部来源', value: '' },
  ...Object.entries(JOB_SOURCE).map(([value, label]) => ({ label, value })),
]
const filtered = computed(() => !!(kind.value || source.value || keyword.value.trim()))

const WEEK = '日一二三四五六'
function dayLabel(d: Date) {
  const today = new Date()
  const diff = Math.round(
    (new Date(today.toDateString()).getTime() - new Date(d.toDateString()).getTime()) / 86_400_000,
  )
  if (diff === 0) return '今天'
  if (diff === 1) return '昨天'
  const md = `${d.getMonth() + 1} 月 ${d.getDate()} 日`
  const y = d.getFullYear() === today.getFullYear() ? '' : `${d.getFullYear()} 年 `
  return `${y}${md} · 周${WEEK[d.getDay()]}`
}

/** 按结束那天分组（后端已按时间倒序），组头写日期，行里只写时分 */
const groups = computed(() => {
  const out: { key: string; label: string; rows: TaskJob[] }[] = []
  for (const j of rows.value) {
    const d = new Date(j.finished_at || j.started_at || j.created_at)
    const key = Number.isNaN(d.getTime()) ? '?' : d.toDateString()
    let g = out[out.length - 1]
    if (!g || g.key !== key) {
      g = { key, label: key === '?' ? '时间未知' : dayLabel(d), rows: [] }
      out.push(g)
    }
    g.rows.push(j)
  }
  return out
})

async function load() {
  loading.value = true
  try {
    const d = await tasksApi.history({
      status: status.value,
      kind: kind.value,
      source: source.value,
      q: keyword.value.trim(),
      page: page.value,
      size: size.value,
    })
    rows.value = d.data ?? []
    total.value = d.total ?? 0
    counts.value = d.counts ?? {}
  } catch (e) {
    toastError(e, '读取任务历史失败')
  } finally {
    loading.value = false
  }
}

function refilter() {
  page.value = 1
  void load()
}

function onPage(p: number) {
  page.value = p
  void load()
}

function onSize(n: number) {
  size.value = n
  refilter()
}

function pickStatus(v: StatusTab) {
  status.value = v
  refilter()
}

function resetFilters() {
  kind.value = ''
  source.value = ''
  keyword.value = ''
  refilter()
}

async function clearAll() {
  await queue.clearFinished()
  refilter()
}

async function retry(j: TaskJob) {
  await queue.retry(j.id)
}

let offFinished: (() => void) | undefined
onMounted(() => {
  void load()
  // 有任务跑完，历史第一页就变了；翻到后面的页不打扰
  offFinished = queue.onFinished(() => {
    if (page.value === 1) void load()
  })
})
onUnmounted(() => offFinished?.())
</script>

<template>
  <SectionCard title="任务历史" hint="成功与取消的保留 7 天，部分失败、失败与中断保留 30 天；什么都没做的定时整理不留记录">
    <template #extra>
      <div class="extra">
        <HTooltip content="刷新">
          <HButton variant="ghost" icon-only :loading="loading" aria-label="刷新" @click="load">
            <RefreshCw />
          </HButton>
        </HTooltip>
        <HPopconfirm danger confirm-text="清理" :disabled="!counts.all" @confirm="clearAll">
          <HButton variant="danger-soft" :disabled="!counts.all">
            <template #icon><Trash2 /></template>
            清理已结束
          </HButton>
          <template #content>删除全部 {{ counts.all ?? 0 }} 条已结束任务的历史（排队中与执行中的不受影响）。整理记录不会被删。</template>
        </HPopconfirm>
      </div>
    </template>

    <HTabs :model-value="status" :items="statusTabs" variant="secondary" class="filters" @update:model-value="pickStatus" />

    <div class="toolbar">
      <div class="sel">
        <HSelect v-model="kind" :options="KIND_OPTIONS" aria-label="任务类型" @update:model-value="refilter" />
      </div>
      <div class="sel">
        <HSelect v-model="source" :options="SOURCE_OPTIONS" aria-label="提交来源" @update:model-value="refilter" />
      </div>
      <HSearchField v-model="keyword" class="kw" placeholder="标题 / 结果说明" @search="refilter" />
      <HButton v-if="filtered" size="sm" variant="ghost" @click="resetFilters">清除筛选</HButton>
      <span class="grow" />
      <span v-if="total" class="dim">共 {{ total }} 条</span>
    </div>

    <div class="list-wrap" :class="{ 'is-loading': loading }">
      <EmptyState
        v-if="!rows.length && !loading"
        :text="filtered || status !== 'all' ? '没有符合条件的任务' : '还没有已结束的任务'"
      />
      <section v-for="g in groups" :key="g.key" class="group">
        <h3 class="day">
          {{ g.label }}<span class="day-n">{{ g.rows.length }}</span>
        </h3>
        <ul class="list">
          <HistoryRow v-for="j in g.rows" :key="j.id" :job="j" clock @open="emit('open', $event)" @retry="retry" />
        </ul>
      </section>
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
.extra {
  display: flex;
  align-items: center;
  gap: 6px;
}
.filters {
  margin-bottom: 12px;
}
.toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
  flex-wrap: wrap;
}
.sel {
  width: 132px;
}
.kw {
  width: 240px;
  max-width: 100%;
}
.grow {
  flex: 1;
}
.dim {
  font-size: 12px;
  color: var(--muted);
}
.list-wrap {
  transition: opacity 150ms ease;
}
.list-wrap.is-loading {
  opacity: 0.55;
  pointer-events: none;
}
.group + .group {
  margin-top: 6px;
}
.day {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 10px 0 2px;
  padding: 4px 4px;
  font-size: 12.5px;
  font-weight: 600;
  color: var(--muted);
}
.day-n {
  font-weight: 400;
  font-size: 12px;
}
.day-n::before {
  content: '· ';
}
.list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.pager {
  margin-top: 12px;
}

@media (max-width: 720px) {
  .sel {
    flex: 1;
    width: auto;
  }
  .kw {
    width: 100%;
  }
}
</style>
