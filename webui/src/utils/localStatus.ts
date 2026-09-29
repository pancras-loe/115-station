import type { LocalTitle, LocalTitleStatus } from '@/api/local'

/** 本地文件页的刮削状态文案：卡片墙与详情抽屉共用（口径在后端 localdetail.go 的 grade） */

export const STATUS_TEXT: Record<LocalTitleStatus, string> = { ok: '已刮削', partial: '不完整', miss: '未刮削' }
export const STATUS_TONE: Record<LocalTitleStatus, 'success' | 'warning' | 'danger'> = {
  ok: 'success',
  partial: 'warning',
  miss: 'danger',
}

type Graded = Pick<LocalTitle, 'status' | 'lack' | 'soft'>

/** 角标：不完整时直接写缺什么（卡片窄，缺好几样只写第一样 + 总数） */
export function statusLabel(t: Graded): string {
  const lack = t.lack ?? []
  if (t.status !== 'partial' || !lack.length) return STATUS_TEXT[t.status]
  return lack.length === 1 ? `缺${lack[0]}` : `缺${lack[0]}等 ${lack.length} 项`
}

/** 悬停提示：缺的必需产物 + 可选图片（后者不影响状态，只在这里提一句） */
export function statusTitle(t: Graded): string {
  const parts = [STATUS_TEXT[t.status]]
  if (t.status === 'partial' && t.lack?.length) parts.push(`缺：${t.lack.join('、')}`)
  if (t.soft?.length) parts.push(`可选图片缺：${t.soft.join('、')}（TMDB 上不一定有，不算没刮全）`)
  return parts.join('\n')
}

/** Emby 媒体信息角标；没缺的不显示 */
export function probeLabel(t: Pick<LocalTitle, 'emby' | 'media_type'>): string {
  const e = t.emby
  if (!e?.lack) return ''
  return e.items <= 1 && t.media_type !== 'tv' ? '未探测' : `未探测 ${e.lack}`
}

export function probeTitle(t: Pick<LocalTitle, 'emby'>): string {
  const e = t.emby
  if (!e?.lack) return ''
  return `Emby 里 ${e.items} 个条目中 ${e.lack} 个还没有媒体信息（音视频轨道）。点开详情的「媒体信息」可以提前探测`
}
