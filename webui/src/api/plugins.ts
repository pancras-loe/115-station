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

// ---- 定时清理 115 回收站 ----
/** 一个 115 账号的清理设置（执行时间全局一个，其余按账号） */
export interface RbCleanAcctCfg {
  id: number
  name: string
  main: boolean
  logged_in: boolean
  on: boolean
  keep_days: number
  has_password: boolean
}
export interface RbCleanConfig {
  enabled: boolean
  cron: string
  accounts: RbCleanAcctCfg[]
  next_run?: string
  last_result?: string
  last_result_at?: string
  last_ok?: boolean
  running?: boolean
}
export interface RbCleanAcct {
  id: number
  account: string
  keep_days: number
  total: number
  expired: number
  size: number
  deleted: number
  error?: string
}
/** password 不带 = 不改，空串 = 清掉 */
export interface RbCleanAcctReq {
  id: number
  on: boolean
  keep_days: number
  password?: string
}

export const rbCleanConfig = () => http.get<RbCleanConfig>('/rbclean/config')
export const saveRbClean = (body: { enabled: boolean; cron: string; accounts: RbCleanAcctReq[] }) =>
  http.post('/rbclean/config', body)
/** 只读预演：按没保存的保留天数算会删多少，列出的账号不看开关（回收站大时要翻很多页） */
export const previewRbClean = (accounts: { id: number; keep_days: number }[]) =>
  http.post<{ data?: RbCleanAcct[]; message?: string }>('/rbclean/preview', { accounts }, { timeoutMs: 10 * 60_000 })
export const runRbClean = () => http.post<{ message?: string }>('/rbclean/run')

// ---- 定时清理 115 离线任务（offclean.go） ----
export interface OffCleanConfig {
  enabled: boolean
  cron: string
  /** 删下载失败的 */
  failed: boolean
  /** 添加超过几小时还没下完的删；0 = 不删 */
  stuck_hours: number
  /** true = 115 App 里自己加的也删 */
  all: boolean
  accounts: { id: number; name: string; main: boolean; logged_in: boolean; on: boolean }[]
  next_run?: string
  last_result?: string
  last_result_at?: string
  last_ok?: boolean
  running?: boolean
}
export interface OffCleanTask {
  name: string
  /** -1 失败 / 0 排队 / 1 下载中 */
  status: number
  percent: number
  hours: number
  size: number
  mine: boolean
}
export interface OffCleanResult {
  id: number
  account: string
  total: number
  matched: number
  deleted: number
  tasks: OffCleanTask[]
  error?: string
}
export interface OffCleanReq {
  enabled: boolean
  cron: string
  failed: boolean
  stuck_hours: number
  all: boolean
  /** 开着的账号 id */
  accounts: number[]
}
export const offCleanConfig = () => http.get<OffCleanConfig>('/offclean/config')
export const saveOffClean = (body: OffCleanReq) => http.post('/offclean/config', body)
/** 只读预演：按没保存的规则列出会删哪些，列出的账号不看开关 */
export const previewOffClean = (body: OffCleanReq) =>
  http.post<{ data?: OffCleanResult[]; message?: string }>('/offclean/preview', body, { timeoutMs: 5 * 60_000 })
export const runOffClean = () => http.post<{ message?: string }>('/offclean/run')

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
  /** 刮削任务结束后补这次刮过的片目 */
  after_scrape: boolean
  enabled: boolean
  cron: string
  types: PersonType[]
  image: boolean
  zh_name: boolean
  zh_bio: boolean
  max_cast: number
  max_per_run: number
  /** 补完后同步本地 NFO：演职人员名换中文、演员头像存进 .actors */
  nfo_sync: boolean
  /** 全库扫描开头合并重复人物（同一个 TMDB 人物在 Emby 里有多个条目） */
  merge_dups: boolean
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
export interface PersonDupBrief {
  id: string
  name: string
  image: boolean
}
export interface PersonDupGroup {
  tmdb_id: number
  keep: PersonDupBrief
  drop: PersonDupBrief[]
  /** 重复条目同名：Emby 按名字认人，合并不了 */
  same_name?: boolean
}
/** 预览重复人物（只读 Emby）。total 是组数，groups 最多列 200 组 */
export const personFillDups = () =>
  http.get<{ data?: { total: number; persons: number; groups: PersonDupGroup[] } }>('/personfill/dups')

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
  /** 刮削方式：emby 时补刮这一步不做 */
  provider?: 'station' | 'emby'
  /** 自动探测的规则：同一视频最多几次、两次间隔几小时 */
  limits?: { max_attempts: number; retry_hours: number }
  last_job?: { id: number; status: string; message?: string; finished_at?: string } | null
}
export const metaFillConfig = () => http.get<{ data?: MetaFillInfo }>('/metafill/config')
export const saveMetaFill = (body: MetaFillConfig) => http.post('/metafill/config', body)
export const runMetaFill = () => http.post<{ message?: string; job_id?: number }>('/metafill/run')
export const resetMetaFill = () => http.post<{ message?: string }>('/metafill/reset')

