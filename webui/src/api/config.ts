import { http } from './client'

/**
 * 后端的通用配置存储是 Setting 表的 key -> value(JSON 字符串)，
 * 所以这里统一负责序列化/反序列化，页面只面对对象。
 */
export async function getSetting<T>(key: string, fallback: T): Promise<T> {
  const data = await http.get<{ value?: string }>('/config/setting', { params: { key } })
  if (!data.value) return fallback
  try {
    // 后端存的是任意 JSON 字符串；解析失败时回退默认值而不是让整页崩掉
    return { ...fallback, ...(JSON.parse(data.value) as object) } as T
  } catch {
    return fallback
  }
}

export const saveSetting = (key: string, value: unknown) =>
  http.post('/config/setting', { key, value: JSON.stringify(value) })

// ---- TMDB 用独立接口，不走 Setting 表 ----
export interface TmdbConfig {
  api_url: string
  image_url: string
  api_key: string
  language: string
}

export const getTmdb = () => http.get<{ data?: TmdbConfig } & Partial<TmdbConfig>>('/config/tmdb')

/** 注意字段名：读回来是 image_url，写进去是 image_api_url（后端历史遗留，别对齐） */
export const saveTmdb = (c: TmdbConfig) =>
  http.post('/config/tmdb', {
    api_url: c.api_url,
    image_api_url: c.image_url,
    api_key: c.api_key,
    language: c.language,
  })

// ---- 连通性测试 ----
export interface TestResult {
  ok: boolean
  error?: string
  latency_ms?: number
}

export const testTmdb = () => http.post<TestResult>('/config/test-tmdb')
export const testGpt = (body: { url: string; key: string; model: string }) =>
  http.post<TestResult>('/config/test-gpt', body)

export const testProxy = (url: string) => http.post<TestResult>('/proxy/test', { url })

export interface NetworkCheckItem {
  name: string
  ok: boolean
  error?: string
  latency_ms?: number
}
export const networkCheck = () => http.get<{ results: NetworkCheckItem[] }>('/network/check')

export interface EmbyTestResult extends TestResult {
  server_name?: string
  version?: string
  library_count?: number
}
export const testEmby = (body: { server_url: string; api_key: string }) =>
  http.post<EmbyTestResult>('/config/test-emby', body)
