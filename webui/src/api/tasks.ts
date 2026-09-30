import { http } from './client'

/** 任务队列（后端 internal/api/taskqueue.go） */
/** partial = 跑完了但有一部分没做成（刮削有片目 / 产物失败，或之后的 Emby 提前探测有条目失败） */
export type TaskJobStatus = 'queued' | 'running' | 'success' | 'partial' | 'failed' | 'canceled' | 'interrupted'

export interface TaskJobProgress {
  phase?: string
  done: number
  total: number
  label?: string
  /** 当前条目内部的进度（刮削一部剧：集 NFO 87/212 · 当前文件）；只在运行中出现 */
  sub?: TaskJobSubProgress
}

export interface TaskJobSubProgress {
  phase?: string
  done: number
  total: number
  label?: string
}

/** 入队接口的回复（202）：任务 id、排第几、预计多久后跑完 */
export interface QueuedReply {
  message: string
  job_id: number
  position: number
  eta_sec: number
}

/** 一个没探成的条目（后端 embyprobereport.go 的 jobProbeItem） */
export interface TaskProbeItem {
  label: string
  /** failed 这次请求失败 / held 刚请求过（防抖）、这次没请求 / missing Emby 里没有 / error 整条路径没法处理 */
  kind: 'failed' | 'held' | 'missing' | 'error'
  err?: string
  attempts?: number
  /** 防抖没过：这个时间之后才能再手动请求 */
  retry_at?: string
  /** 自动入口的次数已用完：入库后不会再自动探，只能手动 */
  auto_stopped?: boolean
}

/** 探测任务（kind=probe）的结果，任务跑的过程中持续更新 */
export interface TaskProbeReport {
  paths: number
  finished: number
  /** 这次要请求的视频数（逐个片目查到后累加） */
  planned: number
  ok: number
  failed: number
  held: number
  missing: number
  errors: number
  /** 任务停止后没探的 */
  canceled?: number
  items?: TaskProbeItem[]
  more?: number
  updated_at: string
  /** lost = 没探完服务就重启了，内存里的队列丢了 */
  state: 'queued' | 'running' | 'done' | 'canceled' | 'lost'
  /** 连续失败熔断，暂停到这个时间 */
  paused_until?: string
}

/** Emby 提前探测的全局状态（GET /tasks/probe） */
export interface ProbeFailRow {
  item_id: string
  label: string
  attempts: number
  last_err: string
  last_at: string
  /** 自动入口冷却中：最早什么时候会再自动试（前提是再次入库确认） */
  auto_retry_at?: string
  /** 自动入口次数用完 */
  auto_stopped?: boolean
  /** 防抖没过：这个时间之后才能手动重试 */
  manual_at?: string
  running?: boolean
  queued?: boolean
}

export interface ProbeStatus {
  enabled: boolean
  emby: boolean
  queue: number
  running: string
  paused_until?: string
  limits: { max_attempts: number; retry_hours: number; debounce_minutes: number; break_after: number; break_minutes: number }
  fails: ProbeFailRow[]
  /** 没成功、也没被忽略的条目总数（fails 只列最近 100 条） */
  fail_total: number
  /** 已忽略的条目数：不列出、不再自动探 */
  ignored_total: number
}

export interface TaskJob {
  id: number
  /**
   * transfer = 转存 / 离线下载完成后的自动整理；background = 不进队列的后台任务
   * （目前只剩 Emby 事件触发的深度删除）跑完留下的历史
   */
  kind:
    | 'redo'
    | 'confirm'
    | 'ignore'
    | 'deepdel'
    | 'organize'
    | 'orgpick'
    | 'scrape'
    | 'probe'
    | 'libredo'
    | 'filemove'
    | 'full'
    | 'incr'
    | 'transfer'
    | 'background'
    | string
  title: string
  /** 0 = 手动，1 = 后台（定时 / 转存触发）；排队中手动的排在前面 */
  priority: number
  status: TaskJobStatus
  source: string
  message: string
  created_at: string
  started_at?: string | null
  finished_at?: string | null
  /** 涉及的整理记录 id：任务结束后记录页据此刷新 */
  record_ids?: number[]
  /** 运行中为实时进度，结束后为最后一次快照 */
  progress?: TaskJobProgress
  /** 结构化结果（整理：{ success, exists, failed, awaiting }） */
  result?: Record<string, unknown>
  /** 结束后排进 Emby 提前探测的结果（手动刮削勾了「轨道探测」时才有），探测跑完前会继续更新 */
  probe?: TaskProbeReport
  /** 结束时另建的后续任务（手动刮削勾了轨道探测 → Emby 提前探测）及其当前状态 */
  follow?: { id: number; kind: string; title: string; status: TaskJobStatus }
  /** 排队中可取消 / 运行中可停止（逐条处理的任务才能在两条之间停） */
  stoppable?: boolean
  /** 排队中：第几位（从 1 起）与预计多少秒后跑完 */
  position?: number
  eta_sec?: number
}

export interface TaskJobList {
  data: TaskJob[]
  running: number
  queued: number
  /** 任务锁占用方：排队的任务在等谁（后台整理 / 增量同步） */
  lock: { busy: boolean; holder?: string; held_sec?: number; progress?: string }
}

/** 任务详情里列出的整理记录（只有列表要的几个字段） */
export interface TaskJobRecord {
  id: number
  source: string
  status: string
  title: string
  year: string
  media_type: string
  tmdb_id: number
}

export interface TaskJobDetail extends TaskJob {
  /** 涉及的整理记录：整理时记下 job_id 的 ∪ 参数里点名的，最多 50 条 */
  records: TaskJobRecord[]
  record_total: number
  /** 参数里对人有意义的部分（「指定 TMDB 123（剧集）」之类） */
  params_summary?: string[]
}

export type TaskHistoryQuery = {
  status?: string
  kind?: string
  source?: string
  q?: string
  page?: number
  size?: number
}

export interface TaskHistoryPage {
  data: TaskJob[]
  total: number
  /** 各结束状态的条数（含 all），不受 status 筛选影响 */
  counts: Record<string, number>
}

export const list = (limit = 30) => http.get<TaskJobList>('/tasks', { params: { limit } })
export const cancel = (id: number) => http.post<{ message: string }>(`/tasks/${id}/cancel`)
export const retry = (id: number) => http.post<{ message: string }>(`/tasks/${id}/retry`)
export const clear = () => http.post<{ message: string }>('/tasks/clear')
/** 已结束任务的历史（任务中心）；顶栏轮询仍走 list，别混用 */
export const history = (params: TaskHistoryQuery) =>
  http.get<TaskHistoryPage>('/tasks/history', { params, timeoutMs: 15_000 })
export const probeStatus = () => http.get<ProbeStatus>('/tasks/probe')
/** 失败清单里手动重试：建一个探测任务（409 = 都还在防抖期内） */
export const retryProbe = (itemIds: string[]) => http.post<QueuedReply>('/tasks/probe/retry', { item_ids: itemIds })
/** 失败清单里忽略（只打标记，记账保留）：item_ids 与 all 二选一 */
export const ignoreProbe = (body: { item_ids?: string[]; all?: boolean }) =>
  http.post<{ message: string; ignored: number }>('/tasks/probe/ignore', body)
/** 撤销全部忽略 */
export const unignoreProbe = () => http.post<{ message: string }>('/tasks/probe/unignore')
export const detail = (id: number) => http.get<{ data: TaskJobDetail }>(`/tasks/${id}`, { timeoutMs: 15_000 })
