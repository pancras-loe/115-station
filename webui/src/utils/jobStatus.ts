import type { TaskJob } from '@/api/tasks'

/** 任务状态 → 文案与 chip 颜色。顶栏弹层与任务中心共用 */
export const JOB_STATUS: Record<string, { text: string; color: 'default' | 'accent' | 'success' | 'warning' | 'danger' }> = {
  running: { text: '执行中', color: 'accent' },
  queued: { text: '排队中', color: 'default' },
  success: { text: '完成', color: 'success' },
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
  cron: '定时',
  auto: '自动',
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

export function pct(j: TaskJob) {
  const p = j.progress
  if (!p || !p.total) return 0
  return Math.min(100, Math.round((p.done / p.total) * 100))
}

/** 后台任务（Emby 事件深删留下的历史行）不在队列里执行，没法重试，等它下一轮自动跑 */
export function retryable(j: TaskJob) {
  return j.kind !== 'background' && (j.status === 'failed' || j.status === 'interrupted' || j.status === 'canceled')
}

/** 结构化结果里认得的计数字段（整理：success / exists / failed / awaiting；全量：orphans） */
const RESULT_LABEL: [string, string][] = [
  ['success', '成功'],
  ['exists', '已存在'],
  ['failed', '失败'],
  ['awaiting', '待确认'],
  ['orphans', '失效 STRM'],
]

/** 结果摘要：「成功 12 · 已存在 3 · 待确认 1」；没有可显示的计数时为空串 */
export function resultSummary(j: TaskJob) {
  const r = j.result
  if (!r) return ''
  return RESULT_LABEL.filter(([k]) => typeof r[k] === 'number' && (r[k] as number) > 0)
    .map(([k, label]) => `${label} ${r[k]}`)
    .join(' · ')
}
