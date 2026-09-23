import { http } from './client'

export interface PipelineStep {
  step: string
  status: string
  message: string
}

export interface PipelineDetail {
  file_name: string
  status: 'success' | 'exists' | string
  message: string
}

export interface PipelineResult {
  message?: string
  steps?: PipelineStep[]
  shows?: { title: string; year?: string; target: string }[]
  details?: PipelineDetail[]
}

/**
 * 整理是长任务，和同步互斥（后端共用 fullSyncMu）。
 * 无请求体：整理自带落盘（STRM + 刮削 + 刷 Emby），不再有「整理后是否同步」这个开关。
 */
export const runPipeline = () =>
  http.post<PipelineResult>('/organize/pipeline', undefined, { timeoutMs: 60 * 60_000 })

// ---- 二级分类 / 洗版规则（YAML） ----
export const getCategories = () => http.get<{ config?: string }>('/scrape/categories')
export const saveCategories = (yaml: string) => http.post('/scrape/categories', { yaml })

export const getWash = () => http.get<{ config?: string }>('/scrape/wash')
export const saveWash = (yaml: string) => http.post('/scrape/wash', { yaml })

// ---- 影视刮削 ----
export interface ScrapeConfig {
  local_root: string
  write_nfo: boolean
  write_images: boolean
  force: boolean
  auto_after_organize: boolean
}

/** 后端回的是 { cfg, status } 两层结构，不是扁平配置 —— 摊平取会全部读成 undefined */
export const getScrapeConfig = () =>
  http.get<{ cfg?: Partial<ScrapeConfig>; status?: { running?: boolean } }>('/scrape/config')
export const saveScrapeConfig = (cfg: ScrapeConfig) => http.post('/scrape/config', cfg)
export const runScrape = () => http.post('/scrape/run')
export const stopScrape = () => http.post('/scrape/stop')
export const scrapeStatus = () =>
  http.get<{ running?: boolean; progress?: string }>('/scrape/status', { timeoutMs: 15_000 })

// ---- 整理记录 ----
export interface OrganizeRecordFile {
  fid: string
  name: string
  /** 重命名之前的原名（与 name 相同时后端不存） */
  orig?: string
  kind: 'video' | 'subtitle' | 'meta' | 'junk'
  pickcode?: string
  size?: number
  sha1?: string
}

/** awaiting = 开了「人工确认」后识别完停下来的条目，文件还在待整理里原地没动 */
export type OrganizeRecordStatus = 'success' | 'exists' | 'failed' | 'unrecognized' | 'awaiting'

export interface OrganizeRecord {
  id: number
  batch_id: string
  source: string
  source_fid: string
  source_kind: 'dir' | 'file'
  source_cid: string
  status: OrganizeRecordStatus
  /** 失败发生在哪一步：recognize / move / strm / scrape / confirm */
  stage: string
  message: string
  tmdb_id: number
  title: string
  year: string
  media_type: string
  poster_path: string
  category: string
  target_dir: string
  target_cid: string
  video_count: number
  total_size: number
  strm_created: number
  scrape_state: string
  scrape_msg: string
  manual_tmdb: boolean
  redo_count: number
  created_at: string
  file_list: OrganizeRecordFile[]
}

export interface OrganizeRecordPage {
  data: OrganizeRecord[]
  total: number
  page: number
  size: number
}

/** status 传 'problem' 取「失败 + 未识别」；type 传 movie / tv；q 纯数字时也按 TMDB ID 匹配 */
export const listRecords = (params: {
  status?: string
  type?: string
  q?: string
  page?: number
  size?: number
}) => http.get<OrganizeRecordPage>('/organize/records', { params })

/** 各状态条数：all / awaiting / problem / success / exists / failed / unrecognized */
export const recordStats = () =>
  http.get<{ data: Record<string, number> }>('/organize/records/stats', { timeoutMs: 15_000 })

/**
 * 确认入库：按识别结果（或改指定的条目）走完后半条流水线。
 * 和整理互斥，一部剧上百集时耗时按分钟计
 */
export const confirmRecord = (id: number, pick?: { tmdbId: number; mediaType: string }) =>
  http.post<{ message?: string; data: OrganizeRecord }>(
    `/organize/records/${id}/confirm`,
    pick ? { tmdb_id: pick.tmdbId, media_type: pick.mediaType } : {},
    { timeoutMs: 30 * 60_000 },
  )

/** 批量按识别结果入库；没识别出来的由后端跳过 */
export const confirmRecords = (ids: number[]) =>
  http.post<{ message?: string; success: number; total: number }>(
    '/organize/records/confirm',
    { ids },
    { timeoutMs: 60 * 60_000 },
  )

/** 不要这一条：移到冗余，记录改成未识别（之后仍能「重新整理」捞回） */
export const ignoreRecord = (id: number) =>
  http.post<{ message?: string }>(`/organize/records/${id}/ignore`, {}, { timeoutMs: 5 * 60_000 })

export const deleteRecord = (id: number) => http.del(`/organize/records/${id}`)

export const clearRecords = (status: string) =>
  http.post<{ message?: string; removed: number }>('/organize/records/clear', { status })

/**
 * 深度删除：删掉这条记录整理出来的**网盘源文件**（进 115 回收站），
 * 连同本地 STRM/附属与台账。和上面的 deleteRecord（只删记录）是两回事。
 */
export const deepDeleteRecord = (id: number) =>
  http.post<{
    message?: string
    removed: number
    videos: number
    assets: number
    pan_dirs: number
    skipped: number
  }>(`/organize/records/${id}/deep-delete`, {}, { timeoutMs: 10 * 60_000 })

/** 重新整理会动网盘与本地文件，和整理/同步互斥，耗时按分钟计 */
export const redoRecord = (id: number, tmdbId: number, mediaType: string) =>
  http.post<{ message?: string; data: OrganizeRecord }>(
    `/organize/records/${id}/redo`,
    { tmdb_id: tmdbId, media_type: mediaType },
    { timeoutMs: 30 * 60_000 },
  )
