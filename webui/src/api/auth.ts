import { http } from './client'

export interface AuthStatus {
  /** false = 容器未配置 AUTH_USER/AUTH_PASSWORD，登录页需要给出配置指引 */
  initialized: boolean
}

export interface LoginResult {
  token: string
  username: string
  /** 密码对了但开着二步验证：不给令牌，带上验证码再提交一次 */
  otp_required?: boolean
}

/** 短超时：鉴权状态挂起时要快速落到登录页，而不是让整页停在空白 */
export const status = () => http.get<AuthStatus>('/auth/status', { timeoutMs: 10_000 })

export const login = (username: string, password: string, otp?: string) =>
  http.post<LoginResult>('/auth/login', { username, password, otp })

export interface OtpStatus {
  enabled: boolean
  /** 登录令牌有效期（分钟），ACCESS_TOKEN_EXPIRE_MINUTES */
  token_expire: number
}
export interface OtpSetup {
  secret: string
  uri: string
  /** 二维码 PNG 的 data URL */
  qr: string
}

export const otpStatus = () => http.get<OtpStatus>('/auth/otp')
export const otpGenerate = () => http.post<OtpSetup>('/auth/otp/generate')
export const otpEnable = (code: string) => http.post('/auth/otp/enable', { code })
export const otpDisable = (password: string) => http.post('/auth/otp/disable', { password })

export const updateAccount = (payload: { username?: string; password?: string; old_password?: string }) =>
  http.post('/auth/update-account', payload)

export interface LoginWallpaper {
  /** TMDB backdrop_path；大图走 /auth/wallpaper?path=&w=（本站转码好的，只认列表里的路径） */
  path: string
  title: string
  year?: string
  /** 32px 小图的 data URI，大图到之前模糊着铺底 */
  thumb: string
}

/** 登录页背景剧照（TMDB 本周热门，后端预取到本地的）。后端不现场访问 TMDB，一张都没有时返回空列表 */
export const wallpapers = () => http.get<{ items: LoginWallpaper[] }>('/auth/wallpapers', { timeoutMs: 10_000 })
