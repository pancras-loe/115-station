<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import type { Component } from 'vue'
import { ChevronRight, DatabaseBackup, FileWarning, Trash2, Zap } from '@lucide/vue'
import HSkeleton from '@/components/hero/HSkeleton.vue'
import { syncApi, tasksApi } from '@/api'
import type { DeepDeleteRecord, OrphanReport } from '@/api/sync'
import type { TaskJob } from '@/api/tasks'
import type { FullSetting } from './fullSetting'
import type { DeepDelSetting } from './deepDelSetting'
import type { StrmRuns } from './useStrmRuns'
import { useQueueStore } from '@/stores/queue'
import { fullTime, relTime } from '@/utils/time'
import { num } from '@/utils/format'

/**
 * Strm 管理页顶部的一行状态条：四件事各一段，「现在好不好」一眼看到，点链接跳到对应页签。
 * 只看不做：「立即同步」「开始全量」放回各自页签（维护者嫌状态条上放执行按钮太挤）。
 *
 * 数据全是已有的只读接口（增量状态 / 失效 STRM / 任务历史 / 删除记录），零 115 请求。
 * 增量状态 15 秒拉一次（后端只读内存），其余在相关任务跑完时刷新。
 */
const props = defineProps<{ full: FullSetting; runs: StrmRuns; deepdel: DeepDelSetting }>()
const emit = defineEmits<{ goto: [tab: string, anchor?: string] }>()

const queue = useQueueStore()

// ---- 数据 ----
const incr = ref<syncApi.IncrStatus | null>(null)
const orphan = ref<OrphanReport | null>(null)
const lastFull = ref<TaskJob | null | undefined>(undefined)
const lastDel = ref<DeepDeleteRecord | null | undefined>(undefined)
const nextFull = ref('')

async function loadIncr() {
  try {
    incr.value = await syncApi.incrStatus()
  } catch {
    // 状态读不到就维持上一次的，不打扰
  }
}
async function loadOrphan() {
  try {
    orphan.value = await syncApi.orphans()
  } catch {
    orphan.value = null
  }
}
async function loadLastFull() {
  try {
    lastFull.value = (await tasksApi.history({ kind: 'full', page: 1, size: 1 })).data[0] ?? null
  } catch {
    lastFull.value = null
  }
}
async function loadLastDel() {
  try {
    lastDel.value = (await syncApi.deepDeleteRecords(1, 1)).data[0] ?? null
  } catch {
    lastDel.value = null
  }
}

let timer: number | undefined
onMounted(() => {
  void loadIncr()
  void loadOrphan()
  void loadLastFull()
  void loadLastDel()
  timer = window.setInterval(() => {
    if (!document.hidden) void loadIncr()
  }, 15_000)
})
const offFinished = queue.onFinished((j) => {
  if (j.kind === 'full') {
    void loadOrphan()
    void loadLastFull()
  }
  if (j.kind === 'incr') void loadIncr()
})
onUnmounted(() => {
  clearInterval(timer)
  offFinished()
})

// 定时全量的下一次执行时间：只看已保存的配置（没保存的 cron 还不会生效）
const savedCfg = computed(() => props.full.saved.value)
watch(
  () => [savedCfg.value.detect_orphans, savedCfg.value.cron_enabled, savedCfg.value.cron] as const,
  async ([detect, on, cron]) => {
    nextFull.value = ''
    if (!detect || !on || !cron?.trim()) return
    try {
      nextFull.value = (await syncApi.cronPreview(cron)).next?.[0] ?? ''
    } catch {
      // cron 写错了就不显示下一次
    }
  },
  { immediate: true },
)

// ---- 四块的状态 ----
type Tone = 'ok' | 'warn' | 'danger' | 'off' | 'loading'
interface Tile {
  key: string
  title: string
  icon: Component
  tone: Tone
  badge: string
  value: string
  unit?: string
  lines: { text: string; title?: string; tone?: 'warn' | 'danger' }[]
  /** 状态条上那一行：有问题时先说问题，正常时给最有用的一两个数字 */
  short: string
}
const LOADING = { tone: 'loading' as const, badge: '', value: '', lines: [], short: '' }

