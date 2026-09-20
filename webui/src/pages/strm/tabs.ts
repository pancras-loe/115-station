import { DatabaseBackup, SlidersHorizontal, Zap } from '@lucide/vue'
import type { Component } from 'vue'

export interface StrmTab {
  key: string
  label: string
  /** 页签下的一句说明，替代「三个纯文字页签让人猜」的下划线 tabs */
  hint: string
  icon: Component
}

export const STRM_TABS: StrmTab[] = [
  { key: 'config', label: 'STRM 配置', hint: '直链域名与格式', icon: SlidersHorizontal },
  { key: 'full', label: '全量同步', hint: '整库扫描 / 失效检测', icon: DatabaseBackup },
  { key: 'incr', label: '增量同步', hint: '生活事件轮询 / 状态排查', icon: Zap },
]
