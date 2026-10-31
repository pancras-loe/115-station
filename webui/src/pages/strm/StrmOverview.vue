<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import type { Component } from 'vue'
import { DatabaseBackup, FileWarning, Link2, MonitorPlay, Trash2, Zap } from '@lucide/vue'
import { useRouter } from 'vue-router'
import StatusStrip from '@/components/ui/StatusStrip.vue'
import type { StripItem, StripTone } from '@/components/ui/StatusStrip.vue'
import { playbackApi, shareStrmApi, syncApi, tasksApi } from '@/api'
import type { PlaybackConfig, ProxyStatus } from '@/api/playback'
import type { ShareSource } from '@/api/sharestrm'
import type { DeepDeleteRecord, OrphanReport } from '@/api/sync'
import type { TaskJob } from '@/api/tasks'
import type { FullSetting } from './fullSetting'
import type { DeepDelSetting } from './deepDelSetting'
import type { StrmRuns } from './useStrmRuns'
import { useQueueStore } from '@/stores/queue'
import { useShareStrm } from '@/composables/shareStrm'
import { cleanupList } from '@/pages/sharestrm/cleanup'
import { fullTime, relCronNext, relTime } from '@/utils/time'
import { num } from '@/utils/format'

/**
 * Strm 管理页顶部的一行状态条：四件事各一段，「现在好不好」一眼看到，点链接跳到对应页签。
 * 只看不做：「立即同步」「开始全量」放回各自页签（维护者嫌状态条上放执行按钮太挤）。
 *
 * 数据全是已有的只读接口（增量状态 / 失效 STRM / 任务历史 / 删除记录），零 115 请求。
 *
 * 最后一段「分享 STRM」只是入口：那是另一套（不进台账、全量增量深删都不碰），页面单独放在 /share-strm，
 * 别并成这里的页签——摆在一页会让人以为全量会扫分享、Emby 里删了会深删。用户想到 STRM 先来这页找，
 * 所以在这里给一段跳过去，没开时也显示，指向开关。
 * 增量状态 15 秒拉一次（后端只读内存），其余在相关任务跑完时刷新。
 */
const props = defineProps<{ full: FullSetting; runs: StrmRuns; deepdel: DeepDelSetting }>()
const emit = defineEmits<{ goto: [tab: string, anchor?: string] }>()

const queue = useQueueStore()
const router = useRouter()

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

// 播放方式（playmode.go）：读配置与中转状态，都是只读内存的接口
const play = ref<PlaybackConfig | null>(null)
const proxy = ref<ProxyStatus | null>(null)
async function loadPlay() {
  try {
    ;[play.value, proxy.value] = await Promise.all([playbackApi.config(), playbackApi.proxyStatus()])
  } catch {
    // 读不到维持上一次的
  }
}

// 分享 STRM：开关读模块级配置（侧栏也读它），开着才列分享（只读本地两张表）
const share = useShareStrm()
const shares = ref<ShareSource[] | null>(null)
async function loadShares() {
  if (!share.enabled.value) return
  try {
    shares.value = (await shareStrmApi.sources()).sources ?? []
  } catch {
    // 读不到维持上一次的
  }
}
watch(share.enabled, (on) => on && void loadShares())

