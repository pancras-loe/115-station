import { http } from './client'

/** 订阅（internal/api/subapi.go 的 subDTO） */
export interface Subscription {
  id: number
  tmdb_id: number
  media_type: 'movie' | 'tv'
  title: string
  orig_title?: string
  year?: string
  poster_path?: string
  /** all 全剧 / season 某一季 / range 集段；电影为空 */
  scope: '' | 'all' | 'season' | 'range'
  season: number
  ep_start: number
  ep_end: number
  specials: boolean
  /** missing 补缺集 / new 只追新集；电影为空 */
  follow: '' | 'missing' | 'new'
  follow_from?: string | null
  sources: string[]
  rank_limit: number
  include: string
  exclude: string
  offline_mode: OfflineMode
  /** 资源条件；null = 跟随订阅设置 */
  cond: SubCond | null
  /** active 追更中 / paused 已暂停 / done 已完成 / stalled 长期找不到 */
  state: 'active' | 'paused' | 'done' | 'stalled'
  next_check_at?: string | null
  last_check_at?: string | null
  last_result: string
  have: number
  total: number
  missing: number
  empty_rounds: number
  created_at: string
  done_at?: string | null
  /** 在路上的尝试数 */
  inflight: number
  /** 正在排队 / 检查 */
  running: boolean
}

/** 新建 / 修改的表单 */
export interface SubForm {
  tmdb_id?: number
  media_type?: 'movie' | 'tv'
  scope: 'all' | 'season' | 'range'
  season: number
  ep_start: number
  ep_end: number
  specials: boolean
  follow: 'missing' | 'new'
  sources: string[]
  rank_limit: number
  include: string
  exclude: string
  offline_mode: OfflineMode
  /** 资源条件；null = 跟随订阅设置 */
  cond: SubCond | null
}

/**
 * 资源条件（同洗版规则的写法：逗号分隔命中任一，「!」开头排除）。
 * 标题明确不符的不要；分享里每个视频再按文件名判（文件名没写看资源标题，都没写算不符）；
 * 磁力只能看标题，没写的不下
 */
export interface SubCond {
  pix: string
  type: string
  effect: string
  video: string
  audio: string
  team: string
  zh: boolean
  min_gb: number
  max_gb: number
}

export const blankCond = (): SubCond => ({
  pix: '',
  type: '',
  effect: '',
  video: '',
  audio: '',
  team: '',
  zh: false,
  min_gb: 0,
  max_gb: 0,
})

/** 离线策略：pack 只下合集包 / share_only 只转存分享 / any 不限；订阅上为空 = 跟随订阅设置 */
export type OfflineMode = '' | 'pack' | 'share_only' | 'any'

export type EpState = 'have' | 'inflight' | 'awaiting' | 'missing' | 'unaired' | 'skipped'

export interface SubSeasonGrid {
  season: number
  eps: { e: number; state: EpState; air?: string }[]
}

/** 试过的资源（SubAttempt） */
export interface SubAttempt {
  id: number
  source: string
  kind: string
  title: string
  url: string
  wrapper: string
  episodes: string[]
  /** inflight / ingested / partial / rejected / failed / useless / paid */
  status: string
  reason: string
  retry_at?: string | null
  points: number
  link_id: number
  records: number
  created_at: string
  resolved_at?: string | null
}

export interface SubDetail {
  data: Subscription
  attempts: SubAttempt[]
  grid?: SubSeasonGrid[]
  grid_error?: string
}

export interface TmdbSeason {
  season: number
  name: string
  episodes: number
  air_date?: string
}

export interface SubscribeConfig {
  enabled: boolean
  air_delay_hours: number
  movie_wait: 'digital' | 'theatrical_plus' | 'now'
  max_subs_per_round: number
  max_eps_per_sub: number
  max_fails_per_sub: number
  try_cooldown_sec: number
  max_snap_dirs: number
  re0_unlock_max: number
  re0_daily_budget: number
  exclude_default: string
  notify: string
  offline_mode: Exclude<OfflineMode, ''>
  offline_wait_hours: number
  offline_monthly: number
  offline_reserve: number
  cond: SubCond
}

