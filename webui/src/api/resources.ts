import { http } from './client'

// ============ TMDB 选片（影视转存与整理记录共用） ============
export interface TmdbCandidate {
  id: number
  title: string
  year?: string
  media_type: 'movie' | 'tv'
  poster?: string
  /** 背景图（TMDB backdrop_path）：发现页横幅与选定影片的头图用 */
  backdrop?: string
  overview?: string
  vote?: number
  /** 原名：影视转存用它判断资源标题是不是这部片 */
  original_title?: string
}

export const tmdbSearch = (query: string) =>
  http.get<{ data?: TmdbCandidate[]; hint?: string }>('/tmdb/search', { params: { query } })

/** TMDB 榜单（找资源页的趋势 / 热门）：不带 list 只返回榜单清单 */
export interface DiscoverList {
  key: string
  label: string
}
/** 筛选项（地区 / 题材），value 原样回传给后端 */
export interface DiscoverOpt {
  value: string
  label: string
}
export const tmdbDiscoverLists = () =>
  http.get<{ lists: DiscoverList[]; countries?: DiscoverOpt[]; genres?: Record<'movie' | 'tv', DiscoverOpt[]> }>(
    '/tmdb/discover',
  )
/** 按条件筛选（list=filter）时带 kind / country / genre */
export interface DiscoverFilter {
  kind: 'movie' | 'tv'
  country: string
  genre: string
}
export const tmdbDiscover = (list: string, page = 1, filter?: DiscoverFilter) =>
  http.get<{ data?: TmdbCandidate[]; has_more?: boolean }>('/tmdb/discover', {
    params: filter ? { list, page, ...filter } : { list, page },
  })

export const tmdbImageUrl = (path: string, size = 'w154') =>
  `/api/tmdb/img?path=${encodeURIComponent(path)}&size=${size}`

// ============ 盘搜 PanSou（搜索走 /transfer/resources） ============
export const pansouConfig = () => http.get<{ base_url?: string }>('/pansou/config')
export const savePansou = (base_url: string) => http.post<{ base_url?: string }>('/pansou/config', { base_url })

// ============ TG 频道 ============
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
/** 观影点选验证码：img 是 data URL，按 text 的顺序点字，坐标换算到 w×h */
export interface GyCaptcha {
  img: string
  text: string
  w: number
  h: number
}
/** 站点要验证码时回 409 {need_captcha, captcha}；点完带 captcha（[x,y] 列表）再调一次 */
export const gyLogin = (body: { base_url: string; username: string; password: string; captcha?: number[][] }) =>
  http.post<{ message?: string }>('/guanying/login', body, { timeoutMs: 120_000 })
export const gyCaptcha = () => http.post<GyCaptcha>('/guanying/captcha', {}, { timeoutMs: 60_000 })
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
  http.get<{
    base_url?: string
    client_id?: string
    client_secret?: string
    authorized?: boolean
    authorized_as?: string
    /** 授权里缺的权限（2026-10 之前的授权没有 meta / write），非空要重新授权 */
    missing_scopes?: string[] | null
    /** 网页登录（2026-10-14 起，RE0 不再开放 OpenAPI 申请） */
    username?: string
    password?: string
    use_proxy?: boolean
    /** 当前走哪条通道：openapi（已授权的老用户优先）/ web / 空 = 未配置 */
    channel?: '' | 'openapi' | 'web'
  }>('/re0/config')
export interface Re0SaveBody {
  base_url: string
  client_id: string
  client_secret: string
  username: string
  password: string
  use_proxy: boolean
}
export const re0SaveConfig = (body: Re0SaveBody) => http.post<{ message?: string }>('/re0/config', body)
export interface Re0CheckResult {
  app_ok: boolean
  app_name?: string
  authorized: boolean
  user?: string
  level?: string | number | null
  points?: number | null
  banned?: boolean
  missing_scopes?: string[] | null
  message?: string
  /** web = 这次检查的是网页登录 */
  channel?: string
}
// 网页通道要真开浏览器登录一次，可能要过 Cloudflare 验证（最长 90 秒）
export const re0Check = () => http.get<Re0CheckResult>('/re0/check', { timeoutMs: 200_000 })
/** 网页登录用的浏览器（CloakBrowser）：本地状态与下载进度 */
export interface Re0BrowserState {
  state: 'missing' | 'downloading' | 'verifying' | 'extracting' | 'ready' | 'error' | 'unsupported'
  version: string
  platform: string
  path?: string
  done: number
  total: number
  speed: number
  source?: string
  error?: string
  reason?: string
}
/** 正在做的网页操作走到哪一步 */
export interface Re0OpState {
  what: string
  step: string
  /** 开始时间（unix 秒） */
  since: number
}
export const re0Browser = () =>
  http.get<{ browser: Re0BrowserState; running: boolean; channel: string; op?: Re0OpState }>('/re0/browser')
