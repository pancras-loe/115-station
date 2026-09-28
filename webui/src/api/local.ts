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
  /** 逐个视频 ffprobe，把轨道写进 NFO（慢） */
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
}

export const scrape = (body: LocalScrapeBody) => http.post<QueuedReply>('/local/scrape', body)
