import { http } from './client'

export interface PipelineStep {
  step: string
  status: string
  message: string
}

export interface PipelineDetail {
  file_name: string
  status: 'success' | 'exists' | string
  message: string
}

export interface PipelineResult {
  message?: string
  steps?: PipelineStep[]
  shows?: { title: string; year?: string; target: string }[]
  details?: PipelineDetail[]
}

/**
 * 整理是长任务，和同步互斥（后端共用 fullSyncMu）。
 * 无请求体：整理自带落盘（STRM + 刮削 + 刷 Emby），不再有「整理后是否同步」这个开关。
 */
export const runPipeline = () =>
  http.post<PipelineResult>('/organize/pipeline', undefined, { timeoutMs: 60 * 60_000 })

// ---- 二级分类 / 洗版规则（YAML） ----
export const getCategories = () => http.get<{ config?: string }>('/scrape/categories')
export const saveCategories = (yaml: string) => http.post('/scrape/categories', { yaml })

export const getWash = () => http.get<{ config?: string }>('/scrape/wash')
export const saveWash = (yaml: string) => http.post('/scrape/wash', { yaml })

// ---- 影视刮削 ----
export interface ScrapeConfig {
  local_root: string
  write_nfo: boolean
  write_images: boolean
  force: boolean
  auto_after_organize: boolean
}

/** 后端回的是 { cfg, status } 两层结构，不是扁平配置 —— 摊平取会全部读成 undefined */
export const getScrapeConfig = () =>
  http.get<{ cfg?: Partial<ScrapeConfig>; status?: { running?: boolean } }>('/scrape/config')
export const saveScrapeConfig = (cfg: ScrapeConfig) => http.post('/scrape/config', cfg)
export const runScrape = () => http.post('/scrape/run')
export const stopScrape = () => http.post('/scrape/stop')
export const scrapeStatus = () =>
  http.get<{ running?: boolean; progress?: string }>('/scrape/status', { timeoutMs: 15_000 })

// ---- 整理记录 ----
export interface OrganizeRecordFile {
  fid: string
  name: string
  /** 重命名之前的原名（与 name 相同时后端不存） */
  orig?: string
  kind: 'video' | 'subtitle' | 'meta' | 'junk'
  pickcode?: string
  size?: number
  sha1?: string
}

/** awaiting = 开了「人工确认」后识别完停下来的条目，文件还在待整理里原地没动 */
/** 整理记录的来源链接：这批内容是从哪条离线 / 分享链接下来的 */
export interface OrganizeRecordLink {
  id: number
  kind: 'magnet' | 'ed2k' | 'http' | 'ftp' | 'share' | string
  url: string
  name: string
  /** 提交来源：web / 机器人 / 观影 / 影巢 / TG订阅 */
  source: string
  created_at: string
}

/** awaiting = 开了「人工确认」后识别完停下来的条目，文件还在待整理里原地没动 */
export type OrganizeRecordStatus = 'success' | 'exists' | 'failed' | 'unrecognized' | 'awaiting'

export interface OrganizeRecord {
  id: number
  batch_id: string
  source: string
  source_fid: string
  source_kind: 'dir' | 'file'
  source_cid: string
  status: OrganizeRecordStatus
  /** 失败发生在哪一步：recognize / move / strm / scrape / confirm */
  stage: string
  message: string
  tmdb_id: number
  title: string
  year: string
  media_type: string
  poster_path: string
  category: string
  target_dir: string
  target_cid: string
  video_count: number
  total_size: number
  strm_created: number
  scrape_state: string
  scrape_msg: string
  manual_tmdb: boolean
  /** 识别出处：ai_title（模型改写片名后搜中）/ ai_pick（模型从候选里选中），规则识别的为空 */
  recog_via?: string
  /** AI 判定的分数 0-100 与打分依据 */
  ai_score?: number
  ai_note?: string
  /** 因为 AI 判定停下来等确认（与「人工确认」开关无关） */
  hold_ai?: boolean
  /** 暂存的指定（还没提交到任务队列）；0 = 没有 */
  pending_tmdb_id?: number
  pending_media_type?: string
  /** 暂存条目的「片名 (年份)」 */
  pending_label?: string
  redo_count: number
  created_at: string
  file_list: OrganizeRecordFile[]
  /** 来源链接；不是离线 / 分享提交进来的（手动丢进待整理等）没有 */
  link?: OrganizeRecordLink
}

export interface OrganizeRecordPage {
  data: OrganizeRecord[]
  total: number
  page: number
  size: number
}

