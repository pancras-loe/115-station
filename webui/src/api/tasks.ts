import { http } from './client'

/** 任务队列（后端 internal/api/taskqueue.go） */
export type TaskJobStatus = 'queued' | 'running' | 'success' | 'failed' | 'canceled' | 'interrupted'

export interface TaskJobProgress {
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
export const detail = (id: number) => http.get<{ data: TaskJobDetail }>(`/tasks/${id}`, { timeoutMs: 15_000 })
