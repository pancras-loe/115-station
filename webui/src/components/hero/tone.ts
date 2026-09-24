/**
 * 旧代码里的状态语义是 Naive 的一套（success / warning / error / info / default），
 * HeroUI 的是另一套（success / warning / danger / accent / default）。
 * 数据驱动的颜色（「成功就绿、失败就红」）统一经这里转，别在各页各写一份 if。
 */
export type HeroTone = 'success' | 'warning' | 'danger' | 'accent' | 'default'

export function heroTone(t?: string | null): HeroTone {
  switch (t) {
    case 'success':
      return 'success'
    case 'warning':
      return 'warning'
    case 'error':
    case 'danger':
      return 'danger'
    case 'info':
    case 'primary':
    case 'accent':
      return 'accent'
    default:
      return 'default'
  }
}
