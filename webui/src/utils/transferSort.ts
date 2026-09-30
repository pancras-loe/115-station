import type { ResourceItem, SourceKey } from '@/api/transfer'

// 影视转存结果的排序与文案。「推荐」排序与后端 resSortDefault 同口径：
// 各来源分开回来，前端合并后要按同一把尺子重排

export const SOURCE_LABEL: Record<SourceKey, string> = {
  gy: '观影',
  pansou: '盘搜',
  tg: 'TG 频道',
  mukaku: '不太灵',
  re0: 'RE0',
}

const PAN_LABEL: Record<string, string> = {
  baidu: '百度网盘',
  uc: 'UC 网盘',
  xunlei: '迅雷云盘',
  tianyi: '天翼云盘',
  mobile: '移动云盘',
  weiyun: '微云',
  lanzou: '蓝奏云',
}

/** 类型标签：115 分享 / 磁力 / ed2k / 百度网盘… */
export function kindLabel(it: ResourceItem): string {
  if (it.kind === 'share115') return '115 分享'
  if (it.kind === 'magnet') return '磁力'
  if (it.kind === 'ed2k') return 'ed2k'
  return (it.pan && (PAN_LABEL[it.pan] || it.pan)) || '其他网盘'
}

/** 类型筛选分组：能直接进 115 的两类 + 其他网盘 */
export type KindGroup = 'share115' | 'download' | 'pan'
export function kindGroup(it: ResourceItem): KindGroup {
  if (it.kind === 'share115') return 'share115'
  if (it.kind === 'magnet' || it.kind === 'ed2k') return 'download'
  return 'pan'
}

export function pixScore(pix?: string): number {
  if (!pix) return 0
  if (pix.startsWith('4320')) return 5
  if (pix.startsWith('2160')) return 4
  if (pix.startsWith('1080')) return 3
  if (pix.startsWith('720')) return 2
  return 1
}

/** 分辨率筛选分组 */
export function pixGroup(pix?: string): '4k' | '1080' | 'other' {
  const s = pixScore(pix)
  if (s >= 4) return '4k'
  if (s === 3) return '1080'
  return 'other'
}

export function pixLabel(pix?: string): string {
  switch (pixScore(pix)) {
    case 5:
      return '8K'
    case 4:
      return '4K'
    case 3:
      return '1080P'
    case 2:
      return '720P'
  }
  return pix ? pix.toUpperCase() : ''
}

export type SortKey = 'recommend' | 'size' | 'time' | 'seeds'

const rankOf = (r: number) => (r < 0 ? 1 << 20 : r)

function recommend(a: ResourceItem, b: ResourceItem): number {
  if (a.relevant !== b.relevant) return a.relevant ? -1 : 1
  const ao = a.action === 'open'
  const bo = b.action === 'open'
  if (ao !== bo) return ao ? 1 : -1
  if (rankOf(a.rank) !== rankOf(b.rank)) return rankOf(a.rank) - rankOf(b.rank)
  const pa = pixScore(a.tags.pix)
  const pb = pixScore(b.tags.pix)
  if (pa !== pb) return pb - pa
  if ((a.seeds ?? 0) !== (b.seeds ?? 0)) return (b.seeds ?? 0) - (a.seeds ?? 0)
  return (b.time_unix ?? 0) - (a.time_unix ?? 0)
}

/** 排序：除「推荐」外都是降序，同值再按推荐顺序 */
export function sortResources(list: ResourceItem[], key: SortKey): ResourceItem[] {
  const out = list.slice()
  const by: Record<SortKey, (a: ResourceItem, b: ResourceItem) => number> = {
    recommend,
    size: (a, b) => (b.size_bytes ?? 0) - (a.size_bytes ?? 0) || recommend(a, b),
    time: (a, b) => (b.time_unix ?? 0) - (a.time_unix ?? 0) || recommend(a, b),
    seeds: (a, b) => (b.seeds ?? 0) - (a.seeds ?? 0) || recommend(a, b),
  }
  return out.sort(by[key])
}

/** 同一条资源的稳定 key：各来源的链接或引用 */
export function resourceKey(it: ResourceItem): string {
  return `${it.source}|${it.url || it.ref || it.title}`
}