let timer: number | undefined
onMounted(() => {
  void loadIncr()
  void loadOrphan()
  void loadLastFull()
  void loadLastDel()
  void loadPlay()
  void loadShares()
  timer = window.setInterval(() => {
    if (document.hidden) return
    void loadIncr()
    void loadPlay()
  }, 15_000)
})
const offFinished = queue.onFinished((j) => {
  if (j.kind === 'full') {
    void loadOrphan()
    void loadLastFull()
  }
  if (j.kind === 'incr') void loadIncr()
  if (j.kind === 'sharestrm') void loadShares()
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
type Tone = StripTone
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
    lines.push({ text: nextFull.value ? `定时 · 下次 ${relCronNext(nextFull.value)}` : `定时 · ${cfg.cron}`, title: cfg.cron })
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

const playTile = computed<Tile>(() => {
  const base = { key: 'play', title: '播放', icon: MonitorPlay }
  const c = play.value
  if (!c) return { ...base, ...LOADING }
  const users = Object.values(c.users ?? {}).filter((m) => m === 'proxy').length
  const mode = c.mode === 'proxy' ? '默认中转' : users ? `302 · ${users} 人中转` : '302 直连'
  const s = proxy.value
  const cool = s?.accounts.find((a) => a.cool_left > 0)
  const lines: Tile['lines'] = [{ text: mode }]
  if (s?.plays.length) lines.push({ text: `${s.plays.length} 路在中转` })
  if (cool) lines.push({ text: `${cool.name} 被 115 限流，暂停 ${cool.cool_left} 秒`, tone: 'warn' })
  return {
    ...base,
    tone: cool ? 'warn' : 'ok',
    badge: mode,
    value: mode,
    lines,
    short: [mode, s?.plays.length ? `${s.plays.length} 路在传` : '', cool ? '被限流' : ''].filter(Boolean).join(' · '),
  }
})

const shareTile = computed<Tile>(() => {
  const base = { key: 'share', title: '分享 STRM', icon: Link2 }
  const d = share.resp.value
  if (!d) return { ...base, ...LOADING }
  if (!d.config.enabled) {
    return {
      ...base,
      tone: 'off',
      badge: '未开启',
      value: '未开启',
      lines: [{ text: '不转存，STRM 直接指向 115 分享；和上面几项是两套，同步与深度删除都不碰它' }],
      short: '未开启',
    }
  }
  const list = shares.value
  if (!list) return { ...base, ...LOADING }
  const clean = cleanupList(list).length
  const files = list.reduce((n, s) => n + (s.files || 0), 0)
  const lines: Tile['lines'] = [{ text: `${list.length} 个分享 · ${num(files)} 个 STRM` }]
  if (clean) lines.push({ text: `${clean} 个分享已失效，待清理`, tone: 'danger' })
  if (share.blocked.value) lines.push({ text: share.blocked.value, tone: 'warn' })
  return {
    ...base,
    tone: clean ? 'danger' : share.blocked.value ? 'warn' : list.length ? 'ok' : 'off',
    badge: '已开启',
    value: `${list.length}`,
    unit: '个',
    lines,
    short: [list.length ? `${list.length} 个分享` : '还没有分享', clean ? `${clean} 个待清理` : ''].filter(Boolean).join(' · '),
  }
})

const tiles = computed(() => [incrTile.value, fullTile.value, orphanTile.value, delTile.value, playTile.value, shareTile.value])

// 状态条上不放执行按钮（维护者要求）：「立即同步」「开始全量」在各自页签里
const ACTION: Record<string, string> = { incr: '详情', full: '设置', orphan: '清理', deepdel: '记录', play: '设置', share: '打开' }
const items = computed<StripItem[]>(() =>
  tiles.value.map((t) => ({
    key: t.key,
    title: t.title,
    tone: t.tone,
    text: t.short,
    tip: t.lines.map((l) => l.title || l.text).join('\n'),
    action:
      t.key === 'orphan' && !(orphan.value?.total ?? 0)
        ? undefined
        : t.key === 'share' && !share.enabled.value
          ? '去开启'
          : ACTION[t.key],
  })),
)
function act(key: string) {
  if (key === 'orphan') emit('goto', 'full', 'orphans')
  else if (key === 'play') emit('goto', 'config', 'playback')
  else if (key === 'share') {
    const clean = shares.value ? cleanupList(shares.value).length : 0
    const tab = !share.enabled.value ? 'settings' : clean ? 'cleanup' : undefined
    void router.push({ name: 'share-strm', query: tab ? { tab } : {} })
  }
  else emit('goto', key)
}
</script>

<template>
  <!-- 原来是四张大卡片，占掉 200px 把下面的配置挤到折叠线以下，2026-09-30 收成一行（样式在 StatusStrip） -->
  <StatusStrip :items="items" @act="act" />
</template>
