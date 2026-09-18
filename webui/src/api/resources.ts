import { http } from './client'

// ============ TMDB 选片（四个资源站共用） ============
export interface TmdbCandidate {
  id: number
  title: string
  year?: string
  media_type: 'movie' | 'tv'
  poster?: string
  overview?: string
  vote?: number
}

export const tmdbSearch = (query: string) =>
  http.get<{ data?: TmdbCandidate[]; hint?: string }>('/tmdb/search', { params: { query } })

export const tmdbImageUrl = (path: string, size = 'w154') =>
  `/api/tmdb/img?path=${encodeURIComponent(path)}&size=${size}`

// ============ 盘搜 PanSou ============
export interface PansouItem {
  cloud_type: string
  url: string
  note?: string
  password?: string
  datetime?: string
  /** 后端已判定好该走哪条链路，前端不要自己按 URL 猜 */
  action: 'transfer' | 'offline' | 'open'
}

export const pansouConfig = () => http.get<{ base_url?: string }>('/pansou/config')
export const savePansou = (base_url: string) => http.post<{ base_url?: string }>('/pansou/config', { base_url })
export const pansouSearch = (kw: string) =>
  http.get<{ data?: PansouItem[] }>('/pansou/search', { params: { kw }, timeoutMs: 120_000 })

// ============ 观影 ============
export interface GyConfig {
  base_url?: string
  username?: string
  password?: string
  logged_in?: boolean
}

export interface GyTorrent {
  title: string
  size?: string
  time?: string
  seeds?: string | number
  path: string
}

export const gyConfig = () => http.get<GyConfig>('/guanying/config')
export const gyCheck = () => http.get<{ logged_in: boolean }>('/guanying/check')
export const gySaveConfig = (body: { base_url: string; username: string; password: string }) =>
  http.post('/guanying/config', body)
export const gyLogin = () => http.post<{ message?: string }>('/guanying/login', {}, { timeoutMs: 120_000 })
export const gyLogout = () => http.post('/guanying/logout')
export const gySearch = (query: string, zy?: string) =>
  http.get<{ data?: GyTorrent[]; zy?: Record<string, string>; debug?: Record<string, unknown> }>(
    '/guanying/search',
    { params: { query, zy }, timeoutMs: 120_000 },
  )
export const gyResources = (path: string) =>
  http.get<{ magnet?: string }>('/guanying/resources', { params: { path }, timeoutMs: 120_000 })
export const gyOffline = (magnet: string) =>
  http.post<{ message?: string }>('/guanying/offline', { magnet }, { timeoutMs: 120_000 })

// ============ 不太灵影视 ============
export interface MkVideo {
  id: number | string
  title: string
  image?: string
  year?: string
  note?: string
}

export interface MkResource {
  seed_name?: string
  link: string
  code?: string
  action: 'transfer' | 'offline' | 'open'
}

export const mkConfig = () =>
  http.get<{ base_url?: string; username?: string; has_token?: boolean; token_at?: string }>('/mukaku/config')
export const mkSaveConfig = (body: { base_url?: string; token?: string }) => http.post('/mukaku/config', body)
export const mkCaptcha = () => http.get<{ img: string; key: string }>('/mukaku/captcha')
export const mkLogin = (body: { username: string; password: string; code: string; key: string }) =>
  http.post('/mukaku/login', body, { timeoutMs: 60_000 })
export const mkSearch = (kw: string) =>
  http.get<{ data?: MkVideo[] }>('/mukaku/search', { params: { kw }, timeoutMs: 120_000 })
export const mkResources = (id: number | string) =>
  http.get<{ data?: MkResource[] }>('/mukaku/resources', { params: { id }, timeoutMs: 120_000 })

// ============ RE0 ============
export interface Re0Resource {
  slug: string
  title?: string
  pan_type?: string
  video_resolution?: string[]
  share_size?: string
  unlock_points?: number | null
  is_unlocked?: boolean
}

export interface Re0Item {
  id: number
  title: string
  year?: string
  vote?: number | string
  media_type: 'movie' | 'tv'
  resources?: Re0Resource[]
  resources_err?: string
}

export const re0Config = () =>
  http.get<{ base_url?: string; client_id?: string; client_secret?: string; authorized?: boolean }>('/re0/config')
export const re0SaveConfig = (body: { base_url: string; client_id: string; client_secret: string }) =>
  http.post('/re0/config', body)
export const re0Check = () => http.get<{ authorized: boolean; message?: string }>('/re0/check')
export const re0OAuthStart = () => http.get<{ authorize_url?: string }>('/re0/oauth/start')
export const re0Search = (query: string) =>
  http.get<{ data?: Re0Item[]; hint?: string }>('/re0/search', { params: { query }, timeoutMs: 120_000 })
export const re0Unlock = (body: {
  media_type: string
  tmdb_id: number
  slug: string
  transfer: boolean
}) =>
  http.post<{ transferred?: boolean; transfer_msg?: string; url?: string }>('/re0/unlock', body, {
    timeoutMs: 180_000,
  })
