import { ref } from 'vue'

/**
 * 链接转存：从一段粘贴文本里认出所有能提交的链接。
 * ed2k 常整批粘贴（一行一个，文件名里还可能带空格），115 官方分享文案会把
 * 「链接：… 访问码：…」写在一段话里，所以按链接形态抓，不按行切。
 */

export type LinkKind = 'share' | 'magnet' | 'ed2k' | 'http'

export interface ParsedLink {
  kind: LinkKind
  url: string
  /** 115 分享的提取码（链接里带的，或整段文本里唯一一个分享时从「提取码：」取） */
  code?: string
}

const RE_SHARE =
  /(?:https?:\/\/)?(?:www\.)?(?:115\.com|115cdn\.com|anxia\.com)\/s\/[a-z0-9_-]+(?:\?[^\s#<>"'，。；;）)]*)?(?:#[a-z0-9]*)?/gi
const RE_MAGNET = /magnet:\?xt=urn:btih:[a-z0-9]{16,}[^\s"'<>]*/gi
// ed2k 以「|/」收尾，文件名里可能有空格：懒惰匹配到「|/」后面跟空白或结尾
const RE_ED2K = /ed2k:\/\/\|file\|.+?\|\/(?=\s|$)/gim
const RE_CODE_IN_URL = /(?:[?&]password=|#)([a-z0-9]{4,})/i
const RE_CODE_IN_TEXT = /(?:提取码|访问码|密码|口令)\s*[:：]?\s*([a-z0-9]{4})/i
const RE_HTTP = /^(?:https?|ftp):\/\/\S+$/i

export function parseLinks(text: string, codeInput = ''): ParsedLink[] {
  const out: ParsedLink[] = []
  const seen = new Set<string>()
  const add = (l: ParsedLink) => {
    if (seen.has(l.url)) return
    seen.add(l.url)
    out.push(l)
  }
  const shares = text.match(RE_SHARE) ?? []
  for (const url of shares) {
    let code = url.match(RE_CODE_IN_URL)?.[1] ?? ''
    // 文本里的「提取码」和输入框只在只有一个分享时才分得清是谁的
    if (!code && shares.length === 1) code = codeInput.trim() || text.match(RE_CODE_IN_TEXT)?.[1] || ''
    add({ kind: 'share', url, code })
  }
  for (const url of text.match(RE_MAGNET) ?? []) add({ kind: 'magnet', url })
  for (const url of text.match(RE_ED2K) ?? []) add({ kind: 'ed2k', url: url.trim() })
  // 普通 HTTP 下载链接只认「整段就是一个链接」：网页里随手复制来的一堆 URL 多半不是文件
  const t = text.trim()
  if (!out.length && RE_HTTP.test(t) && !t.toLowerCase().includes('themoviedb.org')) add({ kind: 'http', url: t })
  return out
}

/** 在「找资源」里贴了链接：带到「链接转存」页签，不用再粘一遍 */
export const pendingLinkText = ref('')
