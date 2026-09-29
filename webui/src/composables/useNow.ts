import { onUnmounted, ref } from 'vue'

/**
 * 每秒走一次的「现在」：倒计时（探测防抖「还有 3 分 20 秒可重试」）要跟着时间自己变，
 * 不能等下一次接口轮询。组件卸载时自动停
 */
export function useNow(intervalMs = 1000) {
  const now = ref(Date.now())
  const timer = window.setInterval(() => (now.value = Date.now()), intervalMs)
  onUnmounted(() => clearInterval(timer))
  return now
}

/** 距离某个时间还有多久：「3 分 20 秒」；已经过了返回空串 */
export function untilText(at: string | undefined, now: number): string {
  if (!at) return ''
  const ms = new Date(at).getTime() - now
  if (!(ms > 0)) return ''
  const sec = Math.ceil(ms / 1000)
  if (sec < 60) return `${sec} 秒`
  const m = Math.floor(sec / 60)
  if (m < 60) return `${m} 分 ${sec % 60} 秒`
  return `${Math.floor(m / 60)} 小时 ${m % 60} 分`
}
