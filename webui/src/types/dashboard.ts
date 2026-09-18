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
  movies: number
  tvs: number
  movies_month: number
  tvs_month: number
  total: number
}

export interface StrmCounts {
  total: number
  /** 台账里本地文件已不存在的条数（抽样上限 500，防大库卡顿） */
  invalid: number
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
