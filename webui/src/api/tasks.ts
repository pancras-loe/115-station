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
  /** background = 还不进队列的后台任务（定时整理 / 转存触发 …）跑完留下的历史 */
  kind: 'redo' | 'confirm' | 'ignore' | 'deepdel' | 'organize' | 'full' | 'incr' | 'background' | string
  title: string
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

export const list = (limit = 30) => http.get<TaskJobList>('/tasks', { params: { limit } })
export const cancel = (id: number) => http.post<{ message: string }>(`/tasks/${id}/cancel`)
export const retry = (id: number) => http.post<{ message: string }>(`/tasks/${id}/retry`)
export const clear = () => http.post<{ message: string }>('/tasks/clear')
