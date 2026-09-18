import { http } from './client'

export interface SyncExtConfig {
  cid: string
  local_path: string
  video_ext: string[]
  image_ext: string[]
  data_ext: string[]
}

export interface FullSyncResult {
  message?: string
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

export const cronPreview = (cron: string) => http.post<{ next: string[] }>('/sync/cron-preview', { cron })

export interface TaskStatus {
  running: boolean
  task?: string
  elapsed?: string
  progress?: string
  recent?: { ok: boolean; name: string; elapsed: string; start: string }[]
}

export const status = () => http.get<TaskStatus>('/sync/status', { timeoutMs: 15_000 })
