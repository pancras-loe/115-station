import { useSetting } from '@/composables/useSetting'
import type { DeepDeleteConfig } from '@/api/sync'

export function defaultDeepDel(): DeepDeleteConfig {
  return { enabled: false, prune_pan_dirs: true, notify: true }
}

export function useDeepDelSetting() {
  // 保存时只保留现役字段，旧扫描、模式、预演与单次量级阈值不再参与执行。
  return useSetting<DeepDeleteConfig>('deepdel', defaultDeepDel(), {
    normalize: v => ({ enabled: v.enabled, prune_pan_dirs: v.prune_pan_dirs, notify: v.notify }),
  })
}
