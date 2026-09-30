import { http } from './client'

// ============ 影视转存：按影片聚合各资源站（后端 transferhub.go） ============

export type SourceKey = 'gy' | 'pansou' | 'tg' | 'mukaku' | 're0'

export interface TransferSource {
  key: SourceKey
  label: string
  /** 用户在来源设置里关掉的不去请求 */
  enabled: boolean
  /** 不能用的原因（没登录 / 没配置），空串 = 可用 */
  reason: string
}

export interface TransferSources {
  sources: TransferSource[]
  /** 转存目录（setting share） */
  folder?: string
  folder_path?: string
}

export interface ResourceTags {
  pix?: string
  type?: string
  effect?: string
  video?: string
  audio?: string
  team?: string
  /** S01 / S01-S03 / 全集 */
  season?: string
  zh?: boolean
}

export type ResourceAction = 'transfer' | 'offline' | 'open' | 'unlock'
export type ResourceKind = 'share115' | 'magnet' | 'ed2k' | 'pan'

export interface ResourceItem {
  source: SourceKey
  kind: ResourceKind
  /** 其他网盘的类型：baidu / uc / xunlei … */
  pan?: string
  action: ResourceAction
  title: string
  /** 更细的出处：TG 的频道名 */
  via?: string
  url?: string
  code?: string
  /** 观影详情页路径 / RE0 slug：服务端提交时再换成真链接 */
  ref?: string
  size?: string
  size_bytes?: number
  seeds?: number
  time?: string
  time_unix?: number
  /** RE0 解锁积分 */
  points?: number | null
  /** RE0 已解锁过，再解锁不扣积分 */
  owned?: boolean
  tags: ResourceTags
  /** 资源标题对得上所选影片（片名 / 原名 / 别名，电影还看年份） */
  relevant: boolean
  /** 命中洗版策略的第几条优先级规则（0 最优），-1 没命中 */
  rank: number
  /** 之前提交过（来源链接台账里有），unix 秒 */
  submitted_at?: number
}

export interface ResourceQuery {
  tmdb_id?: number
  type?: 'movie' | 'tv' | ''
  title: string
  original_title?: string
  year?: string
}

export const sources = () => http.get<TransferSources>('/transfer/sources')

export const saveSources = (disabled: SourceKey[]) => http.post('/transfer/sources', { disabled })

/** 一个来源的结果。来源不可用时 200 + unavailable，而不是报错 */
export const resources = (source: SourceKey, q: ResourceQuery, refresh = false) =>
  http.get<{ items: ResourceItem[]; note?: string; unavailable?: string }>(`/transfer/resources/${source}`, {
    params: { ...q, refresh: refresh ? '1' : undefined },
    // 盘搜聚合全网要好几秒，观影首次还要过 PoW
    timeoutMs: 120_000,
  })

export interface SubmitBody {
  /** 空 = 用户直接贴的链接 */
  source?: SourceKey | ''
  action?: ResourceAction
  url?: string
  code?: string
  ref?: string
  title?: string
  /** RE0 解锁花积分：确认过才带 true */
  confirm?: boolean
}

export interface SubmitResult {
  message: string
  /** 非 115 的链接：交给用户自己打开 */
  open_url?: string
  code?: string
}

export const submit = (body: SubmitBody) =>
  http.post<SubmitResult>('/transfer/submit', body, { timeoutMs: 5 * 60_000 })

export interface OwnedInfo {
  title: string
  category?: string
  at?: number
}

/** 哪些 TMDB 条目已经整理进库（只查本地台账），keys 形如 movie:1 / tv:2 */
export const owned = (keys: string[]) =>
  http.get<{ data: Record<string, OwnedInfo> }>('/transfer/owned', { params: { keys: keys.join(',') } })