const incrTile = computed<Tile>(() => {
  const s = incr.value
  const base = { key: 'incr', title: '增量同步', icon: Zap }
  if (!s) return { ...base, ...LOADING }
  const off = s.interval_sec <= 0
  const r = s.last_round
  let tone: Tone = 'ok'
  let badge = '运行正常'
  if (r.error) [tone, badge] = ['danger', '上一轮出错']
  else if (!s.life_gate.ok) [tone, badge] = ['warn', '事件开关异常']
  else if (s.stall.rounds > 0) [tone, badge] = ['warn', `连续 ${s.stall.rounds} 轮未消费`]
  else if (off) [tone, badge] = ['off', '独立轮询已关闭']

  const lines: Tile['lines'] = []
  if (r.error) lines.push({ text: r.error, tone: 'danger' })
  else if (!s.life_gate.ok) lines.push({ text: s.life_gate.message, tone: 'warn' })
  if (r.at) {
    const sum = r.summary
    const bits = [`上一轮 ${relTime(r.at)}`]
    if (sum && !r.error) {
      if (sum.strm_created) bits.push(`新增 ${sum.strm_created}`)
      if (sum.deleted) bits.push(`清理 ${sum.deleted}`)
      if (sum.moved) bits.push(`移动 ${sum.moved}`)
      if (bits.length === 1) bits.push('无变化')
    }
    lines.push({ text: bits.join(' · '), title: fullTime(r.at) })
  } else {
    lines.push({ text: '还没跑过' })
  }
  if (s.pending_events > 0) lines.push({ text: `积压 ${s.pending_events} 条事件`, tone: 'warn' })
  return {
    ...base,
    tone,
    badge,
    value: off ? '跟随整理' : `${s.interval_sec}`,
    unit: off ? undefined : '秒 / 轮',
    lines,
    // 一段只有 ~250px：正常时只说上一轮是什么时候（间隔在页签里看），有问题先说问题
    short: [
      tone === 'warn' || tone === 'danger' ? badge : off ? '跟随整理执行' : '',
      r.at ? `上一轮 ${relTime(r.at)}` : '还没跑过',
      s.pending_events > 0 ? `积压 ${s.pending_events}` : '',
    ]
      .filter(Boolean)
      .join(' · '),
  }
})

const JOB_STATUS: Record<string, string> = {
  success: '成功',
  failed: '失败',
  partial: '部分失败',
  cancelled: '已取消',
  interrupted: '已中断',
}
const fullTile = computed<Tile>(() => {
  const base = { key: 'full', title: '全量同步', icon: DatabaseBackup }
  if (lastFull.value === undefined) return { ...base, ...LOADING }
  const running = queue.activeManualOf('full') || props.runs.runningFull.value
  const j = lastFull.value
  let tone: Tone = 'ok'
  let badge = props.runs.fullMode.value === 'fast' ? '快速模式' : '标准模式'
  if (running) [tone, badge] = ['ok', '执行中']
  else if (j && (j.status === 'failed' || j.status === 'interrupted')) [tone, badge] = ['danger', '上次失败']
  else if (!j) tone = 'off'

  const lines: Tile['lines'] = []
  if (j) {
    lines.push({
      text: `${JOB_STATUS[j.status] ?? j.status}${j.message ? ` · ${j.message}` : ''}`,
      title: j.message,
      tone: j.status === 'failed' || j.status === 'interrupted' ? 'danger' : undefined,
    })
  }
  const cfg = savedCfg.value
  if (cfg.detect_orphans && cfg.cron_enabled && cfg.cron) {
    lines.push({ text: nextFull.value ? `定时 · 下次 ${relNext(nextFull.value)}` : `定时 · ${cfg.cron}`, title: cfg.cron })
  } else {
    lines.push({ text: '未开启定时全量' })
  }
  const at = j ? j.finished_at || j.started_at || j.created_at : ''
  return {
    ...base,
    tone,
    badge,
    value: j ? relTime(at) : '从未执行',
    lines,
    short: running
      ? '执行中'
      : j
        ? `上次 ${relTime(at)} · ${JOB_STATUS[j.status] ?? j.status}`
        : '从未执行',
  }
})

