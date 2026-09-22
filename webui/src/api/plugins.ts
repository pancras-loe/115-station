import { http } from './client'

// ---- 115 每日签到 ----
export interface CheckinConfig {
  enabled: boolean
  cron: string
  [k: string]: unknown
}

export const checkinConfig = () => http.get<CheckinConfig>('/115checkin/config')
export const saveCheckin = (body: { enabled: boolean; cron: string }) =>
  http.post('/115checkin/config', body)
export const runCheckin = () => http.post<{ message?: string }>('/115checkin/run')

// ---- 一键创建 Emby 媒体库 ----
export interface EmbyLibraryItem {
  name: string
  type_label: string
  emby_path: string
  /** true = Emby 里已有同名库，创建时跳过 */
  exists: boolean
}

export interface EmbyLibraryPreview {
  emby_configured: boolean
  data?: EmbyLibraryItem[]
}

export const embyLibraries = () => http.get<EmbyLibraryPreview>('/plugin/emby-libraries')
export const createEmbyLibraries = () =>
  http.post<{ message?: string; created?: number }>('/plugin/emby-libraries', {}, { timeoutMs: 120_000 })

// ---- 媒体库封面生成 ----
export interface CoverGenConfig {
  cron: string
  style: string
  strategy: string
  blacklist: string
  advanced: string
}

export const coverGenConfig = () => http.get<{ data?: Partial<CoverGenConfig> }>('/covergen/config')
export const saveCoverGen = (body: CoverGenConfig) => http.post('/covergen/config', body)
export const runCoverGen = () => http.post<{ message?: string; warnings?: string[] }>('/covergen/run', undefined, { timeoutMs: 30 * 60_000 })
export const coverGenList = () =>
  http.get<{ data?: { name: string; time?: string }[] }>('/covergen/list')

/** 封面预览图直接走 <img src>，带时间戳绕开浏览器缓存（重新生成后要能立刻看到） */
export const coverPreviewUrl = (name: string) =>
  `/api/covergen/preview?name=${encodeURIComponent(name)}&t=${Date.now()}`
