/**
 * 整理台账里的 TMDB 海报：后端 /api/poster/*path 代理并落盘缓存（路径已带前导斜杠）。
 * size 按显示宽度挑（约两倍，给高分屏）：36px 的缩略图用 w92，拿 w342 就是白白多几倍流量
 */
export function posterUrl(path?: string | null, size?: 'w92' | 'w154' | 'w185' | 'w342'): string | null {
  if (!path) return null
  return size ? `/api/poster${path}?size=${size}` : `/api/poster${path}`
}

/**
 * Emby 图片走服务端代理，api_key 由后端注入 ——
 * 带密钥的封面 URL 会泄露凭据，之前已从通知里移除过一次，别再拼到前端。
 */
export function embyImageUrl(path: string, maxWidth = 200): string {
  return `/api/embyimg?path=${encodeURIComponent(path)}&maxWidth=${maxWidth}`
}
