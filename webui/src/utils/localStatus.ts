import type { LocalTitle, LocalTitleStatus } from '@/api/local'

/**
 * 海报墙的刮削状态文案：卡片墙与详情抽屉共用（口径在后端 localdetail.go 的 grade）。
 * 刮削方式为 Emby 时同一个 status 换一套说法（后端 localemby.go 的 gradeByEmby）：看的是 Emby 认没认出条目、有没有图，
 * 函数都带 emby 参数区分
 */

export const STATUS_TEXT: Record<LocalTitleStatus, string> = { ok: '已刮削', partial: '不完整', miss: '未刮削', pending: '读取中' }
const EMBY_TEXT: Record<LocalTitleStatus, string> = { ok: '已刮削', partial: '不完整', miss: 'Emby 里没有', pending: '读取中' }

export const STATUS_TONE: Record<LocalTitleStatus, 'success' | 'warning' | 'danger' | 'default'> = {
  ok: 'success',
  partial: 'warning',
  miss: 'danger',
  pending: 'default',
}

export function statusText(s: LocalTitleStatus, emby = false): string {
  return (emby ? EMBY_TEXT : STATUS_TEXT)[s]
}

type Graded = Pick<LocalTitle, 'status' | 'lack' | 'soft' | 'mismatch' | 'tmdb_id'>

/** 角标：不完整时直接写缺什么（卡片窄，缺好几样只写第一样 + 总数）；认错了条目比缺图要紧，先说它 */
export function statusLabel(t: Graded, emby = false): string {
  if (t.status === 'partial' && t.mismatch) return 'TMDB 对不上'
  const lack = t.lack ?? []
  if (t.status !== 'partial' || !lack.length) return statusText(t.status, emby)
  return lack.length === 1 ? `缺${lack[0]}` : `缺${lack[0]}等 ${lack.length} 项`
}

/** 悬停提示：缺的必需产物 + 可选图片（后者不影响状态，只在这里提一句） */
export function statusTitle(t: Graded, emby = false): string {
  if (emby) return embyStatusTitle(t)
  const parts = [STATUS_TEXT[t.status]]
  if (t.status === 'partial' && t.lack?.length) parts.push(`缺：${t.lack.join('、')}`)
  if (t.soft?.length) parts.push(`可选图片缺：${t.soft.join('、')}（TMDB 上不一定有，不算没刮全）`)
  return parts.join('\n')
}

function embyStatusTitle(t: Graded): string {
  switch (t.status) {
    case 'ok':
      return 'Emby 已刮削：认出了条目，海报与背景图都有'
    case 'miss':
      return 'Emby 里没有这个片目的影视条目：还没扫描入库，或 Emby 没认出它是电影 / 剧集'
    case 'pending':
      return '正在读取 Emby 的媒体库信息'
  }
  const parts: string[] = []
  if (t.mismatch) {
    parts.push(`Emby 认成了 TMDB ${t.mismatch}，目录名里的编号是 ${t.tmdb_id}：用「刷新元数据 → 替换全部」重刮，或在 Emby 里手动「识别」`)
  }
  if (t.lack?.length) parts.push(`Emby 里缺：${t.lack.join('、')}`)
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