// ---- 阿里云盘秒传进 115（ali2115.go） ----
export interface Ali2115Config {
  enabled: boolean
  /** 默认用哪个阿里云盘账号，0 = 第一个 */
  source: number
  /** 只传视频与字幕 */
  media_only: boolean
}
export interface AliAccount {
  id: number
  name: string
  nick: string
  last_err?: string
  last_ok?: string
}
export const ali2115Config = () => http.get<{ config: Ali2115Config; accounts: AliAccount[] }>('/ali2115/config')
export const saveAli2115 = (body: Ali2115Config) => http.post<{ message?: string }>('/ali2115/config', body)
export const aliAddToken = (token: string) => http.post<{ message: string; id: number }>('/ali2115/accounts', { token })
export const aliDeleteAccount = (id: number) => http.del<{ message: string }>(`/ali2115/accounts/${id}`)
export const aliTestAccount = (id: number) => http.post<{ message: string }>(`/ali2115/accounts/${id}/test`)
export const aliQRCode = () => http.post<{ t: string; ck: string; image: string }>('/ali2115/qrcode')
export const aliQRCheck = (t: string, ck: string) =>
  http.post<{ status: string; message?: string; id?: number }>('/ali2115/qrcode/check', { t, ck })

// ============ 定时清空 115 目录 ============
/** account 同媒体库：0 = 主号，其他是附加账号 id */
export interface DirCleanDir {
  account: number
  cid: string
  path: string
}
export interface DirCleanAcct {
  id: number
  name: string
  main: boolean
  logged_in: boolean
}
export interface DirCleanConfig {
  enabled: boolean
  cron: string
  dirs: DirCleanDir[]
  accounts: DirCleanAcct[]
  next_run?: string
  last_result?: string
  last_result_at?: string
  last_ok?: boolean
  running?: boolean
}
export interface DirCleanResult {
  account: string
  cid: string
  path: string
  dirs: number
  files: number
  size: number
  deleted: number
  error?: string
}

export const dirCleanConfig = () => http.get<DirCleanConfig>('/dirclean/config')
/** 保存时后端逐个核对目录位置（媒体库 / 转存文件夹及它们的上级拒收），目录多时要几秒 */
export const saveDirClean = (body: { enabled: boolean; cron: string; dirs: DirCleanDir[] }) =>
  http.post('/dirclean/config', body, { timeoutMs: 5 * 60_000 })
/** 只读预演：按没保存的目录列出会删什么 */
export const previewDirClean = (dirs: DirCleanDir[]) =>
  http.post<{ data?: DirCleanResult[]; message?: string }>('/dirclean/preview', { dirs }, { timeoutMs: 10 * 60_000 })
export const runDirClean = () => http.post<{ message?: string }>('/dirclean/run')

// ============ 定时清空阿里云盘目录（alidirclean.go） ============
/** account = 阿里云盘账号 id（与秒传共用登录）；drive：resource 资源盘 / backup 备份盘 */
export interface AliDirCleanDir {
  account: number
  drive: 'resource' | 'backup'
  path: string
  file_id?: string
}
export interface AliCleanAcct {
  id: number
  name: string
  last_err?: string
}
export interface AliDirCleanConfig {
  enabled: boolean
  cron: string
  dirs: AliDirCleanDir[]
  accounts: AliCleanAcct[]
  next_run?: string
  last_result?: string
  last_result_at?: string
  last_ok?: boolean
  running?: boolean
}
export interface AliDirCleanResult {
  account: string
  drive: string
  path: string
  dirs: number
  files: number
  size: number
  deleted: number
  error?: string
}
export const aliDirCleanConfig = () => http.get<AliDirCleanConfig>('/alidirclean/config')
/** 保存时后端按路径逐级查出目录（查不到 / 秒传临时目录拒收） */
export const saveAliDirClean = (body: { enabled: boolean; cron: string; dirs: AliDirCleanDir[] }) =>
  http.post('/alidirclean/config', body, { timeoutMs: 5 * 60_000 })
export const previewAliDirClean = (dirs: AliDirCleanDir[]) =>
  http.post<{ data?: AliDirCleanResult[]; message?: string }>('/alidirclean/preview', { dirs }, { timeoutMs: 10 * 60_000 })
export const runAliDirClean = () => http.post<{ message?: string }>('/alidirclean/run')

