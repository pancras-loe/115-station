import { useSetting } from '@/composables/useSetting'
import type { DeepDeleteConfig } from '@/api/sync'

export function defaultDeepDel(): DeepDeleteConfig {
  return { enabled: false, prune_pan_dirs: true, notify: true }
}

/**
 * 由 SyncPage 创建一份，传给顶部状态总览和「深度删除」页签：
 * 各自 useDeepDelSetting 会得到两份互不通知的副本，页签里一保存，总览上的「已开启 / 未开启」还是旧的。
 */
export function useDeepDelSetting() {
  // 保存时只保留现役字段，旧扫描、模式、预演与单次量级阈值不再参与执行。
  return useSetting<DeepDeleteConfig>('deepdel', defaultDeepDel(), {
    normalize: v => ({ enabled: v.enabled, prune_pan_dirs: v.prune_pan_dirs, notify: v.notify }),
  })
}

export type DeepDelSetting = ReturnType<typeof useDeepDelSetting>
