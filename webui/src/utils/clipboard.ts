/**
 * 写剪贴板。返回是否成功，失败时调用方自己兜底（选中文本 / 弹框让用户手动复制）。
 *
 * `navigator.clipboard` 只在安全上下文（HTTPS 或 localhost）里存在，
 * 本项目常见的部署是明文 HTTP 直连 IP:端口，那里它是 undefined —— 此前两个复制按钮
 * 只调它，于是在大多数用户那儿点了没反应。退回老的 `execCommand('copy')`：
 * 已废弃但各浏览器仍支持，且不要求安全上下文，只要求在用户点击的同步调用栈里。
 */
export async function copyText(text: string): Promise<boolean> {
  if (window.isSecureContext && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // 权限被拒等情况，再试一次老办法
    }
  }
  return legacyCopy(text)
}

function legacyCopy(text: string): boolean {
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
  // 移出视口而不是 display:none，隐藏元素选不中；readonly 避免手机弹键盘
  ta.style.cssText = 'position:fixed;top:0;left:-9999px;opacity:0;'
  // 弹窗 / 抽屉里有焦点陷阱（Reka FocusScope），挂到 body 上一 focus 就被抢回去、选区跟着没了，
  // 所以挂进当前焦点所在的对话框里
  const host = document.activeElement?.closest('[role="dialog"]') ?? document.body
  const prev = document.activeElement as HTMLElement | null
  host.appendChild(ta)
  let ok = false
  try {
    ta.focus()
    ta.select()
    ta.setSelectionRange(0, text.length)
    ok = document.execCommand('copy')
  } catch {
    ok = false
  } finally {
    host.removeChild(ta)
    prev?.focus?.()
  }
  return ok
}