/**
 * cron 预览给的是「01-02 15:04」（routes.go 的 /sync/cron-preview，没有年份），挑个短写法：
 * 今天 / 明天 HH:mm，更远的原样给。跨年时补出来的日期会落在过去，按明年算
 */
function relNext(s: string): string {
  const m = /^(\d{2})-(\d{2}) (\d{2}):(\d{2})$/.exec(s.trim())
  if (!m) return s
  const now = new Date()
  const d = new Date(now.getFullYear(), +m[1] - 1, +m[2], +m[3], +m[4])
  if (d.getTime() < now.getTime() - 86_400_000) d.setFullYear(d.getFullYear() + 1)
  const hm = `${m[3]}:${m[4]}`
  const day = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime()
  const diff = Math.round((day(d) - day(new Date())) / 86_400_000)
  if (diff === 0) return `今天 ${hm}`
  if (diff === 1) return `明天 ${hm}`
  return `${d.getMonth() + 1}-${String(d.getDate()).padStart(2, '0')} ${hm}`
}

const orphanTile = computed<Tile>(() => {
  const base = { key: 'orphan', title: '失效 STRM', icon: FileWarning }
  const o = orphan.value
  const detect = savedCfg.value.detect_orphans
  if (!o && detect) return { ...base, ...LOADING }
  const total = o?.total ?? 0
  if (!detect && total === 0) {
    return {
      ...base,
      tone: 'off',
      badge: '检测未开启',
      value: '—',
      lines: [{ text: '在「全量同步」里打开失效检测' }],
      short: '检测未开启',
    }
  }
  const high = (o?.ratio ?? 0) > 0.2
  return {
    ...base,
    tone: total === 0 ? 'ok' : high ? 'danger' : 'warn',
    badge: total === 0 ? '没有失效' : high ? '比例异常' : '待清理',
    value: num(total),
    unit: '个',
    lines: [
      { text: `台账共 ${num(o?.ledger_total ?? 0)} 条`, tone: high ? 'danger' : undefined },
      { text: '每次全量同步结束时刷新' },
    ],
    short: total === 0 ? '没有失效' : `${num(total)} 个${high ? '（比例异常）' : '待清理'}`,
  }
})

const DEL_STATUS: Record<string, string> = { done: '已删', dry_run: '历史预演', rejected: '拦下', failed: '失败' }
const delTile = computed<Tile>(() => {
  const base = { key: 'deepdel', title: '深度删除', icon: Trash2 }
  if (props.deepdel.loading.value) return { ...base, ...LOADING }
  const on = !!props.deepdel.saved.value.enabled
  const r = lastDel.value
  const lines: Tile['lines'] = []
  if (r) {
    lines.push({
      text: `${relTime(r.created_at)} · ${DEL_STATUS[r.status] ?? r.status} ${r.title || ''}`.trim(),
      title: r.message,
      tone: r.status === 'failed' ? 'danger' : r.status === 'rejected' ? 'warn' : undefined,
    })
  } else if (r === null) {
    lines.push({ text: '暂无删除记录' })
  }
  lines.push({ text: on ? 'Emby 删除后联动删网盘源文件' : '开启后 Emby 删片会联动删网盘' })
  return {
    ...base,
    tone: on ? 'ok' : 'off',
    badge: on ? '已开启' : '未开启',
    value: on ? '联动中' : '已关闭',
    lines,
    // 最近一次删除放悬停提示里，这一行只说开没开
    short: on ? '已开启' : '未开启',
  }
})

const tiles = computed(() => [incrTile.value, fullTile.value, orphanTile.value, delTile.value])
</script>

