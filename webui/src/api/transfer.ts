import { http } from './client'

export interface TransferPayload {
  url: string
  code: string
  target_cid: string
  /** 转存/下载完成后是否自动识别入库并生成 STRM */
  organize: boolean
}

/** 115 分享链接转存 */
export const shareReceive = (body: TransferPayload) =>
  http.post<{ message?: string }>('/share/receive', body, { timeoutMs: 5 * 60_000 })

/** 磁力 / ed2k / HTTP 离线下载 */
export const offlineAdd = (body: TransferPayload) =>
  http.post<{ message?: string }>('/offline/add', body, { timeoutMs: 5 * 60_000 })

export interface OfflineTask {
  name?: string
  task_name?: string
  /** -1 失败，1 下载中，2 完成，其余为等待 */
  status: number
  percent?: number | string
  size?: number | string
  /** 完成时间，秒级时间戳 */
  del_time?: number | string
  /** 提交这条任务的原始链接：115 任务列表自带，缺失时由下载链接台账兜底 */
  url?: string
  /** magnet / ed2k / http / ftp */
  link_kind?: string
  /** 下载产物在转存目录里的 fid */
  file_id?: string
  /** 提交时间，秒级时间戳 */
  add_time?: number | string
}

export const offlineTasks = () =>
  http.get<{ data?: OfflineTask[] | { tasks?: OfflineTask[]; list?: OfflineTask[] } }>('/offline/tasks')
