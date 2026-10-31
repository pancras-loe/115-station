import { http } from './client'

// ---- 分享 STRM：不转存，STRM 直接指向 115 分享（后端 sharestrm.go） ----

export type ShareLinkAction = 'ask' | 'transfer' | 'strm'

export interface ShareStrmConfig {
  enabled: boolean
  /** 贴 115 分享链接时默认怎么处理 */
  default_action: ShareLinkAction
  /** 分享 STRM 根：媒体根下的相对路径 */
  root: string
  min_size_mb: number
  skip_owned: boolean
  assets: boolean
  /** 新添加的分享默认隔几小时追更，0 = 不追更 */
  follow_hours: number
  /** 新添加的分享默认怎么放 */
  layout: ShareLayout
  /** 本地整理用的分类方案，0 = 只分电影 / 剧集 */
  scheme_id: number
  /** 本地整理认出的分享生成后用本站刮削（只补缺失、只写本地）；关着交给 Emby */
  scrape: boolean
}

/** keep 按分享里的目录原样放 / organize 本地整理（识别后按命名模板与分类放） */
export type ShareLayout = 'keep' | 'organize'
export const LAYOUT_OPTIONS: { label: string; value: ShareLayout }[] = [
  { label: '原样', value: 'keep' },
  { label: '本地整理', value: 'organize' },
]

/** 追更间隔的选项（后端最短 6 小时） */
export const FOLLOW_OPTIONS = [
  { label: '不追更', value: 0 },
  { label: '每 6 小时', value: 6 },
  { label: '每 12 小时', value: 12 },
  { label: '每天', value: 24 },
  { label: '每 3 天', value: 72 },
]

export interface ShareStrmConfigResp {
  config: ShareStrmConfig
  media_root: string
  root_abs: string
  /** 根目录不能用的原因（和媒体库重叠等），空 = 能用 */
  root_problem: string
  domain_ok: boolean
  sources: number
}

/** ok 正常 / pending 刚添加 / gone 分享已取消或过期 / badcode 提取码错 / error 其他 */
export type ShareSourceState = 'ok' | 'pending' | 'gone' | 'badcode' | 'error'

export interface ShareSource {
  id: number
  share_code: string
  receive_code: string
  title: string
  account: number
  local_dir: string
  layout: string
  state: ShareSourceState
  share_state: string
  last_error: string
  last_check: string | null
  files: number
  assets: number
  source: string
  created_at: string
  /** 有排着 / 正在跑的生成任务 */
  busy: boolean
  /** 追更间隔（小时），0 = 不追更 */
  follow_hours: number
  next_check: string | null
  paused: boolean
  fail_streak: number
  /** 连续几次查到分享失效；满 3 次认定失效，进待清理 */
  gone_streak: number
  /** 待清理里点了「先留着」 */
  ignored: boolean
  /** 本地整理认出的条目（手动指定的 manual = true） */
  tmdb_id: number
  media_type: string
  media_title: string
  category: string
  manual: boolean
  /** 本地整理没认出的原因（这时按原样放） */
  recog_note: string
  /** 本地整理算出的标题目录（相对分享根） */
  title_dir: string
  /** 点过「转存这份」的时间：整理入库后对应的分享 STRM 会自动收掉 */
  transferred: string | null
}

/** 连续几次查到失效才认定（后端 shareGoneStreakMax） */
export const GONE_CONFIRM = 3

export interface ShareFile {
  id: number
  file_id: string
  sha1: string
  size: number
  name: string
  share_path: string
  rel_path: string
  kind: 'video' | 'asset'
  /** 分享里已经没了，等下一轮确认再删 */
  gone_at: string | null
  /** 在 Emby 里删过，不再生成 */
  blocked: boolean
}

export const config = () => http.get<ShareStrmConfigResp>('/sharestrm/config')
export const saveConfig = (body: ShareStrmConfig) => http.post<{ message: string; config: ShareStrmConfig }>('/sharestrm/config', body)
export const sources = () => http.get<{ sources: ShareSource[] }>('/sharestrm/sources')
export const files = (id: number) =>
  http.get<{ source: ShareSource; files: ShareFile[]; root_abs: string }>(`/sharestrm/sources/${id}/files`)
export const add = (url: string, code = '') =>
  http.post<{ message: string; source: ShareSource; job_id: number }>('/sharestrm/add', { url, code })
export const check = (id: number) => http.post<{ message: string; job_id: number }>(`/sharestrm/sources/${id}/check`)
export const setReceiveCode = (id: number, receive_code: string) =>
  http.put<{ message: string }>(`/sharestrm/sources/${id}`, { receive_code })
export interface ShareUpdate {
  follow_hours?: number
  paused?: boolean
  ignored?: boolean
  layout?: ShareLayout
  /** 手动指定是哪部；tmdb_id 0 = 清掉指定、重新按名字认 */
  pick?: { tmdb_id: number; media_type: string }
}
export const update = (id: number, body: ShareUpdate) =>
  http.put<{ message: string }>(`/sharestrm/sources/${id}`, body)
export const remove = (id: number) => http.del<{ message: string }>(`/sharestrm/sources/${id}`)
export const transfer = (id: number, subdir = '') =>
  http.post<{ message: string }>(`/sharestrm/sources/${id}/transfer`, { subdir }, { timeoutMs: 5 * 60_000 })
export const scrape = (id: number) => http.post<{ message: string; job_id: number }>(`/sharestrm/sources/${id}/scrape`)

// ---- 接管别的项目写的分享 STRM（后端 sharestrmadopt.go） ----
export interface ShareAdoptGroup {
  share_code: string
  dir: string
  files: number
  known: number
  has_code: boolean
  listed: boolean
  problem?: string
}
export interface ShareAdoptReport {
  root: string
  scanned: number
  other: number
  groups: ShareAdoptGroup[]
  adopted?: number
  errors?: string[]
}
export const adopt = (apply: boolean) =>
  http.post<ShareAdoptReport>('/sharestrm/adopt', { apply }, { timeoutMs: 10 * 60_000 })
