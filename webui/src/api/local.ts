import { http } from './client'
import type { QueuedReply } from './tasks'
import { embyImageUrl } from '@/utils/media'

/** 海报墙（后端 internal/api/locallib.go / localscrape.go） */

/**
 * 本站刮削：ok = 必需产物（各级 NFO、海报、背景图）都在；partial = 缺一部分；miss = 一样都没有。
 * Emby 刮削：ok = Emby 认出了条目且海报、背景图都有；partial = 缺一部分或认错了条目；miss = Emby 里没有；
 * pending = Emby 快照还没读到（只有 Emby 刮削时出现，不进三档计数）
 */
export type LocalTitleStatus = 'ok' | 'partial' | 'miss' | 'pending'

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
  /** 缺的必需产物（「背景图」「3 集 NFO」…），角标直接写它 */
  lack?: string[]
  /** 缺的可选图片（剧照、季海报）：只提示，不影响 status */
  soft?: string[]
  /** Emby 媒体信息（打开页面时拉的快照）；没有快照或 Emby 里没这个片目时为空 */
  emby?: LocalEmbyStat
  /** 台账有、本地没有这个目录 */
  missing?: boolean
  last_at: string
  /** 海报缩略图查询串，用 posterUrl() 拼 */
  poster?: string
  /** 用 Emby 的海报（片目级条目 id）：后端已决定用不用它，有就优先 */
  emby_poster?: string
  /** Emby 刮削时：Emby 认成的 TMDB 编号与台账不一致 */
  mismatch?: string
}

export interface LocalTitleStats {
  all: number
  ok: number
  partial: number
  miss: number
  movie: number
  tv: number
  /** Emby 里还缺媒体信息的片目数（套用其余筛选）；没有 Emby 快照时为 0 */
  probe_lack: number
}

export interface LocalEmbyStat {
  /** Emby 里这个片目的影视条目数 */
  items: number
  /** 其中还缺媒体信息（没探测过）、且能探测的 */
  lack: number
  /** 片目级条目（电影的 Movie、剧集的 Series）；空 = Emby 没把它认成影视条目 */
  item_id?: string
  tmdb?: string
  imdb?: string
  poster?: boolean
  backdrop?: boolean
}

/** GET /local/titles/emby-stats：打开页面时向 Emby 拉一次快照（只打 Emby，零 115 请求） */
export interface LocalEmbyStats {
  configured: boolean
  ready?: boolean
  /** 后台正在拉新快照：隔一会儿再问，拉完原地刷新卡片 */
  refreshing?: boolean
  at?: string
  error?: string
  /** 读到的 Emby 影视条目数 */
  scanned?: number
  /** 对上了本地片目的片目数 */
  titles?: number
  /** 缺媒体信息的片目数 */
  lacking?: number
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
  /** lack = 只看 Emby 里还缺媒体信息的 */
  probe?: '' | 'lack'
  sort?: LocalTitleSort
  offset?: number
  limit?: number
  /** 跳过后端 30 秒的列表缓存 */
  refresh?: boolean
  /** 一次返回全部筛选结果（忽略 offset / limit），批量栏的全选框用 */
  all?: boolean
}

export const listTitles = (q: LocalTitleQuery) =>
  http.get<LocalTitleList>('/local/titles', {
    params: {
      q: q.q || undefined,
      type: q.type || undefined,
      status: q.status || undefined,
      probe: q.probe || undefined,
      sort: q.sort || undefined,
      offset: q.offset || undefined,
      limit: q.limit || undefined,
      refresh: q.refresh ? 1 : undefined,
      all: q.all ? 1 : undefined,
    },
  })

export const embyStats = (refresh = false) =>
  // 后端只登记、在后台拉，立即返回
  http.get<LocalEmbyStats>('/local/titles/emby-stats', { params: { refresh: refresh ? 1 : undefined } })

/**
 * <img> 带不了登录态：后端按 key 签了名，这条路由公开。
 * w = 需要的像素宽度（显示宽度 × 设备像素比），后端往上取档；不给就是最大档 400
 */
export const posterUrl = (t: { poster?: string; emby_poster?: string }, w?: number) => {
  if (t.emby_poster) return embyImageUrl(`Items/${t.emby_poster}/Images/Primary`, Math.ceil(w || 400))
  return t.poster ? `/api/local/poster?${t.poster}${w ? `&w=${Math.ceil(w)}` : ''}` : ''
}

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
  /** 用 Emby 的背景图（片目级条目 id），有就优先 */
  emby_backdrop?: string
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

