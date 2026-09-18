import { http } from './client'

export interface Cd2Config {
  endpoint: string
  username: string
  password: string
  org_enabled: boolean
  /** 以下三个目录由后端自动派生，前端只读展示，不提交 */
  root_path?: string
  org_pending?: string
  org_existing?: string
}

export interface Cd2OrgStatus {
  enabled: boolean
  running: boolean
  organized?: number
  last_event?: string
  last_err?: string
}

export const getConfig = () => http.get<{ data?: Cd2Config }>('/cd2/config')

export const saveConfig = (body: Pick<Cd2Config, 'endpoint' | 'username' | 'password' | 'org_enabled'>) =>
  http.post<{ message?: string }>('/cd2/config', body)

/** 后端用的是已保存的凭据，所以调用方必须先 saveConfig 再 test */
export const test = () => http.post<{ message?: string }>('/cd2/test')

export const orgStatus = () => http.get<{ data?: Cd2OrgStatus }>('/cd2/org/status')
export const orgRun = () => http.post<{ message?: string }>('/cd2/org/run')
