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
