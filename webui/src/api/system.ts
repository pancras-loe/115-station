import { http } from './client'

export interface VersionInfo {
  version: string
  [k: string]: unknown
}

export const version = () => http.get<VersionInfo>('/version')

export interface LogsResult {
  logs: string[] | string
  [k: string]: unknown
}

export const logs = (params?: Record<string, unknown>) => http.get<LogsResult>('/system/logs', { params })
export const clearLogs = () => http.post('/system/logs/clear')
export const setLogLevel = (level: string) => http.post('/system/log-level', { level })
