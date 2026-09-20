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
    refresh_emby: false,
    cron_enabled: false,
    cron: '0 4 * * *',
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
  return useSetting<FullSyncConfig>('full', defaultFull())
}

export type FullSetting = ReturnType<typeof useFullSetting>
