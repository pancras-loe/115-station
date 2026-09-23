/** GET /api/dashboard —— 字段与 internal/api/dashboard.go 的 DashboardEnhanced 一一对应 */

export interface StorageInfo {
  /** Cookie 失效或未配置时后端只返回 { enabled: false } */
  enabled?: false
  username?: string
  used?: number
  total?: number
  used_h?: string
  total_h?: string
}

export interface MediaCounts {
  /** 展示口径：Emby 可用时就是 Emby 的数字，否则是本地整理台账 */
  movies: number
  tvs: number
  total: number
  /** Emby 的剧集单集数；本地口径下没有这个字段 */
  episodes?: number
  movies_month: number
  tvs_month: number
  /** 本地整理台账自己的数字，始终返回，用来和 Emby 对账 */
  local_movies: number
  local_tvs: number
  source: 'emby' | 'local'
}

export interface StrmCounts {
  total: number
  /** 网盘源文件已删（全量同步打的 orphan_at 标记，即「失效 STRM」） */
  orphan: number
  /** 台账有行但本地 .strm 已不在；只抽查最近 missing_sampled 条 */
  missing: number
  missing_sampled: number
  active: number
}

export interface RecentMedia {
  title: string
  year: string
  category: string
  type: string
  poster: string
  at: string
}

export interface WeeklyPoint {
  day: string
  count: number
}

export interface CategoryCard {
  name: string
  count: number
  posters: string[]
}

export interface SysStat {
  mem_total_mb?: number
  mem_used_mb?: number
  mem_percent?: number
  cpu_percent?: number
}

export interface EmbyLibrary {
  name: string
  count: number
  collage: string[] | null
  /** Emby 的 CollectionType（movies / tvshows / …）与它的中文标签 */
  type?: string
  type_label?: string
}

export interface EmbyDashboard {
  counts: { movies: number; series: number; episodes: number }
  recent: { id: string; name: string; year: string }[]
  libraries: EmbyLibrary[]
}

export interface Dashboard {
  /** Emby 未配置或不可达时为 null */
  emby: EmbyDashboard | null
  storage: StorageInfo
  media: MediaCounts
  strm: StrmCounts
  synced_files: number
  organized: number
  recent_media: RecentMedia[]
  weekly: WeeklyPoint[]
  week_total: number
  categories: CategoryCard[]
  sys: SysStat
  pending_events: number
}

/** POST /api/media-library/calibrate —— 见 internal/api/medialib.go */
export interface CalibrateSample {
  title: string
  year: string
  type: string
  category: string
  target_path: string
}

export interface CalibrateResult {
  /** false = 只是预演，一条都没删 */
  applied: boolean
  local_root: string
  total: number
  /** 本地已经找不到落点的台账行数 */
  stale: number
  kept: number
  /** 台账里没记落点、无法判断的，一律保留 */
  skipped: number
  removed: number
  sample: CalibrateSample[]
  /** 本地 STRM 树实际数出来的部数，按媒体库分类目录分组 */
  libraries: { name: string; count: number }[]
}