export const fanartUrl = (d: { fanart?: string; emby_backdrop?: string }) => {
  if (d.emby_backdrop) return embyImageUrl(`Items/${d.emby_backdrop}/Images/Backdrop`, 960)
  return d.fanart ? `/api/local/poster?${d.fanart}` : ''
}

export interface EmbyRefreshReply {
  message: string
  refreshed: number
  failed: number
  /** Emby 里没有对应影视条目的片目 */
  missing?: string[]
}

/** 让 Emby 重新刮这些片目（刮削方式为 Emby 时代替「刮削」）；replace = 替换全部元数据与图片 */
export const embyRefresh = (keys: string[], replace: boolean) =>
  http.post<EmbyRefreshReply>('/local/titles/emby-refresh', { keys, replace })

/**
 * 提前探测状态（按自动探测的规则说；手动能不能点看 manual_at）：done 已有媒体信息 / none 没探测过 /
 * queued 排队中 / running 探测中 / retry 上次失败、自动还会再试一次 / wait 自动冷却中、到 retry_at 才会再试 /
 * exhausted 自动次数用完、不再自动探 / disc 光盘结构不探测
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
    /** 自动入口冷却中：最早什么时候会再自动试 */
    retry_at?: string
    /** 防抖没过：这个时间之后才能手动请求；为空 = 现在就能手动请求 */
    manual_at?: string
    /** 在任务中心忽略了：state 按 exhausted 报，不再自动探 */
    ignored?: boolean
  }
}

export interface ProbeLimits {
  max_attempts: number
  retry_hours: number
  break_after: number
  break_minutes: number
  /** 手动请求的防抖（分钟） */
  debounce_minutes: number
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
  /** 现在点「提前探测」会真正请求的视频数 */
  manual?: number
  /** 还缺媒体信息、但刚请求过（防抖中）的视频数；manual_at 是其中最早能再请求的时间 */
  debounce?: number
  manual_at?: string
  /** 这个片目排着 / 正在跑的探测任务 */
  job_id?: number
}

export const titleEmby = (key: string) => http.get<TitleEmby>('/local/titles/emby', { params: { key } })

export interface ProbeReply {
  queued: number
  held: number
  message: string
  /** 建出来的探测任务（queued > 0 时） */
  job_id?: number
}

export const probeTitle = (key: string, confirm = false) =>
  http.post<ProbeReply>('/local/titles/probe', { key, confirm })

/** Emby 与本地片目对账（GET /local/titles/reconcile）：只打 Emby，零 115 请求 */
export interface ReconEntry {
  name: string
  year?: number
  /** Emby 里的路径 */
  path?: string
  /** 换算到本地的路径 */
  local?: string
  /** 对上的台账片目 */
  key?: string
  /** split：这个片目在 Emby 里有几个条目 */
  count?: number
}
export interface ReconBucket {
  count: number
  items: ReconEntry[]
}
export interface ReconDir {
  dir: string
  count: number
  samples: string[]
}
/** Emby − 台账 = outside + stale + uncategorized + unledgered + type_mismatch + split_extra − missing */
export interface ReconSide {
  emby: number
  ledger: number
  matched: number
  outside: ReconBucket
  stale: ReconBucket
  uncategorized: ReconBucket
  uncategorized_dirs: ReconDir[] | null
  unledgered: ReconBucket
  type_mismatch: ReconBucket
  split: ReconBucket
  split_extra: number
  missing: ReconBucket
}
export interface EmbyPathSuggest {
  configured: boolean
  local_root?: string
  /** 当前填的 Emby 媒体库目录 */
  current?: string
  /** 推算出的 Emby 媒体库目录；空 = 推不出来 */
  suggest?: string
  evidence?: { location: string; local: string }[]
  libraries?: { name: string; locations: string[]; under_root: boolean }[]
  covered?: number
  error?: string
}
export interface ReconcileResult {
  configured: boolean
  local_root?: string
  result?: { mapping_ok: boolean; scanned: number; movie: ReconSide; tv: ReconSide }
  /** 映射对不上时才有 */
  mapping?: EmbyPathSuggest
}

// 全服务器的 Movie + Series 只读路径，几千条几秒；给足余量，别让大库撞上默认 60 秒
export const reconcile = () => http.get<ReconcileResult>('/local/titles/reconcile', { timeoutMs: 180_000 })

export const embyPathSuggest = () => http.get<EmbyPathSuggest>('/emby/path-suggest')
