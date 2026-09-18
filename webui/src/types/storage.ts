/** 与 internal/model/model.go 的 Storage 一一对应（Cookie / AppSecret 后端标了 json:"-"，不下发） */
export interface Storage {
  id: number
  name: string
  type: string
  cookie_path: string
  device: string
  interval: number
  status: string
  file_count: number
  openapi_enabled: boolean
  app_id: string
  app_key: string
  created_at: string
  updated_at: string
}

export interface LoginDevice {
  name?: string
  device?: string
  ip?: string
  city?: string
  /** 秒级时间戳 */
  utime?: number
  is_current?: boolean
}

/** POST /api/storage/check 的响应。OpenAPI 通道命中时只返回 username/capacity/channel */
export interface StorageCheck {
  valid: boolean
  message?: string
  channel?: string
  username?: string
  capacity?: string
  avatar?: string
  user_id?: number
  /** 0 = 非会员 */
  vip?: number
  /** 秒级时间戳 */
  vip_expire?: number
  /** 1 或 true = 终身会员 */
  vip_forever?: number | boolean
  is_privilege?: boolean
  used_size?: number
  total_size?: number
  devices?: LoginDevice[]
}

export interface QrCode {
  qrcode?: string
  uid?: string
  time?: number | string
  sign?: string
  error?: string
}

export type QrStatus = 'waiting' | 'scanned' | 'success' | 'expired' | 'cancelled'

export interface QrStatusResult {
  status: QrStatus
  username?: string
  error?: string
}

/** 115 Cookie 的设备通道。网页端与 115Browser UA 配套，兼容性最好 */
export const DEVICE_OPTIONS = [
  { label: '115浏览器_网页端（推荐）', value: 'web' },
  { label: '115生活_iOS端', value: '115ios' },
  { label: '115生活_Android端', value: '115android' },
  { label: '115生活_支付宝小程序', value: 'alipaymini' },
  { label: '115生活_微信小程序', value: 'wechatmini' },
  { label: '115生活_TV端', value: 'tv' },
  { label: '115浏览器_Android端', value: 'qandroid' },
]
