import { useSetting } from '@/composables/useSetting'
import type { DeepDeleteConfig } from '@/api/sync'

/**
 * 深度删除配置的默认值。
 *
 * **这三个默认值是安全底线，改之前先想清楚**：默认关、默认只标记、默认预演。
 * 这条链路会真删用户网盘里的内容，三道开关要用户自己一层层打开。
 *
 * 阈值的默认值必须和后端 deepdel.go 的 deepDelMaxBatchDefault /
 * deepDelMaxRatioDefault 一致，否则界面显示的和实际生效的对不上。
 *
 * 工厂函数而不是模块级常量：useSetting 的 defaults 只做浅拷贝，
 * 做成常量的话用户改一下就把「默认值」本身改了（同 defaultFull 的脚注）。
 */
export function defaultDeepDel(): DeepDeleteConfig {
  return {
    enabled: false,
    mode: 'mark',
    dry_run: true,
    scan_interval_sec: 300,
    max_batch: 50,
    max_ratio: 0.1,
    prune_pan_dirs: true,
    notify: true,
  }
}

/**
 * 深度删除配置，独立 setting key「deepdel」。
 *
 * 不跟 full 合住：深度删除与全量同步没有依赖关系（界面也已经拆成两个页签），
 * 同一个 key 被两个页面整存整取就是 incr.cron / incr.interval_sec 那个
 * 互相覆盖的坑。
 */
export function useDeepDelSetting() {
  return useSetting<DeepDeleteConfig>('deepdel', defaultDeepDel())
}