export const re0BrowserDownload = (force = false) =>
  http.post<{ message: string; browser: Re0BrowserState }>('/re0/browser/download', { force })
export interface Re0Checkin {
  enabled: boolean
  last_done?: string
  last_result?: string
  last_result_at?: string
  done_today: boolean
  hour: number
  /** 签不了的原因（未授权 / 授权缺 write），空 = 可以签 */
  blocked?: string
}
export const re0Checkin = () => http.get<Re0Checkin>('/re0/checkin')
export const re0SaveCheckin = (enabled: boolean) => http.post('/re0/checkin', { enabled })
// 网页签到要开浏览器、可能要登录与过验证
export const re0RunCheckin = () => http.post<{ message: string }>('/re0/checkin/run', {}, { timeoutMs: 260_000 })
export const re0OAuthStart = (redirectUri: string) =>
  http.get<{ authorize_url?: string }>('/re0/oauth/start', { params: { redirect_uri: redirectUri } })
export const re0EgressIP = () => http.get<{ ip: string; ipv6: boolean; exact: boolean }>('/re0/egress-ip')

// ============ 癫影（m.dian115.com）：浏览器与 RE0 共用，进度读 re0Browser 的 op ============
export const dianConfig = () =>
  http.get<{ email?: string; password?: string; configured?: boolean }>('/dian/config')
export const dianSaveConfig = (body: { email: string; password: string }) =>
  http.post<{ message?: string }>('/dian/config', body)
// 要开浏览器、登录一次
export const dianCheck = () =>
  http.get<{ message: string; user?: string; points?: number; vip?: boolean }>('/dian/check', { timeoutMs: 200_000 })
export const dianCheckin = () => http.get<Re0Checkin>('/dian/checkin')
export const dianSaveCheckin = (enabled: boolean) => http.post('/dian/checkin', { enabled })
export const dianRunCheckin = () => http.post<{ message: string }>('/dian/checkin/run', {}, { timeoutMs: 260_000 })

// ============ 转存后分享到 RE0 / 癫影（后端 shareout.go）：只管贴链接转存的分享，整理认出是哪一部之后才发 ============
export type ShareOutSite = 're0' | 'dian'
export interface ShareOutSiteCfg {
  enabled: boolean
  /** 分享出去的解锁积分；0 = 不设（RE0 免费，癫影用站点默认价） */
  unlock_points: number
  /** 匿名分享（只有 RE0 支持） */
  anonymous?: boolean
}
export interface ShareOutConfig {
  re0: ShareOutSiteCfg
  dian: ShareOutSiteCfg
  re0_channel: '' | 'openapi' | 'web'
  /** RE0 发不了的原因（没配置 / 授权缺 write），空 = 能发 */
  re0_ready: string
  dian_configured: boolean
}
export const shareOutConfig = () => http.get<ShareOutConfig>('/shareout/config')
export const shareOutSave = (site: ShareOutSite, cfg: ShareOutSiteCfg) =>
  http.post<{ message: string }>('/shareout/config', { site, ...cfg })

/** 任务中心「分享记录」一行（后端 shareoutrecords.go，只读本地记账） */
export interface ShareOutRecord {
  id: number
  link_id: number
  site: ShareOutSite
  /** done = 发成了 / skipped = 不发（原因在 message）/ failed = 失败 */
  status: 'done' | 'skipped' | 'failed'
  message: string
  ref: string
  points: number
  attempts: number
  tmdb_id: number
  media_type: string
  title: string
  year: string
  poster_path: string
  share_name: string
  link_url: string
  source: string
  /** 站上那条分享（发成的才有） */
  site_url?: string
  /** 失败了、还会自动重试的时间 */
  next_retry?: string
  can_retry: boolean
  created_at: string
  updated_at: string
}
export const shareOutRecords = (params: { status?: string; page?: number; size?: number }) =>
  http.get<{ data: ShareOutRecord[]; total: number; counts: Record<string, number>; page: number; size: number }>(
    '/shareout/records',
    { params },
  )
export const shareOutRetry = (id: number) => http.post<{ message: string }>(`/shareout/records/${id}/retry`, {})
/** 删除分享记录：按 id，或按状态清空（all = 全部）。后端只是隐藏，记账留着防重发 */
export const shareOutDelete = (body: { ids?: number[]; status?: string }) =>
  http.post<{ message: string; deleted: number }>('/shareout/records/delete', body)
