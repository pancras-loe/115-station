import type { PtResultKind } from '@/api/plugins'

/** 结果文案：「只确认登录」= 保号有效、签到没确认，别写成成功 */
export const RESULT_LABEL: Record<PtResultKind, string> = {
  ok: '签到成功',
  login: '只确认登录',
  fail: '失败',
}

/** 失败的短标签（卡片上的胶囊） */
export function failLabel(reason?: string): string {
  switch (reason) {
    case 'cookie':
      return 'Cookie 失效'
    case 'token':
      return 'Token 失效'
    case 'cf':
      return '被拦下'
    case 'net':
      return '打不开'
    case 'config':
      return '没填凭据'
    default:
      return '失败'
  }
}

export const HANDLER_LABEL: Record<string, string> = {
  nexus: '通用',
  keep: '只能保号',
  re0: 'RE0',
}
export function handlerLabel(h: string): string {
  return HANDLER_LABEL[h] ?? '专用'
}

/** 首字母色块的颜色：按名字算一个色相，明暗两套都看得清 */
export function logoHue(name: string): number {
  let h = 0
  for (const ch of name) h = (h * 31 + ch.charCodeAt(0)) % 360
  return h
}

export const TRIGGER_LABEL: Record<string, string> = {
  cron: '定时',
  manual: '手动',
  bot: '机器人',
  retry: '重试',
}
