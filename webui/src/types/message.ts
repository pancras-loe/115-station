/** 七个通道共用 Setting 表的同一个 key（'message'），所以整页是一份配置对象 */
export interface MessageConfig {
  wecom: {
    corp_id: string
    secret: string
    agent_id: string
    api_url: string
    token: string
    encoding_aes_key: string
    enabled: boolean
  }
  tg: { token: string; chat_id: string; enabled: boolean }
  feishu: { webhook: string; secret: string; enabled: boolean }
  qq_onebot: {
    url: string
    token: string
    target_type: 'group' | 'private'
    target: string
    admin: string
    event_token: string
    enabled: boolean
  }
  qq_official: { app_id: string; secret: string; group_id: string; enabled: boolean }
  /** IYUU（爱语飞飞）：types 空 = 全部类型都推 */
  iyuu: { token: string; types: string[]; enabled: boolean }
  /** 通用 Webhook：body 空 = 默认 JSON */
  webhook: { url: string; method: 'POST' | 'GET'; body: string; types: string[]; enabled: boolean }
}

/**
 * 消息类型（IYUU / Webhook 按它勾选）。与后端 notify_push.go 的 notifyKindNames 一一对应，
 * 改一边要改另一边；机器人回复（bot）不推这两个通道，所以不列。
 */
export const NOTIFY_KINDS: { value: string; label: string; hint: string }[] = [
  { value: 'ingest', label: '入库', hint: '新片 / 新集进了媒体库' },
  { value: 'organize', label: '整理', hint: '整理结果、洗版替换、待确认、整理出错' },
  { value: 'transfer', label: '下载转存', hint: '115 离线下载完成 / 失败' },
  { value: 'subscribe', label: '订阅', hint: '新订阅、补上了、长期找不到' },
  { value: 'delete', label: '删除', hint: '深度删除、Emby 里删了片' },
  { value: 'emby', label: 'Emby 事件', hint: 'Emby Webhook 转发的播放、入库等' },
  { value: 'checkin', label: '签到', hint: '115 / RE0 / 癫影 / PT 站签到' },
  { value: 'task', label: '定时任务', hint: '清理回收站 / 离线任务 / 目录、备份、封面生成' },
  { value: 'system', label: '系统', hint: '有新版本、已更新、媒体目录没挂上' },
]

export const MESSAGE_DEFAULTS: MessageConfig = {
  wecom: {
    corp_id: '',
    secret: '',
    agent_id: '',
    api_url: 'https://qyapi.weixin.qq.com',
    token: '',
    encoding_aes_key: '',
    enabled: false,
  },
  tg: { token: '', chat_id: '', enabled: false },
  feishu: { webhook: '', secret: '', enabled: false },
  qq_onebot: {
    url: '',
    token: '',
    target_type: 'group',
    target: '',
    admin: '',
    event_token: '',
    enabled: false,
  },
  qq_official: { app_id: '', secret: '', group_id: '', enabled: false },
  iyuu: { token: '', types: [], enabled: false },
  webhook: { url: '', method: 'POST', body: '', types: [], enabled: false },
}

/** 读回来的配置补齐新加的两个通道：getSetting 只浅合并，老配置里没有 iyuu / webhook，数组还会和默认值共用引用 */
export function normalizeMessage(v: MessageConfig): MessageConfig {
  const d = MESSAGE_DEFAULTS
  return {
    ...v,
    iyuu: { ...d.iyuu, ...v.iyuu, types: [...(v.iyuu?.types ?? [])] },
    webhook: { ...d.webhook, ...v.webhook, types: [...(v.webhook?.types ?? [])] },
  }
}
