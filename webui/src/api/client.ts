/**
 * HTTP 客户端。
 *
 * 这里的超时与重试策略不是模板代码，是旧版前端在跨境明文链路上踩出来的：
 *   1. 无超时的 fetch 在连接挂起时会永久 pending，页面停在「登录页和主界面
 *      都没显示」的空白态 —— 所以必须有默认超时。
 *   2. 该链路对连接的掐断是按概率的（同一批请求有的成功、有的 RESET）。
 *      GET 幂等，网络层失败自动换新连接重试一次，失败率从 p 降到 p²；
 *      POST 不重试，防重复提交。
 * 改动前先确认你了解这两条的来由。
 */

const DEFAULT_TIMEOUT = 60_000

export interface RequestOptions extends Omit<RequestInit, 'body'> {
  /** 查询参数，undefined / null / '' 的项会被丢弃 */
  params?: Record<string, unknown>
  /** JSON 请求体，自动序列化 */
  body?: unknown
  /** 覆盖默认超时；传 0 表示不超时 */
  timeoutMs?: number
}

/** 鉴权失效时由 auth store 注入，避免 api 层反向依赖 store */
let onUnauthorized: (() => void) | null = null
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn
}

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

export const tokenStorage = {
  get: () => localStorage.getItem('token'),
  set: (t: string) => localStorage.setItem('token', t),
  clear: () => localStorage.removeItem('token'),
}

function buildUrl(path: string, params?: Record<string, unknown>): string {
  const url = '/api' + path
  if (!params) return url
  const qs = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === '') continue
    qs.append(k, String(v))
  }
  const s = qs.toString()
  return s ? `${url}?${s}` : url
}

// 401 只引导一次：多个轮询同时收到 401 时不应触发重复跳转
let authRedirecting = false

async function requestOnce<T>(url: string, init: RequestInit): Promise<T> {
  const token = tokenStorage.get()
  const res = await fetch(url, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...init.headers,
    },
  })

  if (res.status === 401) {
    const data = await res.json().catch(() => ({}))
    const msg = data.error || data.message || ''
    // 只有来自鉴权中间件的 401（固定文案）才清令牌回登录页。业务接口自己的
    // 401（影巢登录未通过、115 OpenAPI 校验失败等）按普通错误提示 ——
    // 否则点一次影巢登录就把后台登录态误清了。
    if (msg === '未登录' || msg === '登录已过期') {
      if (!authRedirecting) {
        authRedirecting = true
        tokenStorage.clear()
        onUnauthorized?.()
        setTimeout(() => {
          authRedirecting = false
        }, 1000)
      }
      throw new ApiError('登录已过期', 401)
    }
    throw new ApiError(msg || '请求失败', 401)
  }

  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new ApiError(data.message || data.error || '请求失败', res.status)
  return data as T
}

export async function request<T = unknown>(path: string, options: RequestOptions = {}): Promise<T> {
  const { params, body, timeoutMs = DEFAULT_TIMEOUT, ...rest } = options
  const init: RequestInit = { ...rest }
  if (body !== undefined) init.body = typeof body === 'string' ? body : JSON.stringify(body)
  if (!init.signal && timeoutMs > 0) init.signal = AbortSignal.timeout(timeoutMs)

  const url = buildUrl(path, params)
  const isGet = !init.method || init.method.toUpperCase() === 'GET'

  for (let attempt = 0; ; attempt++) {
    try {
      return await requestOnce<T>(url, init)
    } catch (e) {
      // 只重试 GET 的网络层失败（TypeError = "Failed to fetch"），且只重试一次
      if (!(isGet && attempt === 0 && e instanceof TypeError)) throw e
      await new Promise((r) => setTimeout(r, 350 + Math.random() * 450))
    }
  }
}

export const http = {
  get: <T = unknown>(path: string, options?: RequestOptions) =>
    request<T>(path, { ...options, method: 'GET' }),
  post: <T = unknown>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>(path, { ...options, method: 'POST', body }),
  put: <T = unknown>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>(path, { ...options, method: 'PUT', body }),
  del: <T = unknown>(path: string, options?: RequestOptions) =>
    request<T>(path, { ...options, method: 'DELETE' }),
}
