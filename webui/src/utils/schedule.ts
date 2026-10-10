/**
 * 定时计划 ⇄ 5 段 cron（分 时 日 月 周）。给 SchedulePicker 用：常见的几种写法换成「每天 / 每周 / 每月 / 每隔」表单，
 * 认不出来的（月份不是 *、分钟是列表 …）落到「自定义」，原样保留表达式，不替用户改写。
 * 后端的解析（internal/api/cron.go 的 cronFieldMatch）认 `*`、`*\/n`、`a-b`、逗号列表，周日是 0、不认 7。
 */
export type ScheduleMode = 'daily' | 'weekly' | 'monthly' | 'hours' | 'minutes' | 'custom'

export interface Schedule {
  mode: ScheduleMode
  hour: number
  minute: number
  /** 周几：0 = 周日 … 6 = 周六 */
  weekdays: number[]
  /** 每月几号 */
  day: number
  /** 每隔几小时 / 几分钟 */
  every: number
  /** 每隔几分钟：只在这几个钟头里跑（含两端），0–23 即全天 */
  fromHour: number
  toHour: number
  /** 自定义模式的原始表达式 */
  cron: string
}

export const HOUR_STEPS = [1, 2, 3, 4, 6, 8, 12]
export const MINUTE_STEPS = [5, 10, 15, 20, 30]
/** 界面上按周一到周日排 */
export const WEEKDAYS: { v: number; label: string }[] = [
  { v: 1, label: '一' },
  { v: 2, label: '二' },
  { v: 3, label: '三' },
  { v: 4, label: '四' },
  { v: 5, label: '五' },
  { v: 6, label: '六' },
  { v: 0, label: '日' },
]

const NUM = /^\d+$/

function inRange(n: number, lo: number, hi: number) {
  return Number.isInteger(n) && n >= lo && n <= hi
}

/** 周字段：逗号列表 / 范围，展开成 0–6；有认不出的返回 null */
function parseWeekdays(f: string): number[] | null {
  const out = new Set<number>()
  for (const part of f.split(',')) {
    const r = /^(\d)-(\d)$/.exec(part)
    if (r) {
      const lo = +r[1]
      const hi = +r[2]
      if (lo > hi || hi > 6) return null
      for (let i = lo; i <= hi; i++) out.add(i)
    } else if (/^\d$/.test(part) && +part <= 6) {
      out.add(+part)
    } else {
      return null
    }
  }
  return out.size ? [...out].sort((a, b) => a - b) : null
}

export function parseCron(expr: string, fallback = '30 3 * * *'): Schedule {
  const raw = (expr || '').trim() || fallback
  const base: Schedule = {
    mode: 'custom', hour: 3, minute: 30, weekdays: [1], day: 1, every: 2, fromHour: 0, toHour: 23, cron: raw,
  }
  const f = raw.split(/\s+/)
  if (f.length !== 5) return base
  const [m, h, dom, mon, dow] = f
  if (mon !== '*') return base

  const step = (x: string) => (/^\*\/(\d+)$/.exec(x) ? +x.slice(2) : x === '*' ? 1 : 0)

  // 每隔 N 分钟：*/n * * * *，或限定时段 */n 8-23 * * *
  if (dom === '*' && dow === '*' && /^\*\/\d+$/.test(m)) {
    const n = step(m)
    if (!inRange(n, 1, 59)) return base
    if (h === '*') return { ...base, mode: 'minutes', every: n }
    const r = /^(\d+)-(\d+)$/.exec(h)
    if (r && inRange(+r[1], 0, 23) && inRange(+r[2], 0, 23) && +r[1] <= +r[2]) {
      return { ...base, mode: 'minutes', every: n, fromHour: +r[1], toHour: +r[2] }
    }
    return base
  }
  if (!NUM.test(m) || !inRange(+m, 0, 59)) return base
  const minute = +m
  // 每隔 N 小时：m */n * * *（n=1 写成 m * * * *）
  if (dom === '*' && dow === '*' && (h === '*' || /^\*\/\d+$/.test(h))) {
    const n = step(h)
    if (inRange(n, 1, 23)) return { ...base, mode: 'hours', minute, every: n }
    return base
  }
  if (!NUM.test(h) || !inRange(+h, 0, 23)) return base
  const hour = +h
  if (dom === '*' && dow === '*') return { ...base, mode: 'daily', hour, minute }
  if (dom === '*') {
    const days = parseWeekdays(dow)
    if (days) return { ...base, mode: 'weekly', hour, minute, weekdays: days }
    return base
  }
  if (dow === '*' && NUM.test(dom) && inRange(+dom, 1, 31)) return { ...base, mode: 'monthly', hour, minute, day: +dom }
  return base
}

export function buildCron(s: Schedule): string {
  const t = `${s.minute} ${s.hour}`
  switch (s.mode) {
    case 'daily':
      return `${t} * * *`
    case 'weekly': {
      const days = [...new Set(s.weekdays)].sort((a, b) => a - b)
      return `${t} * * ${days.length ? days.join(',') : '*'}`
    }
    case 'monthly':
      return `${t} ${s.day} * *`
    case 'hours':
      return `${s.minute} ${s.every > 1 ? `*/${s.every}` : '*'} * * *`
    case 'minutes': {
      const allDay = s.fromHour <= 0 && s.toHour >= 23
      return `*/${s.every} ${allDay ? '*' : `${s.fromHour}-${Math.max(s.fromHour, s.toHour)}`} * * *`
    }
    default:
      return s.cron.trim()
  }
}

const pad = (n: number) => String(n).padStart(2, '0')

/** 一句话说明，自定义模式返回空串（交给预览的下次运行时间） */
export function describeSchedule(s: Schedule): string {
  const at = `${pad(s.hour)}:${pad(s.minute)}`
  switch (s.mode) {
    case 'daily':
      return `每天 ${at}`
    case 'weekly': {
      const set = new Set(s.weekdays)
      if (set.size === 7) return `每天 ${at}`
      const names = WEEKDAYS.filter((d) => set.has(d.v)).map((d) => d.label)
      return `每周${names.join('、')} ${at}`
    }
    case 'monthly':
      return `每月 ${s.day} 日 ${at}${s.day > 28 ? '（没有这一天的月份跳过）' : ''}`
    case 'hours':
      return s.every > 1 ? `每 ${s.every} 小时一次（整点后第 ${s.minute} 分）` : `每小时第 ${s.minute} 分`
    case 'minutes':
      return s.fromHour <= 0 && s.toHour >= 23
        ? `每 ${s.every} 分钟一次`
        : `每 ${s.every} 分钟一次（${pad(s.fromHour)}:00–${pad(s.toHour)}:59）`
    default:
      return ''
  }
}
