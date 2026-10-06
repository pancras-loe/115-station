import type { TaskJob, TaskProbeItem } from '@/api/tasks'

/** 任务状态 → 文案与 chip 颜色。顶栏弹层与任务中心共用 */
export const JOB_STATUS: Record<string, { text: string; color: 'default' | 'accent' | 'success' | 'warning' | 'danger' }> = {
  running: { text: '执行中', color: 'accent' },
  queued: { text: '排队中', color: 'default' },
  success: { text: '完成', color: 'success' },
  partial: { text: '部分失败', color: 'warning' },
  failed: { text: '失败', color: 'danger' },
  canceled: { text: '已取消', color: 'warning' },
  interrupted: { text: '中断', color: 'danger' },
}

/** 任务类型（TaskJob.kind）。未列出的原样显示 */
export const JOB_KIND: Record<string, string> = {
  organize: '整理',
  transfer: '转存后整理',
  redo: '重新整理',
  confirm: '确认入库',
  orgpick: '整理所选',
  scrape: '刮削',
  probe: 'Emby 探测',
  person: '演职人员补全',
  metafill: '媒体信息补全',
  libredo: '重新整理片目',
  libepisode: '指定季集',
  filemove: '移动',
  ignore: '忽略',
  deepdel: '深度删除',
  full: '全量同步',
  incr: '增量同步',
  background: '后台任务',
}

/** 提交来源（TaskJob.source） */
export const JOB_SOURCE: Record<string, string> = {
  web: '网页',
  wecom: '企业微信',
  tg: 'Telegram',
  cron: '定时',
  auto: '自动',
  organize: '整理后',
  incr: '增量同步后',
  scrape: '刮削后',
  redo: '重新整理后',
  ingest: '入库后',
}

/** 不在主队列上的任务，排队位置前面加上是哪条队列（刮削、探测、人物各自一条，不等任务锁）；主队列为空串 */
export function laneText(kind: string) {
  if (kind === 'scrape' || kind === 'metafill') return '刮削队列'
  if (kind === 'probe') return '探测队列'
  if (kind === 'person') return '人物队列'
  return ''
}

export function jobKindText(kind: string) {
  return JOB_KIND[kind] ?? kind
}

export function jobSourceText(source: string) {
  return JOB_SOURCE[source] ?? (source || '—')
}

export function dur(sec: number) {
  if (sec < 60) return `${Math.max(0, Math.round(sec))} 秒`
  const m = Math.floor(sec / 60)
  if (m < 60) return `${m} 分 ${Math.round(sec % 60)} 秒`
  return `${Math.floor(m / 60)} 小时 ${m % 60} 分`
}

/** 已运行 / 用时：运行中的按当前时间算 */
export function elapsed(j: TaskJob) {
  if (!j.started_at) return ''
  const end = j.finished_at ? new Date(j.finished_at).getTime() : Date.now()
  return dur((end - new Date(j.started_at).getTime()) / 1000)
}

/** 条目内的第二级进度：「集剧照 87/212：S01E87-thumb.jpg」；没有时为空串 */
export function subProgressText(j: TaskJob) {
  const s = j.progress?.sub
  if (!s?.phase) return ''
  let t = s.phase
  if (s.total) t += ` ${s.done}/${s.total}`
  if (s.label) t += `：${s.label}`
  return t
}

export function pct(j: TaskJob) {
  const p = j.progress
  if (!p || !p.total) return 0
  return Math.min(100, Math.round((p.done / p.total) * 100))
}

/** 后台任务（Emby 事件深删留下的历史行）不在队列里执行，没法重试，等它下一轮自动跑 */
export function retryable(j: TaskJob) {
  return (
    j.kind !== 'background' &&
    (j.status === 'partial' || j.status === 'failed' || j.status === 'interrupted' || j.status === 'canceled')
  )
}

/** 结果说明该用什么色：失败 / 中断红、部分失败黄 */
export function msgTone(j: TaskJob): '' | 'err' | 'warn' {
  if (j.status === 'failed' || j.status === 'interrupted') return 'err'
  if (j.status === 'partial') return 'warn'
  return ''
}

