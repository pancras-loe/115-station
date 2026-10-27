import { ApiError, http, tokenStorage } from './client'

export interface VersionInfo {
  version: string
  sha?: string
  [k: string]: unknown
}

export const version = () => http.get<VersionInfo>('/version')

/** 一键更新的进度（后端 selfupdate.go，内存，重启即清） */
export interface UpdateApply {
  phase: 'downloading' | 'restarting' | 'failed'
  target: string
  done?: number
  total?: number
  error?: string
  by?: string
  started: string
}

/** 更新检测结果（后端 update.go）；一键更新在 selfupdate.go */
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
  /** 这个部署能一键更新（在本站镜像里跑、能重启） */
  can_apply: boolean
  /** 不能一键更新的原因 */
  apply_block?: string
  /** 一键更新进行中 / 上一次失败 */
  apply?: UpdateApply
  /** 当前跑的是一键更新下载的版本时，镜像自带的版本 */
  image_version?: string
  /** 下载的版本启动不起来、退回了镜像自带的 */
  last_failed?: string
}

/** 读缓存的检测结果，不触发请求 GitHub */
export const updateStatus = () => http.get<UpdateStatus>('/system/update')
/** 立刻查一次（后端一分钟内重复点直接回缓存） */
export const checkUpdate = () => http.post<UpdateStatus>('/system/update/check')
/** 一键更新到 version（必须是检测到的最新版本）：后台下载校验，好了自动重启 */
export const applyUpdate = (version: string) => http.post<UpdateStatus>('/system/update/apply', { version })

export interface LogsResult {
  logs: string[] | string
  [k: string]: unknown
}

export const logs = (params?: Record<string, unknown>) => http.get<LogsResult>('/system/logs', { params })
export const clearLogs = () => http.post('/system/logs/clear')
export const setLogLevel = (level: string) => http.post('/system/log-level', { level })

/** 备份与恢复（后端 backup.go）。恢复只暂存，重启后才换进去 */
export interface BackupItem {
  name: string
  size: number
  created_at: string
  /** manual 手动 / auto 定时 / prerestore 恢复前自动留的 */
  kind: 'manual' | 'auto' | 'prerestore'
}
export interface RestorePending {
  source: string
  staged_at: string
  safety_backup?: string
  manifest: { version?: string; created_at?: string; kind?: string; has_db: boolean; files?: string[] }
}
export interface BackupList {
  items: BackupItem[] | null
  dir: string
  pending: RestorePending | null
  can_restart: boolean
  last_restore?: { at: string; ok: boolean; source: string; error?: string }
  last_auto?: { at: string; ok: boolean; error?: string }
  next_auto?: string
}
export const backups = () => http.get<BackupList>('/system/backups')
/** 打包要 VACUUM 一遍数据库，台账大时要十几秒 */
export const createBackup = () => http.post<BackupItem>('/system/backups', undefined, { timeoutMs: 0 })
export const deleteBackup = (name: string) => http.del(`/system/backups/${encodeURIComponent(name)}`)
export const restoreBackup = (name: string) =>
  http.post<RestorePending>(`/system/backups/${encodeURIComponent(name)}/restore`, undefined, { timeoutMs: 0 })
export const cancelRestore = () => http.del('/system/restore')
export const restart = () => http.post('/system/restart')

/** 上传备份包暂存恢复。走裸 fetch：要 multipart，不能带 JSON 的 Content-Type */
export async function uploadRestore(file: File): Promise<RestorePending> {
  const fd = new FormData()
  fd.append('file', file)
  const token = tokenStorage.get()
  const res = await fetch('/api/system/restore', {
    method: 'POST',
    body: fd,
    headers: token ? { Authorization: `Bearer ${token}` } : {},
  })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new ApiError(data.error || data.message || '上传失败', res.status, data)
  return data as RestorePending
}

/** 下载要带登录态，<a href> 带不了 Authorization，只能取成 blob 再存 */
export async function downloadBackup(name?: string) {
  const token = tokenStorage.get()
  const url = name ? `/api/system/backups/${encodeURIComponent(name)}` : '/api/system/backup'
  const res = await fetch(url, { headers: token ? { Authorization: `Bearer ${token}` } : {} })
  if (!res.ok) {
    const data = await res.json().catch(() => ({}))
    throw new ApiError(data.error || '下载失败', res.status, data)
  }
  const blob = await res.blob()
  const cd = res.headers.get('Content-Disposition') || ''
  const fname = name || /filename=([^;]+)/.exec(cd)?.[1] || '115-station-backup.zip'
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = fname
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(a.href), 10_000)
}
