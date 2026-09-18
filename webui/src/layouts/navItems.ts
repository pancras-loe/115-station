import {
  LayoutDashboard,
  Rss,
  UserRoundCog,
  RefreshCcw,
  CloudCog,
  Wand2,
  ArrowDownUp,
  Download,
  Settings,
  MessageSquare,
  Blocks,
  ScrollText,
} from '@lucide/vue'
import type { Component } from 'vue'

export interface NavItem {
  name: string
  label: string
  icon: Component
  /** 分组标题，仅首项携带 */
  group?: string
}

/**
 * 旧版侧边栏用 ▦ ✦ ◈ ⇄ ☁ 这类字符当图标——不同平台字形宽窄不一、无法统一
 * 描边粗细，是「廉价感」的主要来源。这里换成真图标集。
 * 顺序沿用旧版，用户的肌肉记忆不打断；只额外做了分组。
 */
export const navItems: NavItem[] = [
  { name: 'dashboard', label: '总览面板', icon: LayoutDashboard, group: '概览' },
  { name: 'tgsub', label: '订阅管理', icon: Rss },

  { name: 'accounts', label: '账号管理', icon: UserRoundCog, group: '媒体库' },
  { name: 'sync', label: '账号同步', icon: RefreshCcw },
  { name: 'cd2', label: 'CloudDrive2', icon: CloudCog },
  { name: 'organize', label: '自动整理', icon: Wand2 },

  { name: 'upload-download', label: '上传下载', icon: ArrowDownUp, group: '传输' },
  { name: 'media-transfer', label: '影视转存', icon: Download },

  { name: 'settings', label: '系统配置', icon: Settings, group: '系统' },
  { name: 'message', label: '消息配置', icon: MessageSquare },
  { name: 'plugins', label: '扩展功能', icon: Blocks },
  { name: 'logs', label: '实时日志', icon: ScrollText },
]
