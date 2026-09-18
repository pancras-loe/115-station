import type { DialogApi, LoadingBarApi, MessageApi, NotificationApi } from 'naive-ui'

export interface Feedback {
  message: MessageApi
  dialog: DialogApi
  notification: NotificationApi
  loadingBar: LoadingBarApi
}

let instance: Feedback | null = null

export function registerFeedback(f: Feedback) {
  instance = f
}

/** 组件内外通用；provider 挂载前调用会抛错，说明调用时机有问题 */
export function useFeedback(): Feedback {
  if (!instance) throw new Error('FeedbackBridge 尚未挂载')
  return instance
}

/** 统一的错误提示：Error 取 message，其余转字符串 */
export function toastError(e: unknown, fallback = '操作失败') {
  const msg = e instanceof Error ? e.message : String(e ?? '')
  instance?.message.error(msg || fallback)
}
