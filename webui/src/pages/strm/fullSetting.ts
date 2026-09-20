import { useSetting } from '@/composables/useSetting'
import type { FullSyncConfig } from '@/api/sync'

/**
 * full 配置的默认值。
 *
 * 必须是工厂函数而不是模块级常量：useSetting 的 defaults 只做浅拷贝，
 * 数组字段（后缀列表）会和默认值对象共享同一个引用——做成常量的话，
 * 用户在 NDynamicTags 里删掉一个后缀就把「默认值」本身改了，
 * 之后点「重置配置」恢复出来的是被改过的那份。
 */
export function defaultFull(): FullSyncConfig {
  return {
    cid: '',
    cid_path: '',
    local_path: '/media',
    video_ext: ['mp4', 'mkv', 'ts', 'avi', 'mov', 'rmvb', 'webm', 'flv', 'm2ts', 'wmv', 'mpg', 'iso'],
    image_ext: ['jpg', 'png', 'jpeg', 'webp'],
    data_ext: ['ass', 'srt', 'ssa', 'sub'],
    mode: 'normal',
    detect_orphans: false,
    cron_enabled: false,
    cron: '0 4 * * *',
    // 深度删除会真删网盘内容：默认关、默认只标记、默认预演，三道都开着。
    // 阈值的默认值要和后端 deepdel.go 的 deepDelMaxBatchDefault / MaxRatioDefault 一致
    deep_delete: {
      enabled: false,
      mode: 'mark',
      dry_run: true,
      scan_interval_sec: 300,
      max_batch: 50,
      max_ratio: 0.1,
      prune_pan_dirs: true,
      notify: true,
    },
  }
}

/**
 * 全量同步配置。
 *
 * 在 StrmPage 里创建一份，再传给「全量同步」「增量同步」两个页签——
 * 两个页签各自 useSetting('full') 会得到两份互不通知的副本，
 * 在全量页改完 cid 保存，增量页手里还是旧值。
 */
export function useFullSetting() {
  return useSetting<FullSyncConfig>('full', defaultFull(), {
    /**
     * deep_delete 是嵌套对象，而 getSetting 的兜底只做浅展开：库里还没有这个字段时
     * model.deep_delete 会和 defaults 里那份共用同一个引用，用户在界面上一改就把
     * 「默认值」本身改掉了，之后点「重置配置」恢复出来的是被改过的那份
     * （与上面数组字段同款的坑）。顺带把旧配置缺的子字段补齐。
     */
    normalize: (v) => ({
      ...v,
      deep_delete: { ...defaultFull().deep_delete, ...(v.deep_delete ?? {}) },
    }),
  })
}

export type FullSetting = ReturnType<typeof useFullSetting>
