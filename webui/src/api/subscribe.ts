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
  /** 资源条件（中字 / 大小）；null = 跟随订阅设置 */
  cond: SubCond | null
  /** 资源优先级；null = 跟随订阅设置 */
  priority: SubPrioSel | null
  /** 洗版：库里没达标的集出现更好的就换（subwash.go） */
  wash: boolean
  /** 补齐之后洗到哪天；没齐时为空 */
  wash_until?: string | null
  /** 转存到哪个转存子目录（cid）；空 = 跟随订阅设置，root = 转存目录本身 */
  subdir?: string
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
  /** 资源条件（中字 / 大小）；null = 跟随订阅设置 */
  cond: SubCond | null
  /** 资源优先级；null = 跟随订阅设置 */
  priority: SubPrioSel | null
  wash: boolean
  /** 转存到哪个转存子目录（cid）；空 = 跟随订阅设置，root = 转存目录本身 */
  subdir: string
}

/**
 * 资源优先级（internal/api/subpriority.go）：挑资源时谁更好，与整理时洗版同一套比法。
 * basic 基础（只按 分辨率 → 片源 → HDR → 视频编码）/ follow 跟随洗版策略 / custom 自定义规则。
 * 命中第 1 条规则算「达标」
 */
export type PrioMode = 'basic' | 'follow' | 'custom'

/** 一条规则，字段同洗版 YAML 的 priority_level */
export interface WashRule {
  resource_pix?: string
  resource_type?: string
  resource_effect?: string
  video_encode?: string
  audio_encode?: string
  resource_team?: string
}

/** 单个订阅自己选的（「只要命中前 N 条」用订阅的 rank_limit） */
export interface SubPrioSel {
  mode: PrioMode
  rules?: WashRule[]
}

/** 订阅设置里的默认值 */
export interface SubPrioCfg {
  mode: PrioMode
  rules: WashRule[]
  /** 只要命中前 N 条，0 = 不限 */
  limit: number
}

/** 规则与资源条件的画质几项互转：界面上用同一套预设标签编辑 */
export const ruleToCond = (r: WashRule): SubCond => ({
  ...blankCond(),
  pix: r.resource_pix ?? '',
  type: r.resource_type ?? '',
  effect: r.resource_effect ?? '',
  video: r.video_encode ?? '',
  audio: r.audio_encode ?? '',
  team: r.resource_team ?? '',
})
export const condToRule = (c: SubCond): WashRule => ({
  resource_pix: c.pix,
  resource_type: c.type,
  resource_effect: c.effect,
  video_encode: c.video,
  audio_encode: c.audio,
  resource_team: c.team,
})

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
  /** 缺的集里有刚播出的（播出 3 天内）：没找到时隔多少分钟再查 */
  fresh_interval_min: number
  /** 只缺老集：没找到时隔多少小时再查 */
  gap_interval_hours: number
  max_eps_per_sub: number
  max_res_per_sub: number
  try_cooldown_sec: number
  max_snap_dirs: number
  /** 一轮先列几条排在前面的 115 分享、按文件实际能补几集重排，0 = 不预看 */
  share_preview_max: number
  re0_unlock_max: number
  re0_daily_budget: number
  /** 癫影自动解锁：积分与 RE0 分开算 */
  dian_unlock_max: number
  dian_daily_budget: number
  exclude_default: string
  notify: string
  offline_mode: Exclude<OfflineMode, ''>
  offline_wait_hours: number
  offline_monthly: number
  offline_reserve: number
  /** 中字与大小（画质几项 2026-10-22 起归资源优先级管） */
  cond: SubCond
  priority: SubPrioCfg
  /** 洗版：补齐之后再洗多少天 */
  wash_days: number
  /** 新订阅默认开洗版 */
  wash_default: boolean
  /** 默认转存到哪个转存子目录（cid）；空 = 转存目录本身 */
  subdir?: string
}

export const list = () => http.get<{ data: Subscription[] }>('/subscriptions')
export const of = (tmdbId: number, type: string) =>
  http.get<{ data: Subscription | null }>('/subscriptions/of', { params: { tmdb_id: tmdbId, type } })
export const detail = (id: number) => http.get<SubDetail>(`/subscriptions/${id}`)
export const create = (f: SubForm) =>
  http.post<{ data: Subscription; message?: string; job_id?: number }>('/subscriptions', f)
/** 一键订阅：按默认值订（补缺集；在播的多季剧只订最新一季），与机器人 /sub 同一套 */
export const quickCreate = (tmdbId: number, type: 'movie' | 'tv') =>
  http.post<{ data: Subscription; message?: string; job_id?: number }>('/subscriptions', {
    tmdb_id: tmdbId,
    media_type: type,
    quick: true,
  })
export const update = (id: number, f: Partial<SubForm> & { state?: 'active' | 'paused' }) =>
  http.put<{ data: Subscription; message?: string }>(`/subscriptions/${id}`, f)
export const remove = (id: number) => http.del<{ message?: string }>(`/subscriptions/${id}`)
export const run = (id: number) => http.post<{ message?: string; job_id?: number }>(`/subscriptions/${id}/run`)
export const retryAttempt = (id: number, aid: number) =>
  http.post<{ message?: string }>(`/subscriptions/${id}/attempts/${aid}/retry`)
export const seasons = (tmdbId: number) =>
  http.get<{ data: TmdbSeason[]; status?: string; ended?: boolean }>('/subscriptions/seasons', { params: { tmdb_id: tmdbId } })
export const getConfig = () => http.get<{ data: SubscribeConfig; re0_spent_today: number; dian_spent_today?: number }>('/subscribe/config')
export const saveConfig = (c: SubscribeConfig) => http.post<{ data: SubscribeConfig; message?: string }>('/subscribe/config', c)
/** 只改一个站的自动解锁积分（来源设置里用，不整份覆盖订阅设置） */
export const saveUnlock = (b: { site: 're0' | 'dian'; unlock_max: number; daily_budget: number }) =>
  http.post<{ data: SubscribeConfig; message?: string }>('/subscribe/config/unlock', b)

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
    priority: s.priority ? { mode: s.priority.mode, rules: [...(s.priority.rules ?? [])] } : null,
    wash: !!s.wash,
    subdir: s.subdir || '',
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
