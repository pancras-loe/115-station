import { http } from './client'

export interface VersionInfo {
  version: string
  sha?: string
  [k: string]: unknown
}

export const version = () => http.get<VersionInfo>('/version')

/** 更新检测结果（后端 update.go）：只提示，不自更新 */
export interface UpdateStatus {
  current: string
  sha?: string
  latest?: string
  has_update: boolean
  /** 当前版本认得出版本号；dev / 裸提交号的构建比不了 */
  comparable: boolean
  notes?: string
  url?: string
  published_at?: string
  checked_at?: string
  error?: string
  /** 定时检查开关 */
  enabled: boolean
}

/** 读缓存的检测结果，不触发请求 GitHub */
export const updateStatus = () => http.get<UpdateStatus>('/system/update')
/** 立刻查一次（后端一分钟内重复点直接回缓存） */
export const checkUpdate = () => http.post<UpdateStatus>('/system/update/check')

export interface LogsResult {
  logs: string[] | string
  [k: string]: unknown
}

export const logs = (params?: Record<string, unknown>) => http.get<LogsResult>('/system/logs', { params })
export const clearLogs = () => http.post('/system/logs/clear')
export const setLogLevel = (level: string) => http.post('/system/log-level', { level })
