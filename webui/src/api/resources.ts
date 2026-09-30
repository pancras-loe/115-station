import { http } from './client'

// ============ TMDB 选片（影视转存与整理记录共用） ============
export interface TmdbCandidate {
  id: number
  title: string
  year?: string
  media_type: 'movie' | 'tv'
  poster?: string
  overview?: string
  vote?: number
  /** 原名：影视转存用它判断资源标题是不是这部片 */
  original_title?: string
}

export const tmdbSearch = (query: string) =>
  http.get<{ data?: TmdbCandidate[]; hint?: string }>('/tmdb/search', { params: { query } })

export const tmdbImageUrl = (path: string, size = 'w154') =>
  `/api/tmdb/img?path=${encodeURIComponent(path)}&size=${size}`

// ============ 盘搜 PanSou（搜索走 /transfer/resources） ============
export const pansouConfig = () => http.get<{ base_url?: string }>('/pansou/config')
export const savePansou = (base_url: string) => http.post<{ base_url?: string }>('/pansou/config', { base_url })

// ============ TG 频道（与 TG 关键词订阅共用一份频道清单） ============
export const tgConfig = () => http.get<{ channels?: string }>('/tgsearch/config')
export const tgSaveConfig = (channels: string) => http.post<{ channels?: string }>('/tgsearch/config', { channels })

// ============ 观影 ============
export interface GyConfig {
  base_url?: string
  username?: string
  password?: string
  logged_in?: boolean
}

export const gyConfig = () => http.get<GyConfig>('/guanying/config')
export const gyCheck = () => http.get<{ logged_in: boolean }>('/guanying/check')
export const gySaveConfig = (body: { base_url: string; username: string; password: string }) =>
  http.post('/guanying/config', body)
export const gyLogin = (body: { base_url: string; username: string; password: string }) =>
  http.post<{ message?: string }>('/guanying/login', body, { timeoutMs: 120_000 })
export const gyLogout = () => http.post('/guanying/logout')

// ============ 不太灵影视 ============
export const mkConfig = () =>
  http.get<{ base_url?: string; username?: string; has_token?: boolean; token_at?: string }>('/mukaku/config')
export const mkSaveConfig = (body: { base_url?: string; token?: string }) => http.post('/mukaku/config', body)
export const mkCaptcha = () => http.get<{ img: string; key: string }>('/mukaku/captcha')
export const mkLogin = (body: { username: string; password: string; code: string; key: string }) =>
  http.post('/mukaku/login', body, { timeoutMs: 60_000 })

// ============ RE0 ============
export const re0Config = () =>
  http.get<{ base_url?: string; client_id?: string; client_secret?: string; authorized?: boolean }>('/re0/config')
export const re0SaveConfig = (body: { base_url: string; client_id: string; client_secret: string }) =>
  http.post('/re0/config', body)
export const re0Check = () => http.get<{ authorized: boolean; message?: string }>('/re0/check')
export const re0OAuthStart = () => http.get<{ authorize_url?: string }>('/re0/oauth/start')
