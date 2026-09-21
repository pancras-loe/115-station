import { useSetting } from '@/composables/useSetting'
import type { DeepDeleteConfig } from '@/api/sync'

export function defaultDeepDel(): DeepDeleteConfig {
  return { enabled: false, max_batch: 50, max_ratio: 0.1, prune_pan_dirs: true, notify: true }
}

export function useDeepDelSetting() {
  // 保存时只保留现役字段，旧扫描、模式与预演配置不再参与执行。
  return useSetting<DeepDeleteConfig>('deepdel', defaultDeepDel(), {
    normalize: v => ({ enabled: v.enabled, max_batch: v.max_batch, max_ratio: v.max_ratio,
      prune_pan_dirs: v.prune_pan_dirs, notify: v.notify }),
  })
}
