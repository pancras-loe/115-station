import { http } from './client'

/** TG 群（消息来源） */
export interface TgSource {
  id?: number
  type: string
  name: string
  url: string
  priority: number
  note: string
  enabled: boolean
  last_id?: number
}

/** 关键词订阅 */
export interface TgItem {
  id?: number
  keyword: string
  /** 多个频道用换行分隔；留空表示用全部 TG 群 */
  channels: string
  /** 命中后自动转存 / 离线 */
  auto: boolean
  enabled: boolean
  last_id?: number
  last_hit?: string
}

export interface TgSubConfig {
  sources: TgSource[]
  items: TgItem[]
  interval_min: number
}

export const getConfig = async (): Promise<TgSubConfig> => {
  const d = await http.get<{ data?: Partial<TgSubConfig> }>('/tgsub/config')
  const c = d.data ?? {}
  return { sources: c.sources ?? [], items: c.items ?? [], interval_min: c.interval_min ?? 30 }
}

/**
 * 后端存的是整份配置，没有单条增删改接口 ——
 * 所以任何一条的改动都要「读全量 → 改 → 写回全量」，不能只 POST 变更的那条。
 */
export const saveConfig = (cfg: TgSubConfig) => http.post('/tgsub/config', cfg)

export const run = () => http.post<{ message?: string }>('/tgsub/run')
