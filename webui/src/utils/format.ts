/** 千分位；null/undefined 显示为 0 */
export function num(n: number | undefined | null): string {
  return (n ?? 0).toLocaleString('zh-CN')
}

const UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']

export function bytes(n: number | undefined | null): string {
  let v = Number(n ?? 0)
  if (!Number.isFinite(v) || v <= 0) return '0 B'
  let i = 0
  while (v >= 1024 && i < UNITS.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${UNITS[i]}`
}

export function percent(n: number | undefined | null, digits = 1): string {
  return `${(n ?? 0).toFixed(digits)}%`
}

/** 占用率配色：>85 危险 / >65 警告 / 其余正常。三卡（容量、内存、CPU）共用同一口径 */
export function loadTone(pct: number): 'danger' | 'warning' | 'normal' {
  if (pct > 85) return 'danger'
  if (pct > 65) return 'warning'
  return 'normal'
}