// ============ 清理阿里云盘回收站（alirbclean.go） ============
/** keep_days 0 = 整个清空 */
export interface AliRbCleanAcct extends AliCleanAcct {
  on: boolean
  keep_days: number
}
export interface AliRbCleanConfig {
  enabled: boolean
  cron: string
  accounts: AliRbCleanAcct[]
  next_run?: string
  last_result?: string
  last_result_at?: string
  last_ok?: boolean
  running?: boolean
}
export interface AliRbCleanResult {
  id: number
  account: string
  keep_days: number
  total: number
  expired: number
  unknown: number
  size: number
  deleted: number
  error?: string
}
export const aliRbCleanConfig = () => http.get<AliRbCleanConfig>('/alirbclean/config')
export const saveAliRbClean = (body: { enabled: boolean; cron: string; accounts: { id: number; on: boolean; keep_days: number }[] }) =>
  http.post('/alirbclean/config', body)
export const previewAliRbClean = (accounts: { id: number; keep_days: number }[]) =>
  http.post<{ data?: AliRbCleanResult[]; message?: string }>('/alirbclean/preview', { accounts }, { timeoutMs: 10 * 60_000 })
export const runAliRbClean = () => http.post<{ message?: string }>('/alirbclean/run')

// ---- PT 站签到（ptsignin.go，设计见 PT-SIGNIN-PLAN.md） ----
export type PtResultKind = 'ok' | 'login' | 'fail'
export interface PtPreset {
  key: string
  name: string
  domains: string[]
  handler: string
  cred: 'cookie' | 'token'
  note?: string
  unavailable?: boolean
}
export interface PtDay {
  result: PtResultKind
  reason?: string
  message: string
  at: string
}
export interface PtSite {
  id: number
  preset: string
  name: string
  base_url: string
  handler: string
  cred: 'cookie' | 'token'
  has_cookie: boolean
  has_token: boolean
  cookie_end?: string
  ua: string
  proxy: boolean
  mode: 'signin' | 'keep'
  enabled: boolean
  need_fix: boolean
  today?: PtDay
  /** 近 7 天每天的结果（旧 → 新，最后是今天），没跑是 '' */
  week: ('' | PtResultKind)[]
}
/** 新增 / 修改 / 测试；cookie、token 不传 = 不改 */
export interface PtSiteReq {
  preset?: string
  name?: string
  base_url?: string
  cookie?: string
  token?: string
  ua: string
  proxy: boolean
  mode: 'signin' | 'keep'
  enabled?: boolean
}
export interface PtResult {
  result: PtResultKind
  reason?: string
  message: string
}
export interface PtRecordRow {
  site_id: number
  name: string
  days: (PtDay | null)[]
  ok: number
}
export interface PtRecords {
  days: string[]
  rows: PtRecordRow[]
  last: { start?: string; seconds?: number; trigger?: string }
  items: { name: string; result: PtResultKind; reason?: string; message: string; at: string }[]
  notify_preview: string
}
export interface PtConfig {
  enabled: boolean
  mode: 'window' | 'cron'
  window_from: string
  window_to: string
  cron: string
  gap_min: number
  gap_max: number
  retry: boolean
  notify: 'always' | 'fail_only' | 'off'
  today_at?: string
  next_run?: string
  running?: boolean
}
export const ptPresets = () => http.get<{ data: PtPreset[] }>('/ptsignin/presets')
export const ptSites = () => http.get<{ data: PtSite[]; running: boolean }>('/ptsignin/sites')
export const ptCreateSite = (body: PtSiteReq) => http.post<{ data: PtSite }>('/ptsignin/sites', body)
export const ptUpdateSite = (id: number, body: PtSiteReq) => http.put<{ data: PtSite }>(`/ptsignin/sites/${id}`, body)
export const ptDeleteSite = (id: number) => http.del(`/ptsignin/sites/${id}`)
export const ptTestSite = (body: PtSiteReq & { id?: number }) =>
  http.post<{ data: PtResult; ms: number }>('/ptsignin/test', body, { timeoutMs: 90_000 })
export const ptRunAll = () => http.post<{ message?: string }>('/ptsignin/run', {})
export const ptRunOne = (siteId: number) =>
  http.post<{ data: PtResult }>('/ptsignin/run', { site_id: siteId }, { timeoutMs: 3 * 60_000 })
export const ptRecords = () => http.get<PtRecords>('/ptsignin/records')
export const ptConfig = () => http.get<PtConfig>('/ptsignin/config')
export const ptSaveConfig = (body: Omit<PtConfig, 'today_at' | 'next_run' | 'running'>) => http.post('/ptsignin/config', body)

// ---- 各插件自己的日志（pluginlog.go）----
/** key 同扩展功能页的插件 key；最后 lines 行，时间正序 */
export const pluginLogs = (key: string, lines = 1000) =>
  http.get<{ lines: string[] }>(`/plugins/logs/${key}`, { params: { lines } })
export const clearPluginLogs = (key: string) => http.post(`/plugins/logs/${key}/clear`)
