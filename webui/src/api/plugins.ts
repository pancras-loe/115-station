import { http } from './client'

// ---- 115 每日签到 ----
export interface CheckinConfig {
  enabled: boolean
  cron: string
  [k: string]: unknown
}

export const checkinConfig = () => http.get<CheckinConfig>('/115checkin/config')
export const saveCheckin = (body: { enabled: boolean; cron: string }) =>
  http.post('/115checkin/config', body)
export const runCheckin = () => http.post<{ message?: string }>('/115checkin/run')

// ---- 媒体库封面生成 ----
export interface CoverGenConfig {
  enabled: boolean
  cron: string
  style: string
  strategy: string
  include: string
  blacklist: string
  titles: string
  resolution: string
  poster_count: number
  background: string
  custom_color: string
  blur: number
  color_ratio: number
  use_primary: boolean
}

export const coverGenConfig = () => http.get<{ data?: Partial<CoverGenConfig> }>('/covergen/config')
export const saveCoverGen = (body: CoverGenConfig) => http.post('/covergen/config', body)
export const runCoverGen = () => http.post<{ message?: string; warnings?: string[] }>('/covergen/run', undefined, { timeoutMs: 30 * 60_000 })
export const coverGenList = () =>
  http.get<{ data?: { name: string; time?: string }[] }>('/covergen/list')
export const cleanCoverGen = () => http.post<{ message?: string }>('/covergen/clean')

/** 预览：live=false 返回五种样式的示意图；live=true 用某个库的真实海报按当前（未保存）配置出一张 */
export interface CoverGenSample {
  samples?: Record<string, string>
  image?: string
  library?: string
  libraries?: string[]
}
export const coverGenSample = (body: { config: CoverGenConfig; live: boolean; library?: string }) =>
  http.post<CoverGenSample>('/covergen/sample', body, { timeoutMs: 120_000 })

/** 封面预览图直接走 <img src>，带时间戳绕开浏览器缓存（重新生成后要能立刻看到） */
export const coverPreviewUrl = (name: string) =>
  `/api/covergen/preview?name=${encodeURIComponent(name)}&t=${Date.now()}`

// ---- 演职人员补全 ----
export type PersonType = 'actor' | 'director' | 'writer'
export interface PersonFillConfig {
  enabled: boolean
  cron: string
  types: PersonType[]
  image: boolean
  zh_name: boolean
  zh_bio: boolean
  max_cast: number
  max_per_run: number
}
export interface PersonFillInfo {
  config: PersonFillConfig
  next_run?: string
  /** 记账里各状态的人物数（这些人物到期前不再处理） */
  marks?: Record<string, number>
  state_text?: Record<string, string>
  cached?: number
  cached_zh?: number
  /** 续扫位置：下次从第 cursor+1 部片目开始（按加入时间排序），0 = 从头 */
  cursor?: number
  last_job?: { id: number; status: string; message?: string; finished_at?: string } | null
}
export const personFillConfig = () => http.get<{ data?: PersonFillInfo }>('/personfill/config')
export const savePersonFill = (body: PersonFillConfig) => http.post('/personfill/config', body)
export const runPersonFill = () => http.post<{ message?: string; job_id?: number }>('/personfill/run')
export const resetPersonFill = () => http.post<{ message?: string }>('/personfill/reset')

// ---- 媒体信息补全 ----
export interface MetaFillConfig {
  enabled: boolean
  cron: string
  /** 没刮全的片目补 NFO / 图片 */
  scrape: boolean
  /** Emby 里缺媒体信息的让它提前探测（自动规则） */
  probe: boolean
  max_titles: number
  max_probe: number
}
export interface MetaFillInfo {
  config: MetaFillConfig
  next_run?: string
  /** 补刮过、缺的没变，冷却期内不再刮的片目数 */
  marks?: number
  retry_days?: number
  emby?: boolean
  local_root?: boolean
  /** 自动探测的规则：同一视频最多几次、两次间隔几小时 */
  limits?: { max_attempts: number; retry_hours: number }
  last_job?: { id: number; status: string; message?: string; finished_at?: string } | null
}
export const metaFillConfig = () => http.get<{ data?: MetaFillInfo }>('/metafill/config')
export const saveMetaFill = (body: MetaFillConfig) => http.post('/metafill/config', body)
export const runMetaFill = () => http.post<{ message?: string; job_id?: number }>('/metafill/run')
export const resetMetaFill = () => http.post<{ message?: string }>('/metafill/reset')
