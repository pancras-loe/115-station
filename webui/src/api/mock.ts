/**
 * 演示用假数据。只在 `VITE_MOCK=1` 的构建里被引入（见 main.ts 的条件 import），
 * 正式构建里整个模块会被 tree-shake 掉，不会进产物。
 *
 * 用途：本地没有 Go 后端 / 没有 115 账号时预览界面与配色。
 */
import type { Dashboard } from '@/types/dashboard'
import type { OrganizeRecord, OrganizeRecordFile } from '@/api/organize'

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
  media: {
    movies: 1284,
    tvs: 312,
    total: 1596,
    episodes: 9_841,
    movies_month: 47,
    tvs_month: 9,
    local_movies: 1284,
    local_tvs: 312,
    source: 'local',
  },
  strm: { total: 18_432, orphan: 26, missing: 4, missing_sampled: 500, active: 18_406 },
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

/** 整理记录：几种状态各来一条，覆盖待确认 / AI 识别 / 来源链接 / 文件清单这些分支 */
const ago = (min: number) => new Date(Date.now() - min * 60_000).toISOString()
const baseRecord = {
  batch_id: 'b1',
  source_fid: '',
  source_cid: '',
  stage: '',
  message: '',
  poster_path: '',
  category: '',
  target_dir: '',
  target_cid: '',
  video_count: 1,
  total_size: 0,
  strm_created: 0,
  scrape_state: '',
  scrape_msg: '',
  manual_tmdb: false,
  redo_count: 0,
  file_list: [] as OrganizeRecordFile[],
}
const records: OrganizeRecord[] = [
  {
    ...baseRecord,
    id: 1,
    source: 'Dune.Part.Two.2024.2160p.WEB-DL.DV.HDR.H265.mkv',
    source_kind: 'file',
    status: 'awaiting',
    tmdb_id: 693134,
    title: '沙丘 2',
    year: '2024',
    media_type: 'movie',
    target_dir: '/媒体库/电影/科幻电影/沙丘 2 (2024)',
    recog_via: 'ai_title',
    ai_score: 92,
    ai_note: '文件名英文片名与 TMDB 原名一致，年份吻合',
    created_at: ago(3),
    total_size: 32_500_000_000,
    link: { id: 1, kind: 'magnet', url: 'magnet:?xt=urn:btih:5f3c2e8a9d1b4c7e6f0a2b3c4d5e6f7a8b9c0d1e', name: 'Dune', source: 'TG订阅', created_at: ago(40) },
  },
  {
    ...baseRecord,
    id: 2,
    source: '[某字幕组] 未知动画 01-12 [1080p]/',
    source_kind: 'dir',
    status: 'awaiting',
    tmdb_id: 0,
    title: '',
    year: '',
    media_type: '',
    message: '未能自动识别，请重新指定 TMDB 条目',
    created_at: ago(8),
    video_count: 12,
  },
  {
    ...baseRecord,
    id: 3,
    source: 'The.Last.of.Us.S01.1080p.BluRay/',
    source_kind: 'dir',
    status: 'success',
    tmdb_id: 100088,
    title: '最后生还者',
    year: '2023',
    media_type: 'tv',
    category: '欧美剧集',
    target_dir: '/媒体库/剧集/欧美剧集/最后生还者 (2023)/Season 01',
    message: '→ /媒体库/剧集/欧美剧集/最后生还者 (2023)/Season 01',
    video_count: 9,
    strm_created: 9,
    total_size: 41_000_000_000,
    created_at: ago(95),
    file_list: [
      { fid: 'f1', name: '最后生还者 S01E01.mkv', orig: 'The.Last.of.Us.S01E01.1080p.BluRay.mkv', kind: 'video', size: 4_600_000_000 },
      { fid: 'f2', name: '最后生还者 S01E02.mkv', orig: 'The.Last.of.Us.S01E02.1080p.BluRay.mkv', kind: 'video', size: 4_400_000_000 },
      { fid: 'f3', name: '最后生还者 S01E01.chs.ass', kind: 'subtitle', size: 88_000 },
    ],
    link: { id: 2, kind: 'share', url: 'https://115cdn.com/s/swzabc123?password=x1y2', name: 'TLOU', source: 'web', created_at: ago(120) },
  },
  {
    ...baseRecord,
    id: 4,
    source: 'Oppenheimer.2023.2160p.UHD.BluRay.mkv',
    source_kind: 'file',
    status: 'exists',
    tmdb_id: 872585,
    title: '奥本海默',
    year: '2023',
    media_type: 'movie',
    category: '剧情电影',
    message: '库内已有相同或更优版本（洗版策略判定保留旧版）',
    created_at: ago(60 * 26),
    file_list: [{ fid: 'f4', name: '奥本海默 (2023).mkv', kind: 'video', size: 68_000_000_000 }],
  },
  {
    ...baseRecord,
    id: 5,
    source: 'Some.Random.Clip.2019.mp4',
    source_kind: 'file',
    status: 'failed',
    stage: 'move',
    tmdb_id: 0,
    title: '',
    year: '',
    media_type: '',
    message: '搬移失败：115 返回「操作过于频繁」，已计入下一轮重试',
    created_at: ago(60 * 50),
  },
]

