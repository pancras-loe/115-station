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
}

export const offlineTasks = () =>
  http.get<{ data?: OfflineTask[] | { tasks?: OfflineTask[]; list?: OfflineTask[] } }>('/offline/tasks')
