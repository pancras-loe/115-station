import { http } from './client'
import type { CalibrateResult, Dashboard } from '@/types/dashboard'

/** refresh=true 跳过后端 60 秒的 Emby 缓存：用户点「刷新」就是想立刻核对数字 */
export const get = (refresh = false) =>
  http.get<Dashboard>('/dashboard', { params: refresh ? { refresh: 1 } : undefined })

/** 媒体库台账校准。apply=false 只预演，返回将被清除的条目 */
export const calibrate = (apply: boolean) =>
  http.post<CalibrateResult>('/media-library/calibrate', { apply })
