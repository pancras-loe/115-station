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

/** 整理是长任务，和同步互斥（后端共用 fullSyncMu） */
export const runPipeline = () =>
  http.post<PipelineResult>('/organize/pipeline', { sync_after: true }, { timeoutMs: 60 * 60_000 })

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