/** status 传 'problem' 取「失败 + 未识别」；type 传 movie / tv；q 纯数字时也按 TMDB ID 匹配 */
export const listRecords = (params: {
  status?: string
  type?: string
  q?: string
  page?: number
  size?: number
}) => http.get<OrganizeRecordPage>('/organize/records', { params })

/** 各状态条数：all / awaiting / problem / success / exists / failed / unrecognized */
export const recordStats = () =>
  http.get<{ data: Record<string, number> }>('/organize/records/stats', { timeoutMs: 15_000 })

/**
 * 确认入库：按识别结果（或改指定的条目）走完后半条流水线。
 * 和整理互斥，一部剧上百集时耗时按分钟计
 */
/** 入队接口的回复（202）：任务 id、排第几、预计多久后跑完 */
export interface QueuedReply {
  message: string
  job_id: number
  position: number
  eta_sec: number
}

/** 确认入库 → 入任务队列立即返回。label 是选中条目的「片名 (年份)」，只用于任务标题 */
export const confirmRecord = (id: number, pick?: { tmdbId: number; mediaType: string; label?: string }) =>
  http.post<QueuedReply>(
    `/organize/records/${id}/confirm`,
    pick ? { tmdb_id: pick.tmdbId, media_type: pick.mediaType, label: pick.label } : {},
  )

/** 批量按识别结果入库；没识别出来的由后端跳过 */
/** 不要这一条：移到冗余，记录改成未识别（之后仍能「重新整理」捞回） */
export const ignoreRecord = (id: number) =>
  http.post<{ message?: string }>(`/organize/records/${id}/ignore`, {}, { timeoutMs: 5 * 60_000 })

export const deleteRecord = (id: number) => http.del(`/organize/records/${id}`)

export const clearRecords = (status: string) =>
  http.post<{ message?: string; removed: number }>('/organize/records/clear', { status })

/**
 * 深度删除：删掉这条记录整理出来的**网盘源文件**（进 115 回收站），
 * 连同本地 STRM/附属与台账。和上面的 deleteRecord（只删记录）是两回事。
 */
export const deepDeleteRecord = (id: number) =>
  http.post<{
    message?: string
    removed: number
    videos: number
    assets: number
    pan_dirs: number
    skipped: number
  }>(`/organize/records/${id}/deep-delete`, {}, { timeoutMs: 10 * 60_000 })

/** 重新整理会动网盘与本地文件，和整理/同步互斥，耗时按分钟计 */
/** 暂存指定（不执行）；pick 为空 = 撤销暂存 */
export const setPending = (id: number, pick?: { tmdbId: number; mediaType: string; label?: string }) =>
  http.put<{ message: string }>(
    `/organize/records/${id}/pending`,
    pick ? { tmdb_id: pick.tmdbId, media_type: pick.mediaType, label: pick.label } : {},
  )

/** 统一提交勾选的记录：暂存了指定的按指定执行，已识别的待确认合成一个批量确认 */
export const submitRecords = (ids: number[]) =>
  http.post<{ message: string; job_ids: number[]; eta_sec: number }>('/organize/records/submit', { ids })

/** 重新整理 → 入任务队列立即返回 */
export const redoRecord = (id: number, tmdbId: number, mediaType: string, label?: string) =>
  http.post<QueuedReply>(`/organize/records/${id}/redo`, { tmdb_id: tmdbId, media_type: mediaType, label })

// ---- 识别记忆（人工改指定过的「片名 + 年份 → 条目」）----

export interface RecognizeMemory {
  id: number
  /** 归一化后的片名（小写、去标点），识别时按它匹配 */
  title_key: string
  year: string
  tmdb_id: number
  media_type: 'movie' | 'tv' | string
  /** TMDB 片名 */
  title: string
  /** 当时人工指定的那条记录的原名 */
  sample: string
  hits: number
  created_at: string
  updated_at: string
}

export const listRecognizeMemory = () =>
  http.get<{ data: RecognizeMemory[] }>('/organize/recognize-memory')

export const deleteRecognizeMemory = (id: number) =>
  http.del<{ message?: string }>(`/organize/recognize-memory/${id}`)

export const clearRecognizeMemory = () =>
  http.post<{ message?: string; removed: number }>('/organize/recognize-memory/clear', {})

// ---- 工作目录一键创建：网盘根下 /StrmStation/{转存,待整理,已存在,冗余}，只补未配置的 ----
export interface WorkspaceInitResult {
  message: string
  created: { key: string; label: string; cid: string; path: string }[]
}
export const initWorkspace = () =>
  http.post<WorkspaceInitResult>('/organize/workspace/init', undefined, { timeoutMs: 120_000 })
