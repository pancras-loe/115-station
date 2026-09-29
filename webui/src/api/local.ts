import { http } from './client'
import type { QueuedReply } from './tasks'

/** 本地文件页（后端 internal/api/locallib.go / localscrape.go） */

/** ok = NFO 与海报都有；partial = 缺一样；miss = 都没有 */
export type LocalTitleStatus = 'ok' | 'partial' | 'miss'

export interface LocalTitle {
  /** 台账片目 key（含库名前缀）：刮削提交的就是它 */
  key: string
  title: string
  year?: string
  tmdb_id?: number
  media_type: 'movie' | 'tv'
  category: string
  videos: number
  has_nfo: boolean
  has_poster: boolean
  status: LocalTitleStatus
  /** 台账有、本地没有这个目录 */
  missing?: boolean
  last_at: string
  /** 海报缩略图查询串，用 posterUrl() 拼 */
  poster?: string
}

export interface LocalTitleStats {
  all: number
  ok: number
  partial: number
  miss: number
  movie: number
  tv: number
}

export interface LocalTitleList {
  /** 没配本地媒体库根目录时为 false */
  configured: boolean
  root?: string
  items: LocalTitle[]
  total: number
  offset?: number
  limit?: number
  stats: LocalTitleStats
  /** 台账有、本地目录不见了的片目数 */
  missing?: number
}

export type LocalTitleSort = 'added_desc' | 'title' | 'year_desc' | 'year_asc'

export interface LocalTitleQuery {
  q?: string
  type?: '' | 'movie' | 'tv'
  status?: '' | LocalTitleStatus
  sort?: LocalTitleSort
  offset?: number
  limit?: number
  /** 跳过后端 30 秒的列表缓存 */
  refresh?: boolean
  /** 一次返回全部筛选结果（忽略 offset / limit），「全选筛选结果」用 */
  all?: boolean
}

export const listTitles = (q: LocalTitleQuery) =>
  http.get<LocalTitleList>('/local/titles', {
    params: {
      q: q.q || undefined,
      type: q.type || undefined,
      status: q.status || undefined,
      sort: q.sort || undefined,
      offset: q.offset || undefined,
      limit: q.limit || undefined,
      refresh: q.refresh ? 1 : undefined,
      all: q.all ? 1 : undefined,
    },
  })

/** <img> 带不了登录态：后端按 key 签了名，这条路由公开 */
export const posterUrl = (t: LocalTitle) => (t.poster ? `/api/local/poster?${t.poster}` : '')

/** 本次刮削选项：只对这一次生效，不改已保存的刮削配置 */
export interface ScrapeOptions {
  write_nfo: boolean
  write_images: boolean
  force: boolean
  /** 这一次直接传进网盘；不勾时交给监控上传 */
  upload: boolean
  /** 刮完让 Emby 给还没有媒体信息的视频提前探测（每个一次 115 直链请求；与入库后的自动探测共用记账） */
  probe: boolean
  /** 同一季多集共用的剧照判为占位图，不写 */
  skip_shared_stills: boolean
}

export interface LocalScrapeBody {
  keys: string[]
  scrape: ScrapeOptions
  /** 指定 TMDB 条目（只能单选一部） */
  tmdb_id?: number
  media_type?: 'movie' | 'tv'
  label?: string
  /** 开着轨道探测（Emby 提前探测）且所选视频超过 100 个时，用户确认过两次才带 */
  confirm_probe?: boolean
}

export const scrape = (body: LocalScrapeBody) => http.post<QueuedReply>('/local/scrape', body)

// ---- 片目详情（后端 internal/api/localdetail.go）----

/** 片目里的一个刮削产物 */
export interface LocalFile {
  name: string
  label?: string
  exists: boolean
  /** 缺了不算没刮全：TMDB 上不一定有这张图，集剧照还可能被判成占位图 */
  optional?: boolean
  size?: number
  mod_at?: string
}

/** 片目里的一个视频 */
export interface LocalEntry {
  /** STRM 基名（不带 .strm） */
  name: string
  /** 相对标题目录（含 .strm） */
  rel: string
  season?: number
  /** 剧集：0 / 缺省 = 解析不出集号，刮削跳过它 */
  episode?: number
  /** 网盘上的视频大小 */
  size?: number
  /** 网盘上已经没有这个文件（失效 STRM） */
  orphan?: boolean
  /** 台账有、本地 STRM 不在 */
  strm_missing?: boolean
  nfo: LocalFile
  /** 只有剧集有 */
  thumb?: LocalFile
  subtitles?: string[]
}

export interface LocalSeason {
  season: number
  /** 季目录；空 = 集文件平铺或混季，不写 season.nfo */
  dir?: string
  nfo?: LocalFile
  poster: LocalFile
  videos: number
}

export interface LocalTitleDetail extends LocalTitle {
  dir: string
  /** 背景图查询串，用 fanartUrl() 拼 */
  fanart?: string
  files: LocalFile[]
  seasons?: LocalSeason[]
  entries: LocalEntry[]
  summary: {
    nfo_have: number
    nfo_total: number
    img_have: number
    img_total: number
    thumb_have: number
    thumb_total: number
    subtitled: number
  }
}

export const titleDetail = (key: string) => http.get<LocalTitleDetail>('/local/titles/detail', { params: { key } })

export const fanartUrl = (d: { fanart?: string }) => (d.fanart ? `/api/local/poster?${d.fanart}` : '')

/**
 * 提前探测状态：done 已有媒体信息 / none 没探测过 / queued 排队中 / running 探测中 /
 * retry 上次失败、可以再试 / wait 近期请求过、到 retry_at 才能再试 / exhausted 次数用完 / disc 光盘结构不探测
 */
export type ProbeState = 'done' | 'none' | 'queued' | 'running' | 'retry' | 'wait' | 'exhausted' | 'disc'

export interface EmbyTrack {
  codec?: string
  profile?: string
  language?: string
  lang_name?: string
  display?: string
  title?: string
  width?: number
  height?: number
  bitrate?: number
  bit_depth?: number
  fps?: number
  range?: string
  channels?: number
  layout?: string
  default?: boolean
  forced?: boolean
  external?: boolean
}

export interface EmbyDetailItem {
  id: string
  name: string
  type: string
  season?: number
  episode?: number
  rel?: string
  container?: string
  size?: number
  bitrate?: number
  /** 秒 */
  runtime?: number
  video: EmbyTrack[]
  audio: EmbyTrack[]
  subtitles: EmbyTrack[]
  has_info: boolean
  probe: {
    state: ProbeState
    attempts?: number
    last_at?: string
    last_err?: string
    retry_at?: string
  }
}

export interface ProbeLimits {
  max_attempts: number
  retry_hours: number
  break_after: number
  break_minutes: number
  prune_days: number
  gap_seconds: number
}

export interface TitleEmby {
  /** 配了 Emby 没有 */
  configured: boolean
  /** Emby 里有没有这个片目 */
  found: boolean
  error?: string
  /** 入库后自动探测（影视刮削「轨道探测」）开着没有 */
  auto_probe: boolean
  limits: ProbeLimits
  items: EmbyDetailItem[]
  counts: Partial<Record<ProbeState, number>>
}

export const titleEmby = (key: string) => http.get<TitleEmby>('/local/titles/emby', { params: { key } })

export interface ProbeReply {
  queued: number
  held: number
  message: string
}

export const probeTitle = (key: string, confirm = false) =>
  http.post<ProbeReply>('/local/titles/probe', { key, confirm })
