import { ref, shallowRef } from 'vue'

/**
 * 全站反馈：轻提示（toast）与确认框。
 *
 * 状态放在模块单例里，渲染由 App.vue 挂的 HToaster / HDialogHost 负责——
 * store、api 层、composable 这些非组件代码也要能弹提示，不能依赖组件上下文。
 * （原来用 Naive 的 useMessage 时得靠一个 FeedbackBridge 空组件把实例捞出来，现在不需要了。）
 */

export type ToastTone = 'success' | 'warning' | 'danger' | 'accent'

export interface Toast {
  id: number
  tone: ToastTone
  text: string
}

/** 同时最多几条：连续操作（批量确认、逐项保存）时旧提示往上挤掉，不铺满屏 */
const MAX_TOASTS = 4

export const toasts = ref<Toast[]>([])
let seq = 0

function push(tone: ToastTone, text: string, ms?: number) {
  const id = ++seq
  toasts.value = [...toasts.value, { id, tone, text }].slice(-MAX_TOASTS)
  // 错误多停一会：通常要读完原因才知道下一步怎么办
  const life = ms ?? (tone === 'danger' ? 6000 : tone === 'warning' ? 4500 : 3000)
  window.setTimeout(() => dismissToast(id), life)
}

export function dismissToast(id: number) {
  toasts.value = toasts.value.filter((t) => t.id !== id)
}

/** duration 是毫秒；不传按语气给默认值（错误停得最久） */
export interface MessageOptions {
  duration?: number
}

export interface MessageApi {
  success: (text: string, opts?: MessageOptions) => void
  info: (text: string, opts?: MessageOptions) => void
  warning: (text: string, opts?: MessageOptions) => void
  error: (text: string, opts?: MessageOptions) => void
}

const message: MessageApi = {
  success: (t, o) => push('success', t, o?.duration),
  info: (t, o) => push('accent', t, o?.duration),
  warning: (t, o) => push('warning', t, o?.duration),
  error: (t, o) => push('danger', t, o?.duration),
}

// ---- 确认框 ----

export interface DialogAction<V> {
  label: string
  value: V
  variant?: 'primary' | 'secondary' | 'tertiary' | 'danger'
}

export interface DialogOptions<V> {
  title: string
  content: string
  tone?: ToastTone
  /** 按钮从左到右；点遮罩 / Esc / 右上角关闭都返回 null */
  actions: DialogAction<V>[]
}

export interface PendingDialog {
  opts: DialogOptions<unknown>
  resolve: (v: unknown) => void
}

export const currentDialog = shallowRef<PendingDialog | null>(null)

export interface DialogApi {
  confirm: <V>(opts: DialogOptions<V>) => Promise<V | null>
}

const dialog: DialogApi = {
  confirm<V>(opts: DialogOptions<V>) {
    // 同一时刻只有一个确认框：前一个没答就被新的顶掉时按「取消」了结，免得 Promise 悬着
    currentDialog.value?.resolve(null)
    return new Promise<V | null>((resolve) => {
      currentDialog.value = {
        opts: opts as DialogOptions<unknown>,
        resolve: (v) => {
          currentDialog.value = null
          resolve(v as V | null)
        },
      }
    })
  },
}

export interface Feedback {
  message: MessageApi
  dialog: DialogApi
}

const instance: Feedback = { message, dialog }

/** 组件内外通用 */
export function useFeedback(): Feedback {
  return instance
}

/** 统一的错误提示：Error 取 message，其余转字符串 */
export function toastError(e: unknown, fallback = '操作失败') {
  const msg = e instanceof Error ? e.message : String(e ?? '')
  message.error(msg || fallback)
}