/** 配置项（/config/setting?key=…）：后端存的是 JSON 字符串，原样模拟 */
const SETTINGS: Record<string, unknown> = {
  strm: { domain: 'http://192.168.1.10:6086', format: 'pick_code_name', keep_ext: 'true', exist: 'overwrite' },
  full: {
    cid: '2894561234',
    cid_path: '/媒体库',
    local_path: '/media',
    video_ext: ['mp4', 'mkv', 'ts', 'iso'],
    image_ext: ['jpg', 'png'],
    data_ext: ['ass', 'srt'],
    mode: 'fast',
    detect_orphans: true,
    refresh_emby: false,
    cron_enabled: true,
    cron: '0 4 * * *',
  },
  incr: { cron: '*/30 * * * *', interval_sec: 30 },
  deepdel: { enabled: true, prune_pan_dirs: true, notify: false },
}

type Route = unknown | ((q: URLSearchParams) => unknown)

const ROUTES: Record<string, Route> = {
  '/config/setting': (q: URLSearchParams) => {
    const v = SETTINGS[q.get('key') ?? '']
    return v === undefined ? {} : { value: JSON.stringify(v) }
  },
  '/tasks': {
    running: 1,
    queued: 2,
    lock: { busy: true, holder: '全量同步', held_sec: 84 },
    data: [
      {
        id: 12, kind: 'full', title: '全量同步', priority: 0, status: 'running', source: 'web', message: '',
        created_at: '2026-09-26T10:00:00+08:00', started_at: '2026-09-26T10:00:02+08:00',
        progress: { phase: '全量同步', done: 0, total: 0, label: '已扫描 3,120 / 约 18,400 个文件 · 当前：/媒体库/剧集/欧美剧集' },
      },
      {
        id: 13, kind: 'redo', title: '重新整理《三体.S01》→ 三体 (2023)', priority: 0, status: 'queued', source: 'web',
        message: '', created_at: '2026-09-26T10:01:00+08:00', record_ids: [3], position: 1, eta_sec: 15, stoppable: true,
      },
      {
        id: 14, kind: 'confirm', title: '批量确认入库 6 条', priority: 0, status: 'queued', source: 'web', message: '',
        created_at: '2026-09-26T10:01:30+08:00', record_ids: [4, 5, 6, 7, 8, 9], position: 2, eta_sec: 105, stoppable: true,
      },
      {
        id: 11, kind: 'background', title: '定时整理+增量', priority: 1, status: 'success', source: 'auto', message: '完成',
        created_at: '2026-09-26T09:50:00+08:00', started_at: '2026-09-26T09:50:00+08:00', finished_at: '2026-09-26T09:51:12+08:00',
      },
    ],
  },
  '/sync/cron-preview': { next: ['09-25 04:00', '09-26 04:00', '09-27 04:00'] },
  '/sync/capabilities': { fast_available: true, reason: '' },
  '/sync/orphans': {
    enabled: true,
    total: 26,
    ledger_total: 41_209,
    ratio: 0.0006,
    sample_limit: 50,
    sample: [
      { rel_path: '电影/科幻电影/星际穿越 (2014)/星际穿越 (2014) - 2160p.strm', kind: 'video', size: 96, marked_at: '09-23 04:12' },
      { rel_path: '电影/科幻电影/星际穿越 (2014)/poster.jpg', kind: 'asset', size: 412_000, marked_at: '09-23 04:12' },
      { rel_path: '剧集/国产剧集/漫长的季节 (2023)/Season 01/漫长的季节 S01E03.strm', kind: 'video', size: 102, marked_at: '09-23 04:12' },
    ],
  },
  '/sync/incr-status': {
    life_gate: { ok: true, message: '生活事件已开启', checked_at: '09-24 09:10' },
    endpoint: '主通道（life_list）',
    cursor: {},
    last_round: {
      at: '09-24 09:41:30',
      summary: {
        round: 1873, events_total: 4, events_fresh: 4, events_pending: 0, relevant: 2, structural: 0, deleted: 0,
        moved: 1, dirs: 1, videos: 2, strm_created: 2, strm_existing: 0, assets_total: 3, assets_downloaded: 3,
        assets_skipped: 0, assets_failed: 0, ignored: 2, elapsed: '1.8s', dirs_shallow: 1, dirs_deep: 0,
        list_calls: 2, consumed: true, not_consumed: '',
      },
    },
    pending_events: 0,
    path_cache: 312,
    interval_sec: 30,
    task_lock: { busy: true, holder: 'full', describe: '全量同步（已运行 84 秒）', running_sec: 84, waiting: ['自动整理'], waited_sec: 12 },
    stall: { rounds: 0, reason: '' },
  },
  '/sync/deep-delete/records': {
    data: [
      { id: 3, status: 'done', title: '星际穿越 (2014)', reason: 'emby_webhook', video_cnt: 1, asset_cnt: 4, created_at: '09-23 21:04', message: '' },
      { id: 2, status: 'rejected', title: '漫长的季节', reason: 'emby_webhook', video_cnt: 0, asset_cnt: 0, created_at: '09-22 18:40', message: '剧/季目录缺少台账布局证据，已拦截' },
    ],
    total: 2, page: 1, size: 20,
  },

  '/organize/records': { data: records, total: 86, page: 1, size: 20 },
  '/organize/records/stats': {
    data: { all: 86, awaiting: 2, problem: 7, success: 61, exists: 16, unrecognized: 4, failed: 3 },
  },
  '/tmdb/search': {
    data: [
      { id: 693134, media_type: 'movie', title: '沙丘 2', year: '2024', vote: 8.2, poster: '', overview: '保罗·厄崔迪与契妮和弗雷曼人联手，踏上向毁灭他家族的阴谋者复仇的战争之路。' },
      { id: 438631, media_type: 'movie', title: '沙丘', year: '2021', vote: 7.8, poster: '', overview: '天赋异禀的少年保罗·厄崔迪必须前往宇宙中最危险的星球。' },
      { id: 90228, media_type: 'tv', title: '沙丘：预言', year: '2024', vote: 7.1, poster: '', overview: '' },
    ],
  },
  '/auth/status': { initialized: true },
  '/auth/login': { token: 'mock-token', username: 'demo' },
  '/version': { version: '2.4.1-preview' },
  '/dashboard': dashboard,
}

export function installMockApi() {
  const real = window.fetch.bind(window)
  window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
    const [path, query = ''] = url.replace(/^.*\/api/, '').split('?')
    if (path in ROUTES) {
      // 留一点延迟，好让骨架屏/加载态在演示里真的出现
      await new Promise((r) => setTimeout(r, 180))
      const route = ROUTES[path]
      const body = typeof route === 'function' ? route(new URLSearchParams(query)) : route
      return new Response(JSON.stringify(body), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    return real(input as RequestInfo, init)
  }
  console.info('[mock] 假数据模式已启用，接口不会真正发出')
}