export const list = () => http.get<{ data: Subscription[] }>('/subscriptions')
export const of = (tmdbId: number, type: string) =>
  http.get<{ data: Subscription | null }>('/subscriptions/of', { params: { tmdb_id: tmdbId, type } })
export const detail = (id: number) => http.get<SubDetail>(`/subscriptions/${id}`)
export const create = (f: SubForm) =>
  http.post<{ data: Subscription; message?: string; job_id?: number }>('/subscriptions', f)
export const update = (id: number, f: Partial<SubForm> & { state?: 'active' | 'paused' }) =>
  http.put<{ data: Subscription; message?: string }>(`/subscriptions/${id}`, f)
export const remove = (id: number) => http.del<{ message?: string }>(`/subscriptions/${id}`)
export const run = (id: number) => http.post<{ message?: string; job_id?: number }>(`/subscriptions/${id}/run`)
export const retryAttempt = (id: number, aid: number) =>
  http.post<{ message?: string }>(`/subscriptions/${id}/attempts/${aid}/retry`)
export const seasons = (tmdbId: number) =>
  http.get<{ data: TmdbSeason[]; status?: string; ended?: boolean }>('/subscriptions/seasons', { params: { tmdb_id: tmdbId } })
export const getConfig = () => http.get<{ data: SubscribeConfig; re0_spent_today: number }>('/subscribe/config')
export const saveConfig = (c: SubscribeConfig) => http.post<{ data: SubscribeConfig; message?: string }>('/subscribe/config', c)

/** 订阅转回表单（编辑时用） */
export function toForm(s: Subscription): SubForm {
  return {
    scope: (s.scope || 'all') as SubForm['scope'],
    season: s.season,
    ep_start: s.ep_start,
    ep_end: s.ep_end,
    specials: s.specials,
    follow: (s.follow || 'missing') as SubForm['follow'],
    sources: [...(s.sources ?? [])],
    rank_limit: s.rank_limit,
    include: s.include,
    exclude: s.exclude,
    offline_mode: s.offline_mode || '',
    cond: s.cond ? { ...blankCond(), ...s.cond } : null,
  }
}

export const STATE_TEXT: Record<Subscription['state'], string> = {
  active: '追更中',
  paused: '已暂停',
  done: '已完成',
  stalled: '长期找不到',
}
export const STATE_TONE: Record<Subscription['state'], 'accent' | 'default' | 'success' | 'warning'> = {
  active: 'accent',
  paused: 'default',
  done: 'success',
  stalled: 'warning',
}

export const ATTEMPT_TEXT: Record<string, string> = {
  inflight: '在路上',
  ingested: '已入库',
  partial: '部分入库',
  rejected: '内容不对',
  failed: '失败',
  useless: '没有要的集',
  paid: '需要积分',
}
export const ATTEMPT_TONE: Record<string, 'accent' | 'default' | 'success' | 'warning' | 'danger'> = {
  inflight: 'accent',
  ingested: 'success',
  partial: 'warning',
  rejected: 'danger',
  failed: 'danger',
  useless: 'default',
  paid: 'warning',
}

/** 订阅范围一句话：「全剧」「第 2 季」「第 2 季 E05 起」 */
export function scopeText(s: Pick<Subscription, 'media_type' | 'scope' | 'season' | 'ep_start' | 'ep_end' | 'follow' | 'specials'>) {
  if (s.media_type === 'movie') return '电影'
  let t = '全剧'
  if (s.scope === 'season') t = `第 ${s.season} 季`
  if (s.scope === 'range') t = `第 ${s.season} 季 E${s.ep_start}${s.ep_end ? `–E${s.ep_end}` : ' 起'}`
  if (s.scope === 'all' && s.specials) t += '（含特别篇）'
  return t + (s.follow === 'new' ? ' · 只追新集' : ' · 补缺集')
}
