import { http } from './client'

export type FullSyncMode = 'normal' | 'fast'

/** 同步接口的请求体（/sync/full 与 /sync/incremental 共用） */
export interface SyncExtConfig {
  cid: string
  local_path: string
  video_ext: string[]
  image_ext: string[]
  data_ext: string[]
  /** 全量同步模式；增量同步忽略此字段 */
  mode?: FullSyncMode
}

/** setting「full」的持久化结构（与后端 fullSyncCfg 同构） */
export interface FullSyncConfig {
  cid: string
  /** 仅用于配置页回显；同步仍以 cid 为准 */
  cid_path?: string
  local_path: string
  video_ext: string[]
  image_ext: string[]
  data_ext: string[]
  mode: FullSyncMode
  /** 失效 STRM 检测：全量同步后标出「本地还在、网盘已删」的条目，只打标不删 */
  detect_orphans: boolean
  /** 定时全量开关。只在 detect_orphans 打开时生效，后端同样按此判定 */
  cron_enabled: boolean
  cron: string
}

export interface FullSyncResult {
  message?: string
  /** 实际使用的模式：选了 fast 但接口失败时会降级为 normal */
  mode_used?: FullSyncMode
  /** 本次清单是否完整；false 时后端会跳过失效 STRM 标记 */
  scan_complete?: boolean
  /** 当前待清理的失效 STRM 数 */
  orphans?: number
  total: number
  created: number
  assets_total: number
  assets_downloaded: number
  assets_skipped: number
  assets_failed: number
}

/** 全量同步是长任务（受 115 节流限制可能数分钟），必须放宽超时 */
export const runFull = (body: SyncExtConfig) =>
  http.post<FullSyncResult>('/sync/full', body, { timeoutMs: 30 * 60_000 })

export interface IncrSummary {
  events_total: number
  events_fresh: number
  relevant: number
  structural: number
  deleted: number
  moved: number
  dirs: number
  dirs_skipped: number
  videos: number
  strm_created: number
  assets_total: number
  assets_downloaded: number
  assets_skipped: number
  assets_failed: number
  /** 非媒体库区域（待整理/已存在/冗余等）的事件 */
  ignored: number
  elapsed: string
}

export const runIncremental = (body: SyncExtConfig) =>
  http.post<{ message?: string; summary: IncrSummary }>('/sync/incremental', body, {
    timeoutMs: 30 * 60_000,
  })

export interface SyncCapabilities {
  fast_available: boolean
  /** 不可用时的原因，直接展示给用户 */
  reason: string
}

export const capabilities = () => http.get<SyncCapabilities>('/sync/capabilities', { timeoutMs: 15_000 })

export interface OrphanEntry {
  rel_path: string
  kind: string
  size: number
  marked_at: string
}

/** 失效 STRM 报告（接口沿用 orphan 命名，界面一律叫「失效 STRM」） */
export interface OrphanReport {
  enabled: boolean
  total: number
  ledger_total: number
  /** 失效条目占台账总数的比例，异常偏高时前端要拦一下 */
  ratio: number
  sample: OrphanEntry[]
  sample_limit: number
}

export const orphans = () => http.get<OrphanReport>('/sync/orphans', { timeoutMs: 15_000 })

export const cleanOrphans = () =>
  http.post<{ message?: string; removed: number; missing: number; failed: number }>(
    '/sync/orphans/clean',
    {},
    { timeoutMs: 10 * 60_000 },
  )

export const cronPreview = (cron: string) => http.post<{ next: string[] }>('/sync/cron-preview', { cron })

export interface TaskStatus {
  running: boolean
  task?: string
  elapsed?: string
  progress?: string
  recent?: { ok: boolean; name: string; elapsed: string; start: string }[]
}

export const status = () => http.get<TaskStatus>('/sync/status', { timeoutMs: 15_000 })
