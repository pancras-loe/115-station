import { http } from './client'

/** 播放方式（后端 playmode.go / playproxy.go，setting「playback」） */
export type PlayMode = 'redirect' | 'proxy'

export interface PlaybackConfig {
  /** 全局默认 */
  mode: PlayMode
  /** Emby 用户 id（小写）→ 单独的播放方式；没列的跟随默认 */
  users: Record<string, PlayMode>
  /** 每个 115 账号同时连 CDN 的中转连接上限 */
  max_conns: number
  /** 中转的播放允许 Emby 转码（实验） */
  transcode: boolean
}

export interface PlaybackEmbyUser {
  id: string
  name: string
  admin: boolean
  disabled: boolean
  /** Emby 记的最近活动 */
  last_seen?: string
  /** 最近一次经本站 Emby 反代播放（本站内存，重启清零） */
  via_proxy?: string
}

export interface PlaybackEmbyUsers {
  configured: boolean
  error?: string
  users: PlaybackEmbyUser[]
}

export interface ProxyPlay {
  who: string
  name: string
  acct: string
  ua: string
  ip: string
  conns: number
  bytes: number
  /** 字节 / 秒 */
  rate: number
  start: string
}

export interface ProxyAcct {
  acct: number
  name: string
  active: number
  /** 被 115 限流后还要暂停几秒，0 = 没在冷却 */
  cool_left: number
}

export interface ProxyStatus {
  plays: ProxyPlay[]
  accounts: ProxyAcct[]
  rate: number
  today_bytes: number
  max_conns: number
  any_proxy: boolean
}

export const config = () => http.get<PlaybackConfig>('/playback/config')
export const saveConfig = (c: PlaybackConfig) => http.post<PlaybackConfig>('/playback/config', c)
export const embyUsers = () => http.get<PlaybackEmbyUsers>('/playback/emby-users')
export const proxyStatus = () => http.get<ProxyStatus>('/playback/proxy-status')