/** 结构化结果里认得的计数字段（整理：success / exists / failed / awaiting；全量：orphans） */
const RESULT_LABEL: [string, string][] = [
  ['success', '成功'],
  ['exists', '已存在'],
  ['failed', '失败'],
  ['awaiting', '待确认'],
  ['orphans', '失效 STRM'],
  // 刮削（localscrape.go 的 fileScrapeResult）
  ['titles', '片目'],
  ['reused', '复用已下载图片'],
  ['placeholder', '占位剧照未写'],
  ['reclaimed', '收回孤儿文件'],
  // 演职人员补全（embypeople.go 的 personJobResult）
  ['people', '处理人物'],
  ['images', '补头像'],
  ['renamed', '改中文名'],
  ['bios', '补中文简介'],
  // 媒体信息补全（metafill.go 的 metaFillResult）
  ['scrape_lack', '没刮全'],
  ['scrape_queued', '补刮'],
  ['probe_lack', '缺媒体信息'],
  ['probe_queued', '探测'],
]

/** 刮削结果里嵌套的产物计数 */
const SCRAPE_STAT_LABEL: [string, string][] = [
  ['local', '写入本地'],
  ['uploaded', '上传网盘'],
  ['skipped', '已存在跳过'],
]

/** 结果摘要：「成功 12 · 已存在 3 · 待确认 1」；没有可显示的计数时为空串 */
export function resultSummary(j: TaskJob) {
  const r = j.result
  if (!r) return ''
  const parts = RESULT_LABEL.filter(([k]) => typeof r[k] === 'number' && (r[k] as number) > 0).map(
    ([k, label]) => `${label} ${r[k]}`,
  )
  const stat = r.stat as Record<string, unknown> | undefined
  if (stat && typeof stat === 'object') {
    for (const [k, label] of SCRAPE_STAT_LABEL) {
      if (typeof stat[k] === 'number' && (stat[k] as number) > 0) parts.push(`${label} ${stat[k]}`)
    }
  }
  const p = j.probe
  if (p) {
    if (p.state === 'queued' || p.state === 'running') parts.push('Emby 探测进行中')
    if (p.ok) parts.push(`探测成功 ${p.ok}`)
    if (p.failed + p.errors) parts.push(`探测失败 ${p.failed + p.errors}`)
    if (p.held) parts.push(`探测跳过 ${p.held}`)
  }
  return parts.join(' · ')
}

/** 探测任务的整体状态文案 */
export const PROBE_REPORT_STATE: Record<string, string> = {
  queued: '排队中',
  running: '探测中',
  done: '已结束',
  canceled: '已停止',
  lost: '中断（服务重启过，没探完的没有继续）',
}

/** 没探成的条目归类 */
export const PROBE_ITEM_KIND: Record<TaskProbeItem['kind'], { text: string; color: 'warning' | 'danger' | 'default' }> = {
  failed: { text: '失败', color: 'danger' },
  error: { text: '出错', color: 'danger' },
  held: { text: '防抖跳过', color: 'warning' },
  missing: { text: '查不到', color: 'default' },
}

/** 失败 / 跳过的条目里，最晚什么时候全部能再手动请求（重试按钮据此倒计时）；都能了返回 undefined */
export function probeRetryReadyAt(items: TaskProbeItem[] | undefined, now: number): string | undefined {
  let latest: string | undefined
  for (const it of items ?? []) {
    if ((it.kind === 'failed' || it.kind === 'held') && it.retry_at && new Date(it.retry_at).getTime() > now) {
      if (!latest || new Date(it.retry_at) > new Date(latest)) latest = it.retry_at
    }
  }
  return latest
}

/** 结果里的问题清单（刮削：未能刮削的片目 problems + 出错明细 errors） */
export function resultIssues(j: TaskJob): string[] {
  const r = j.result
  if (!r) return []
  const pick = (k: string) => (Array.isArray(r[k]) ? (r[k] as unknown[]).filter((x): x is string => typeof x === 'string') : [])
  return [...pick('problems'), ...pick('errors')]
}
