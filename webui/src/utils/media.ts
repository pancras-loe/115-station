/** 本地刮削海报：后端 /api/poster/*path 直接透传路径（路径已带前导斜杠） */
export function posterUrl(path?: string | null): string | null {
  return path ? `/api/poster${path}` : null
}

/**
 * Emby 图片走服务端代理，api_key 由后端注入 ——
 * 带密钥的封面 URL 会泄露凭据，之前已从通知里移除过一次，别再拼到前端。
 */
export function embyImageUrl(path: string, maxWidth = 200): string {
  return `/api/embyimg?path=${encodeURIComponent(path)}&maxWidth=${maxWidth}`
}

/** 观影门户跑在独立端口 6688（见 internal/api/portal.go） */
export function openPortal() {
  window.open(`${location.protocol}//${location.hostname}:6688`, '_blank', 'noopener')
}
