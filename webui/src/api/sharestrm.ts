import { http } from './client'

// ---- 分享 STRM：不转存，STRM 直接指向 115 分享（后端 sharestrm.go） ----

export type ShareLinkAction = 'ask' | 'transfer' | 'strm'

export interface ShareStrmConfig {
  enabled: boolean
  /** 贴 115 分享链接时默认怎么处理 */
  default_action: ShareLinkAction
  /** 分享 STRM 根：媒体根下的相对路径 */
  root: string
  min_size_mb: number
  skip_owned: boolean
  assets: boolean
}

export interface ShareStrmConfigResp {
  config: ShareStrmConfig
  media_root: string
  root_abs: string
  /** 根目录不能用的原因（和媒体库重叠等），空 = 能用 */
  root_problem: string
  domain_ok: boolean
  sources: number
}

/** ok 正常 / pending 刚添加 / gone 分享已取消或过期 / badcode 提取码错 / error 其他 */
export type ShareSourceState = 'ok' | 'pending' | 'gone' | 'badcode' | 'error'

export interface ShareSource {
  id: number
  share_code: string
  receive_code: string
  title: string
  account: number
  local_dir: string
  layout: string
  state: ShareSourceState
  share_state: string
  last_error: string
  last_check: string | null
  files: number
  assets: number
  source: string
  created_at: string
  /** 有排着 / 正在跑的生成任务 */
  busy: boolean
}

export interface ShareFile {
  id: number
  file_id: string
  sha1: string
  size: number
  name: string
  share_path: string
  rel_path: string
  kind: 'video' | 'asset'
}

export const config = () => http.get<ShareStrmConfigResp>('/sharestrm/config')
export const saveConfig = (body: ShareStrmConfig) => http.post<{ message: string; config: ShareStrmConfig }>('/sharestrm/config', body)
export const sources = () => http.get<{ sources: ShareSource[] }>('/sharestrm/sources')
export const files = (id: number) =>
  http.get<{ source: ShareSource; files: ShareFile[]; root_abs: string }>(`/sharestrm/sources/${id}/files`)
export const add = (url: string, code = '') =>
  http.post<{ message: string; source: ShareSource; job_id: number }>('/sharestrm/add', { url, code })
export const check = (id: number) => http.post<{ message: string; job_id: number }>(`/sharestrm/sources/${id}/check`)
export const setReceiveCode = (id: number, receive_code: string) =>
  http.put<{ message: string }>(`/sharestrm/sources/${id}`, { receive_code })
export const remove = (id: number) => http.del<{ message: string }>(`/sharestrm/sources/${id}`)
