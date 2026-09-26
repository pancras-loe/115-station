import { http } from './client'
import type { QueuedReply } from './tasks'

/** 网盘文件页（后端 internal/api/filebrowser.go / filescrape.go / fileorganize.go） */

/** 工作区根目录的角色 */
export type WorkspaceRole = 'library' | 'pending' | 'share' | 'existing' | 'redundant'

export interface FileItem {
  /** 文件是 fid，目录是它自己的 cid */
  id: string
  name: string
  is_dir: boolean
  size?: number
  pickcode?: string
  /** 这个目录本身是哪个工作区根 */
  root?: WorkspaceRole
}

export interface FileList {
  cid: string
  data: FileItem[]
  /** 目录太大，只列了前几千项 */
  truncated?: boolean
  /** 工作区根目录：cid → 角色 */
  roots: Record<string, WorkspaceRole>
}

/** refresh 跳过后端 2 分钟的浏览缓存 */
export const list = (cid: string, refresh = false) =>
  http.get<FileList>('/files/115', { params: refresh ? { cid, refresh: 1 } : { cid } })

export interface Crumb {
  cid: string
  name: string
}

export interface FileJobBody {
  /** 所选条目所在目录 */
  cid: string
  /** 面包屑（不含网盘根）：后端 Cookie 通道取不到祖先链时用它定位 */
  chain: Crumb[]
  items: Pick<FileItem, 'id' | 'name' | 'is_dir' | 'pickcode'>[]
  /** 指定 TMDB 条目（不传 = 自动识别） */
  tmdb_id?: number
  media_type?: 'movie' | 'tv'
  label?: string
}

/** 本次刮削选项：只对这一次生效，不改已保存的刮削配置 */
export interface ScrapeOptions {
  write_nfo: boolean
  write_images: boolean
  force: boolean
  upload: boolean
}

export const scrape = (body: FileJobBody & { scrape: ScrapeOptions }) => http.post<QueuedReply>('/files/scrape', body)

export const organize = (body: FileJobBody) => http.post<QueuedReply>('/files/organize', body)
