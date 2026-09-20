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

export const getScrapeConfig = () => http.get<{ data?: Partial<ScrapeConfig> } & Partial<ScrapeConfig>>('/scrape/config')
export const saveScrapeConfig = (cfg: ScrapeConfig) => http.post('/scrape/config', cfg)
export const runScrape = () => http.post('/scrape/run')
export const stopScrape = () => http.post('/scrape/stop')
export const scrapeStatus = () =>
  http.get<{ running?: boolean; progress?: string }>('/scrape/status', { timeoutMs: 15_000 })

// ---- 整理记录 ----
export interface OrganizeRecordFile {
  fid: string
  name: string
  kind: 'video' | 'subtitle' | 'meta' | 'junk'
  pickcode?: string
  size?: number
  sha1?: string
}

export type OrganizeRecordStatus = 'success' | 'exists' | 'failed' | 'unrecognized'

export interface OrganizeRecord {
  id: number
  batch_id: string
  source: string
  source_fid: string
  source_kind: 'dir' | 'file'
  status: OrganizeRecordStatus
  /** 失败发生在哪一步：recognize / move / strm / scrape */
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

/** status 传 'problem' 取「失败 + 未识别」，前端最常用的一档 */
export const listRecords = (params: { status?: string; q?: string; page?: number; size?: number }) =>
  http.get<OrganizeRecordPage>('/organize/records', { params })

export const deleteRecord = (id: number) => http.del(`/organize/records/${id}`)

export const clearRecords = (status: string) =>
  http.post<{ message?: string; removed: number }>('/organize/records/clear', { status })

/** 重新整理会动网盘与本地文件，和整理/同步互斥，耗时按分钟计 */
export const redoRecord = (id: number, tmdbId: number, mediaType: string) =>
  http.post<{ message?: string; data: OrganizeRecord }>(
    `/organize/records/${id}/redo`,
    { tmdb_id: tmdbId, media_type: mediaType },
    { timeoutMs: 30 * 60_000 },
  )
