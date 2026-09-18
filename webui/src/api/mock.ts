/**
 * 演示用假数据。只在 `VITE_MOCK=1` 的构建里被引入（见 main.ts 的条件 import），
 * 正式构建里整个模块会被 tree-shake 掉，不会进产物。
 *
 * 用途：本地没有 Go 后端 / 没有 115 账号时预览界面与配色。
 */
import type { Dashboard } from '@/types/dashboard'

const TITLES: [string, string, string][] = [
  ['沙丘 2', '2024', '科幻电影'],
  ['奥本海默', '2023', '剧情电影'],
  ['流浪地球 2', '2023', '科幻电影'],
  ['三体', '2023', '国产剧集'],
  ['最后生还者', '2023', '欧美剧集'],
  ['疾速追杀 4', '2023', '动作电影'],
  ['铃芽之旅', '2022', '动画电影'],
  ['狂飙', '2023', '国产剧集'],
  ['西部世界', '2022', '欧美剧集'],
  ['蜘蛛侠：纵横宇宙', '2023', '动画电影'],
  ['漫长的季节', '2023', '国产剧集'],
  ['继承之战', '2023', '欧美剧集'],
]

const dashboard: Dashboard = {
  emby: null,
  storage: {
    username: 'demo_user',
    used: 8_243_000_000_000,
    total: 11_000_000_000_000,
    used_h: '7.50 TB',
    total_h: '10.00 TB',
  },
  media: { movies: 1284, tvs: 312, movies_month: 47, tvs_month: 9, total: 1596 },
  strm: { total: 18_432, invalid: 26, active: 18_406 },
  synced_files: 41_209,
  organized: 1596,
  recent_media: TITLES.map(([title, year, category], i) => ({
    title,
    year,
    category,
    type: category.includes('剧集') ? 'tv' : 'movie',
    poster: '',
    at: `09-${String(18 - Math.floor(i / 2)).padStart(2, '0')} ${String(9 + i).padStart(2, '0')}:24`,
  })),
  weekly: [
    { day: '09-12', count: 6 },
    { day: '09-13', count: 14 },
    { day: '09-14', count: 3 },
    { day: '09-15', count: 0 },
    { day: '09-16', count: 21 },
    { day: '09-17', count: 11 },
    { day: '09-18', count: 8 },
  ],
  week_total: 63,
  categories: [
    { name: '科幻电影', count: 218, posters: [] },
    { name: '国产剧集', count: 164, posters: [] },
    { name: '欧美剧集', count: 148, posters: [] },
    { name: '动画电影', count: 121, posters: [] },
    { name: '动作电影', count: 96, posters: [] },
    { name: '纪录片', count: 43, posters: [] },
  ],
  sys: { mem_total_mb: 16_384, mem_used_mb: 11_900, mem_percent: 72.6, cpu_percent: 23.4 },
  pending_events: 3,
}

const ROUTES: Record<string, unknown> = {
  '/auth/status': { initialized: true },
  '/auth/login': { token: 'mock-token', username: 'demo' },
  '/version': { version: '2.4.1-preview' },
  '/dashboard': dashboard,
}

export function installMockApi() {
  const real = window.fetch.bind(window)
  window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
    const path = url.replace(/^.*\/api/, '').split('?')[0]
    if (path in ROUTES) {
      // 留一点延迟，好让骨架屏/加载态在演示里真的出现
      await new Promise((r) => setTimeout(r, 180))
      return new Response(JSON.stringify(ROUTES[path]), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    return real(input as RequestInfo, init)
  }
  console.info('[mock] 假数据模式已启用，接口不会真正发出')
}
