import { http } from './client'

export interface AuthStatus {
  /** false = 容器未配置 AUTH_USER/AUTH_PASSWORD，登录页需要给出配置指引 */
  initialized: boolean
}

export interface LoginResult {
  token: string
  username: string
}

/** 短超时：鉴权状态挂起时要快速落到登录页，而不是让整页停在空白 */
export const status = () => http.get<AuthStatus>('/auth/status', { timeoutMs: 10_000 })

export const login = (username: string, password: string) =>
  http.post<LoginResult>('/auth/login', { username, password })

export const updateAccount = (payload: { username?: string; password?: string; old_password?: string }) =>
  http.post('/auth/update-account', payload)