<template>
  <!-- 一条细状态条（约 48px 高）：四件事各一段，圆点颜色 = 好不好，悬停看细节。
       原来是四张大卡片，占掉 200px 把下面的配置挤到折叠线以下，2026-09-30 收成一行 -->
  <div class="card card--default strip">
    <div
      v-for="t in tiles"
      :key="t.key"
      class="seg"
      :class="`tone-${t.tone}`"
      :title="t.lines.map((l) => l.title || l.text).join('\n')"
    >
      <span class="seg-dot" />
      <span class="seg-title">{{ t.title }}</span>
      <HSkeleton v-if="t.tone === 'loading'" width="90px" height="10px" radius="999px" />
      <span v-else class="seg-text">{{ t.short }}</span>

      <span class="seg-act">
        <!-- 状态条上不放执行按钮（维护者要求）：「立即同步」「开始全量」在各自页签里 -->
        <button v-if="t.key === 'incr'" type="button" class="seg-link" @click="emit('goto', 'incr')">
          详情<ChevronRight :size="13" />
        </button>

        <button v-else-if="t.key === 'full'" type="button" class="seg-link" @click="emit('goto', 'full')">
          设置<ChevronRight :size="13" />
        </button>

        <button
          v-else-if="t.key === 'orphan' && (orphan?.total ?? 0) > 0"
          type="button"
          class="seg-link"
          @click="emit('goto', 'full', 'orphans')"
        >
          清理<ChevronRight :size="13" />
        </button>

        <button v-else-if="t.key === 'deepdel'" type="button" class="seg-link" @click="emit('goto', 'deepdel')">
          记录<ChevronRight :size="13" />
        </button>
      </span>
    </div>
  </div>
</template>

<style scoped>
.strip {
  flex-direction: row;
  align-items: stretch;
  gap: 0;
  padding: 0;
  overflow: hidden;
}
.seg {
  --tone: var(--success);
  flex: 1 1 0;
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 8px;
  height: 48px;
  padding: 0 8px 0 16px;
  font-size: 13px;
}
.seg + .seg {
  border-left: 1px solid var(--separator);
}
.tone-warn {
  --tone: var(--warning);
}
.tone-danger {
  --tone: var(--danger);
}
.tone-off,
.tone-loading {
  --tone: var(--border-tertiary);
}
.seg-dot {
  width: 8px;
  height: 8px;
  flex-shrink: 0;
  border-radius: 999px;
  background: var(--tone);
  box-shadow: 0 0 0 3px color-mix(in oklab, var(--tone) 22%, transparent);
}
.seg-title {
  flex-shrink: 0;
  font-weight: 600;
  color: var(--foreground);
  white-space: nowrap;
}
.seg-text {
  min-width: 0;
  flex: 1 1 auto;
  color: var(--muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  font-variant-numeric: tabular-nums;
}
.tone-warn .seg-text {
  color: var(--warning-soft-foreground);
}
.tone-danger .seg-text {
  color: var(--danger);
}
.seg-act {
  margin-left: auto;
  flex-shrink: 0;
  display: flex;
}
.seg-link {
  display: inline-flex;
  align-items: center;
  height: 28px;
  padding: 0 8px;
  border: 0;
  border-radius: 8px;
  background: none;
  font: inherit;
  font-size: 12.5px;
  color: var(--muted);
  cursor: pointer;
  transition:
    background-color 150ms ease,
    color 150ms ease;
}
.seg-link:hover {
  background: var(--default);
  color: var(--accent);
}

/* 放不下一行时 2×2：每段仍是一行字，总高约 96px */
@media (max-width: 1180px) {
  .strip {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .seg + .seg {
    border-left: 0;
  }
  .seg:nth-child(even) {
    border-left: 1px solid var(--separator);
  }
  .seg:nth-child(n + 3) {
    border-top: 1px solid var(--separator);
  }
}
@media (max-width: 600px) {
  .strip {
    grid-template-columns: 1fr;
  }
  .seg:nth-child(even) {
    border-left: 0;
  }
  .seg:nth-child(n + 2) {
    border-top: 1px solid var(--separator);
  }
}
</style>
