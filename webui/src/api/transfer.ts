import { http } from './client'

// ============ 影视转存：按影片聚合各资源站（后端 transferhub.go） ============

export type SourceKey = 'gy' | 'pansou' | 'tg' | 'mukaku' | 're0' | 'dian'

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
  /** 列表上标的解锁积分，只用于后端日志写清楚花没花积分 */
  points?: number
  /** 转存到哪个绑定了媒体库的转存子目录（cid）；空 = 转存目录本身 */
  subdir?: string
  /** 115 分享不转存、直接生成分享 STRM（后端 sharestrm.go），只认自己贴的链接 */
  strm?: boolean
}

export interface SubmitResult {
  message: string
  /** 非 115 的链接：交给用户自己打开 */
  open_url?: string
  code?: string
}

export const submit = (body: SubmitBody) =>
  http.post<SubmitResult>('/transfer/submit', body, { timeoutMs: 5 * 60_000 })

/** 转存子目录绑定的媒体库（按来源分库，后端 stagingsub.go） */
export interface StagingLib {
  cid: string
  name: string
  /** 媒体库 cid */
  lib: string
  lib_name?: string
  /** 失效原因（库移出配置 / 子目录不在转存目录下） */
  problem?: string
  /** V2：这个库在哪个账号（订阅只能进主号的库） */
  account?: number
}

export interface LibChoice {
  cid: string
  name: string
  primary: boolean
}

export interface StagingLibs {
  root: string
  items: StagingLib[]
  libs: LibChoice[]
  /** 媒体库表（V2）：items 是能转存进的库（cid = lib:<id>），没有子目录绑定 */
  v2?: boolean
  /** 默认转存库 id（0 = 没设） */
  default?: number
  /** 多个库、没设默认：每次都要选 */
  need_pick?: boolean
}

/** 转存到哪个库（V2，后端 xfertarget.go） */
export interface XferLib {
  value: string
  id: number
  name: string
  account: number
  account_name?: string
  staging_path: string
}
export interface XferTargets {
  v2: boolean
  libs: XferLib[]
  default: number
  multi_account: boolean
  need_pick: boolean
}
export const xferTargets = () => http.get<XferTargets>('/transfer/targets')
export const saveXferDefault = (lib: number) => http.put<XferTargets>('/transfer/targets/default', { lib })

export const stagingLibs = () => http.get<StagingLibs>('/staging/libs')
export const saveStagingLibs = (items: Pick<StagingLib, 'cid' | 'name' | 'lib'>[]) =>
  http.put<StagingLibs>('/staging/libs', { items })
export const stagingSubdirs = () => http.get<{ dirs: { cid: string; name: string }[] }>('/staging/subdirs')
export const mkStagingSubdir = (name: string) => http.post<{ cid: string; name: string }>('/staging/subdirs', { name })

export interface OwnedInfo {
  title: string
  category?: string
  at?: number
}

/** 哪些 TMDB 条目已经整理进库（只查本地台账），keys 形如 movie:1 / tv:2 */
export const owned = (keys: string[]) =>
  http.get<{ data: Record<string, OwnedInfo> }>('/transfer/owned', { params: { keys: keys.join(',') } })
