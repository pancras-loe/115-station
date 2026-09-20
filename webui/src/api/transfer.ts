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

/** 下载记录：提交过的离线/分享链接，整理入库后带上识别结果 */
export interface DownloadLink {
  id: number
  kind: 'magnet' | 'ed2k' | 'http' | 'ftp' | 'share' | string
  url: string
  name: string
  source: string
  /** 下载/转存侧状态：submitted / downloading / done / failed */
  status: string
  note: string
  /** 整理结果：空 = 还没整理，其余 success / exists / failed / unrecognized */
  organize_status: string
  organized_at: string | null
  /** 对应的整理记录 id，可跳去整理记录页 */
  record_id: number
  tmdb_id: number
  title: string
  year: string
  media_type: string
  poster_path: string
  category: string
  target_dir: string
  created_at: string
}

export interface DownloadLinkPage {
  data: DownloadLink[]
  total: number
  page: number
  size: number
}

/** status 传 all / pending / organized / failed */
export const downloadLinks = (params: { status?: string; q?: string; page?: number; size?: number }) =>
  http.get<DownloadLinkPage>('/download/links', { params })

export const deleteDownloadLink = (id: number) => http.del(`/download/links/${id}`)

export const clearDownloadLinks = (status: string) =>
  http.post<{ message?: string; removed: number }>('/download/links/clear', { status })
