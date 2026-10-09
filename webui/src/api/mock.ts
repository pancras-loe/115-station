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

/** Emby 的最新入库：横幅要用的简介 / 类型 / 评分都带上，图片 id 从 1001 起（预览时由本地图片桩返回） */
const EMBY_RECENT: [string, string, string, string, number, string[], string, boolean, boolean][] = [
  ['沙丘：第二部', '2024', 'Movie', '保罗·厄崔迪与弗雷曼人联手，踏上向毁灭他家族的阴谋者复仇的征程。在一生挚爱与已知宇宙命运之间，他必须做出抉择，阻止只有他能预见的可怕未来。', 8.2, ['科幻', '冒险'], 'PG-13', true, true],
  ['三体', '2023', 'Series', '纳米物理学家汪淼卷入一连串科学家离奇死亡的调查，一个神秘的倒计时出现在他眼前，把他引向一场跨越四百年的文明对决。', 7.9, ['剧情', '科幻', '悬疑'], 'TV-14', true, false],
  ['奥本海默', '2023', 'Movie', '二战期间，物理学家罗伯特·奥本海默受命领导曼哈顿计划，在新墨西哥州的沙漠里造出了改变世界的武器。', 8.1, ['剧情', '历史'], 'R', true, true],
  ['最后生还者', '2023', 'Series', '真菌疫情爆发二十年后，走私者乔尔受托护送少女艾莉穿越满目疮痍的美国，一场本该简单的任务变成了残酷而心碎的旅程。', 8.6, ['剧情', '动作'], 'TV-MA', true, false],
  ['蜘蛛侠：纵横宇宙', '2023', 'Movie', '迈尔斯·莫拉莱斯穿越多元宇宙，遇见了一群守护多元宇宙存在的蜘蛛侠，却与他们在如何应对新威胁上产生了分歧。', 8.4, ['动画', '动作'], 'PG', false, false],
  ['漫长的季节', '2023', 'Series', '东北小城桦林，出租车司机王响在一桩碎尸案里发现了儿子死亡的线索，一段横跨十八年的往事被慢慢揭开。', 9.0, ['剧情', '犯罪'], '', true, false],
  ['疾速追杀 4', '2023', 'Movie', '约翰·威克找到了击败高桌会的方法，但在赢得自由之前，他必须面对一个在全球拥有强大盟友的新敌人。', 7.7, ['动作', '惊悚'], 'R', true, false],
  ['铃芽之旅', '2022', 'Movie', '少女铃芽在九州小镇遇见了寻找「门」的青年草太，两人踏上关闭灾难之门的旅程，从九州一路北上。', 7.6, ['动画', '奇幻'], 'PG', true, false],
]

const dashboard: Dashboard = {
  emby: {
    counts: { movies: 619, series: 212, episodes: 9_841 },
    recent: EMBY_RECENT.map(([name, year, type, overview, rating, genres, official, backdrop, logo], i) => ({
      id: String(1001 + i),
      name,
      year,
      type,
      overview,
      rating,
      genres,
      official_rating: official,
      created: new Date(Date.now() - (i * 7 + 2) * 3_600_000).toISOString(),
      has_backdrop: backdrop,
      has_logo: logo,
    })),
    libraries: [
      ['电影', 'movies', '电影'],
      ['剧集', 'tvshows', '剧集'],
      ['动漫', 'tvshows', '剧集'],
      ['纪录片', 'movies', '电影'],
    ].map(([name, type, label], i) => ({
      name,
      count: [619, 164, 48, 43][i],
      type,
      type_label: label,
      collage: [0, 1, 2, 3].map((k) => `Items/${1001 + ((i * 3 + k) % 8)}/Images/Primary`),
    })),
  },
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
    source: 'emby',
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
  job_id: 11,
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

/** 任务历史（/tasks/history）：整理 / 重新整理 / 全量各一条，覆盖成功与失败 */
const HISTORY = [
  {
    id: 11, kind: 'organize', title: '定时整理', priority: 1, status: 'success', source: 'cron',
    message: '整理 5 项：成功 3，已存在 1，待确认 1', result: { success: 3, exists: 1, failed: 0, awaiting: 1 },
    created_at: '2026-09-26T09:50:00+08:00', started_at: '2026-09-26T09:50:00+08:00', finished_at: '2026-09-26T09:51:12+08:00',
  },
  {
    id: 10, kind: 'redo', title: '重新整理《漫长的季节》→ 漫长的季节 (2023)', priority: 0, status: 'failed', source: 'web',
    message: '剧集两集算出同一个新名字：E01.mkv 与 Season 2/E01.mkv，请加替换规则', record_ids: [2],
    created_at: '2026-09-26T09:30:00+08:00', started_at: '2026-09-26T09:30:01+08:00', finished_at: '2026-09-26T09:30:09+08:00',
  },
  {
    id: 9, kind: 'full', title: '全量同步', priority: 0, status: 'success', source: 'wecom', message: '新增 STRM 42 个',
    result: { mode_used: 'fast', scan_complete: true, orphans: 3 },
    created_at: '2026-09-26T04:00:00+08:00', started_at: '2026-09-26T04:00:01+08:00', finished_at: '2026-09-26T04:12:40+08:00',
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
  monitor: { enabled: false },
  'org-basic': {
    manual_confirm: true,
    pending: '301',
    pending_path: '/StrmStation/待整理',
    existing: '303',
    existing_path: '/StrmStation/已存在',
    redundant: '',
    redundant_path: '',
  },
  share: { folder: '302', folder_path: '/StrmStation/转存' },
}

/** 网盘文件页（/files/115?cid=…）：一棵小目录树 */
const d = (id: string, name: string, root?: string) => ({ id, name, is_dir: true, ...(root ? { root } : {}) })
const f = (id: string, name: string, size: number) => ({ id, name, is_dir: false, size, pickcode: 'pc' + id })
const FILE_TREE: Record<string, unknown[]> = {
  '0': [d('2894561234', '媒体库', 'library'), d('300', 'StrmStation'), d('400', '我的资源'), f('9001', '说明.txt', 1_200)],
  '300': [d('301', '待整理', 'pending'), d('302', '转存', 'share'), d('303', '已存在', 'existing'), d('304', '冗余', 'redundant')],
  '301': [
    d('3011', '沙丘2.Dune.Part.Two.2024.2160p.WEB-DL'),
    d('3012', 'The.Last.of.Us.S01.1080p'),
    f('3013', '奥本海默.Oppenheimer.2023.1080p.BluRay.x264.mkv', 12_884_901_888),
    f('3014', '奥本海默.Oppenheimer.2023.1080p.BluRay.x264.chs.ass', 84_000),
  ],
  '2894561234': [d('201', '电影'), d('202', '剧集')],
  '201': [d('2011', '科幻电影'), d('2012', '动画电影')],
  '2011': [d('20111', '沙丘 (2021) [tmdb=438631]'), d('20112', '星际穿越 (2014) [tmdb=157336]')],
}
const FILE_ROOTS = { '2894561234': 'library', '301': 'pending', '302': 'share', '303': 'existing', '304': 'redundant' }

type Route = unknown | ((q: URLSearchParams) => unknown)


// ---- 影视转存 / 资源订阅（2026-10-08 合并页预览用） ----
const SUBS = (
  [
    [1, 'tv', '凡人修仙传', '2020', 'active', 152, 160, 8, 2, false],
    [2, 'tv', '漫长的季节', '2023', 'done', 12, 12, 0, 0, false],
    [3, 'movie', '沙丘：第二部', '2024', 'active', 0, 1, 1, 0, true],
    [4, 'tv', '三体', '2023', 'stalled', 20, 30, 10, 0, false],
    [5, 'tv', '繁花', '2023', 'paused', 18, 30, 12, 0, false],
    [6, 'tv', '最后生还者', '2023', 'active', 9, 9, 0, 0, false],
    [7, 'movie', '奥本海默', '2023', 'done', 1, 1, 0, 0, false],
  ] as const
).map(([id, type, title, year, state, have, total, missing, inflight, running]) => ({
  id, tmdb_id: 90000 + id, media_type: type, title, year, poster_path: '', state, have, total, missing, inflight, running,
  scope: type === 'tv' ? 'all' : '', season: 0, ep_start: 0, ep_end: 0, specials: false, follow: type === 'tv' ? 'missing' : '',
  sources: [], rank_limit: 0, include: '', exclude: '', offline_mode: '', cond: null, last_result: '找到 2 条资源，提交 1 条',
  last_check_at: new Date(Date.now() - 3_600_000).toISOString(), next_check_at: new Date(Date.now() + 3_600_000).toISOString(),
  empty_rounds: 0, created_at: new Date(Date.now() - 86_400_000 * 9).toISOString(),
}))
const DISCOVER = TITLES.map(([title, year, cat], i) => ({
  id: i === 0 ? 693134 : 90001 + i, media_type: cat.endsWith('剧集') ? 'tv' : 'movie', title, year, vote: 7 + (i % 3) * 0.6, poster: '',
  overview: '演示条目的简介。',
}))
const subRoutes: Record<string, unknown> = {
  '/subscriptions': { data: SUBS },
  '/subscribe/config': {
    re0_spent_today: 5,
    data: {
      enabled: true, air_delay_hours: 6, movie_wait: 'digital', max_subs_per_round: 10, fresh_interval_min: 60, gap_interval_hours: 12, max_eps_per_sub: 50, max_res_per_sub: 50, try_cooldown_sec: 5, max_snap_dirs: 40, share_preview_max: 8,
      re0_unlock_max: 10, re0_daily_budget: 30, exclude_default: '预告,花絮', notify: 'created,ingested,stalled',
      offline_mode: 'pack', offline_wait_hours: 24, offline_monthly: 30, offline_reserve: 10,
      cond: { pix: '2160p,1080p', type: '', effect: '', video: '', audio: '', team: '', zh: true, min_gb: 0, max_gb: 0 },
    },
  },
  '/tmdb/discover': (q: URLSearchParams) =>
    q.get('list')
      ? { data: DISCOVER, has_more: false }
      : { lists: [{ key: 'trending', label: '本周趋势' }, { key: 'movie_popular', label: '热门电影' }, { key: 'tv_popular', label: '热门剧集' }] },
  '/guanying/config': { base_url: 'https://www.gyg.si', username: 'demo', logged_in: true },
  '/guanying/check': { logged_in: true },
  '/pansou/config': { base_url: 'https://pansou.app' },
  '/mukaku/config': { base_url: 'https://www.mukaku.com', has_token: false },
  '/re0/config': { base_url: 'https://re0.me', client_id: 'demo', client_secret: '••••', authorized: true, authorized_as: 'demo' },
  '/re0/check': { app_ok: true, app_name: 'StrmStation', authorized: true, user: 'demo', level: 'V3', points: 128 },
  '/re0/checkin': { enabled: true, done_today: true, hour: 8, last_result: '签到成功 +3 积分' },
}
for (const s of SUBS) {
  subRoutes[`/subscriptions/${s.id}`] = {
    data: s,
    grid:
      s.media_type === 'tv'
        ? [{ season: 1, eps: Array.from({ length: Math.min(s.total, 24) }, (_, i) => ({ e: i + 1, state: i < s.total - s.missing ? 'have' : i < s.total - s.missing + s.inflight ? 'inflight' : 'missing' })) }]
        : undefined,
    attempts: [
      { id: 1, source: 'pansou', kind: 'share115', title: `${s.title} 2160p 内封中字`, url: 'https://115cdn.com/s/demo', wrapper: '', episodes: ['S01E09'], status: 'ingested', reason: '', points: 0, link_id: 0, records: 1, created_at: new Date(Date.now() - 86_400_000).toISOString() },
    ],
  }
}

const ROUTES: Record<string, Route> = {
  ...(subRoutes as Record<string, Route>),
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
  // 按 kind 筛（Strm 管理页的状态总览只要最近一次全量），其余筛选条件演示里不区分
  '/tasks/history': (q: URLSearchParams) => {
    const kind = q.get('kind')
    const data = HISTORY.filter((j) => !kind || j.kind === kind)
    return { total: data.length, counts: { all: data.length, success: 2, failed: 1, canceled: 0, interrupted: 0 }, data }
  },
  '/tasks/11': {
    data: {
      id: 11, kind: 'organize', title: '定时整理', priority: 1, status: 'success', source: 'cron',
      message: '整理 5 项：成功 3，已存在 1，待确认 1', result: { success: 3, exists: 1, failed: 0, awaiting: 1 },
      created_at: '2026-09-26T09:50:00+08:00', started_at: '2026-09-26T09:50:00+08:00', finished_at: '2026-09-26T09:51:12+08:00',
      progress: { phase: '落盘', done: 5, total: 5, label: '沙丘 2 (2024)' },
      params_summary: ['定时触发'],
      record_total: 2,
      records: [
        { id: 1, source: 'Dune.Part.Two.2024.2160p.mkv', status: 'awaiting', title: '沙丘 2', year: '2024', media_type: 'movie', tmdb_id: 693134 },
        { id: 2, source: 'Long.Season.S01/', status: 'success', title: '漫长的季节', year: '2023', media_type: 'tv', tmdb_id: 1 },
      ],
    },
  },
  '/sync/cron-preview': { next: ['10-01 04:00', '10-02 04:00', '10-03 04:00'] },
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
      at: '2026-09-24T09:41:30+08:00',
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
      { id: 3, status: 'done', title: '星际穿越 (2014)', reason: 'emby_webhook', video_cnt: 1, asset_cnt: 4, created_at: '2026-09-23 21:04:12', message: '' },
      { id: 2, status: 'rejected', title: '漫长的季节', reason: 'emby_webhook', video_cnt: 0, asset_cnt: 0, created_at: '2026-09-22 18:40:05', message: '剧/季目录缺少台账布局证据，已拦截' },
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
  '/transfer/sources': {
    sources: [
      { key: 'gy', label: '观影', enabled: true, reason: '' },
      { key: 'pansou', label: '盘搜', enabled: true, reason: '' },
      { key: 'tg', label: 'TG 频道', enabled: true, reason: '' },
      { key: 'mukaku', label: '不太灵', enabled: true, reason: '未设置 VIP Token（资源仅 VIP 可见）' },
      { key: 're0', label: 'RE0', enabled: true, reason: '' },
    ],
    folder: '3100000000000000001',
    folder_path: '/StrmStation/转存',
  },
  '/transfer/owned': { data: { 'movie:438631': { title: '沙丘', category: '电影/科幻电影', at: 1760000000 } } },
  '/transfer/resources/gy': {
    items: [
      { source: 'gy', kind: 'magnet', action: 'offline', title: 'Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.DV.HDR.HEVC.TrueHD.7.1.Atmos-FraMeSToR', ref: '/bt/1', size: '78.21G', size_bytes: 83977000000, seeds: 12, time: '3 天前', time_unix: 1759500000, tags: { pix: '2160p', type: 'REMUX', effect: 'DV.HDR', video: 'HEVC', audio: 'TrueHD.7.1' }, relevant: true, rank: 0 },
      { source: 'gy', kind: 'magnet', action: 'offline', title: 'Dune.Part.Two.2024.1080p.WEB-DL.DDP5.1.H264 中字', ref: '/bt/2', size: '8.2G', size_bytes: 8804000000, seeds: 40, time: '2 周前', time_unix: 1758500000, tags: { pix: '1080p', type: 'WEB-DL', video: 'H264', zh: true }, relevant: true, rank: 2 },
      { source: 'gy', kind: 'magnet', action: 'offline', title: 'Dune.1984.1080p.BluRay.x264', ref: '/bt/3', size: '12G', size_bytes: 12884901888, seeds: 3, tags: { pix: '1080p', type: 'BluRay', video: 'x264' }, relevant: false, rank: 2 },
    ],
  },
  '/transfer/resources/pansou': {
    items: [
      { source: 'pansou', kind: 'share115', action: 'transfer', title: '沙丘2 (2024) 4K 杜比视界 内封简繁 [58.3G]', url: 'https://115cdn.com/s/swabc', code: 'x1y2', size: '58.3G', size_bytes: 62598000000, time: '2026-09-12', time_unix: 1757600000, tags: { pix: '2160p', effect: 'DV', zh: true }, relevant: true, rank: 0, submitted_at: 1759000000 },
      { source: 'pansou', kind: 'pan', pan: 'baidu', action: 'open', title: '沙丘2 1080P 国英双语', url: 'https://pan.baidu.com/s/1abc', code: 'ab12', time: '2026-08-30', time_unix: 1756500000, tags: { pix: '1080p', zh: true }, relevant: true, rank: 2 },
      { source: 'pansou', kind: 'share115', action: 'transfer', title: '流浪地球2 4K 合集', url: 'https://115.com/s/other', tags: { pix: '2160p' }, relevant: false, rank: 1 },
    ],
  },
  '/transfer/resources/tg': {
    items: [
      { source: 'tg', kind: 'share115', action: 'transfer', title: '沙丘2 (2024) 2160p WEB-DL DDP5.1 Atmos', via: '@quanquan_115', url: 'https://115cdn.com/s/tg001', code: 'q9w8', size: '21.6GB', size_bytes: 23192823398, time: '2026-09-20 21:14', time_unix: 1758374040, tags: { pix: '2160p', type: 'WEB-DL', audio: 'DDP5.1' }, relevant: true, rank: 1 },
    ],
    note: '1 个频道没抓到：share_other',
  },
  '/tgsearch/config': { channels: 'quanquan_115\nshare_other' },
  '/transfer/resources/re0': {
    items: [
      { source: 're0', kind: 'share115', action: 'unlock', title: '沙丘2 2160p WEB-DL', ref: 'slug-1', size: '22.4GB', size_bytes: 24051816857, points: 5, owned: false, tags: { pix: '2160p', type: 'WEB-DL' }, relevant: true, rank: 1 },
    ],
  },
  '/files/115': (q: URLSearchParams) => {
    const cid = q.get('cid') ?? '0'
    // 每行能做什么本来由后端按面包屑算（rowActionsOf），这里按目录粗略写死
    const lib = ['2894561234', '201', '2011'].includes(cid)
    const data = (FILE_TREE[cid] ?? []).map((raw) => {
      const it = raw as { id: string; name: string; is_dir: boolean; root?: string }
      const video = !it.is_dir && /\.(mkv|mp4)$/i.test(it.name)
      if (it.root) return { ...it, block: '整理工作区目录不能整理或移动' }
      if (lib) {
        if (cid !== '2011') return { ...it, block: '媒体库里只有分类目录下的片目目录能整理和移动' }
        return { ...it, organize: true, move: true, title: true, title_key: `媒体库/电影/科幻电影/${it.name}`, title_rel: `电影/科幻电影/${it.name}` }
      }
      if (!it.is_dir && !video) return { ...it, move: true, organize_block: '不是视频文件' }
      return { ...it, video, organize: true, move: true }
    })
    return { cid, data, roots: FILE_ROOTS }
  },
  '/local/titles': (q: URLSearchParams) => {
    const all = TITLES.map(([title, year, cat], i) => {
      const tv = cat.endsWith('剧集')
      const status = (['ok', 'ok', 'partial', 'miss'] as const)[i % 4]
      return {
        key: `影视/${tv ? '剧集' : '电影'}/${cat}/${title} (${year})`,
        title, year, tmdb_id: i % 5 === 4 ? 0 : 1000 + i, media_type: tv ? 'tv' : 'movie', category: cat,
        videos: tv ? 8 + i : 1, has_nfo: status !== 'miss', has_poster: status === 'ok', status,
        last_at: new Date(Date.now() - i * 3_600_000).toISOString(),
      }
    })
    const type = q.get('type') ?? ''
    const status = q.get('status') ?? ''
    const items = all.filter((t) => (!type || t.media_type === type) && (!status || t.status === status))
    const inType = all.filter((t) => !type || t.media_type === type)
    return {
      configured: true, root: '/strm', items, total: items.length, missing: 0,
      stats: {
        all: inType.length, ok: inType.filter((t) => t.status === 'ok').length,
        partial: inType.filter((t) => t.status === 'partial').length, miss: inType.filter((t) => t.status === 'miss').length,
        movie: all.filter((t) => t.media_type === 'movie').length, tv: all.filter((t) => t.media_type === 'tv').length,
      },
    }
  },
  '/scrape/config': { cfg: { write_nfo: true, write_images: true, force: false, auto_after_organize: true, auto_after_sync: false } },
  '/storage': {
    data: [{ id: 1, name: '115主号', type: '115', cookie_path: '/config/115-cookies.txt', device: 'web', interval: 3, openapi_enabled: false, app_id: '' }],
  },
  '/storage/check': {
    valid: true,
    channel: 'Cookie',
    username: '影迷小王',
    capacity: '7.50 TB / 10.00 TB',
    user_id: 38_120_456,
    vip: 1,
    vip_expire: Math.floor(Date.now() / 1000) + 86_400 * 212,
    used_size: 8_243_000_000_000,
    total_size: 11_000_000_000_000,
    devices: [
      { name: '网页端', device: 'web', ip: '203.0.113.24', city: '上海', utime: Math.floor(Date.now() / 1000) - 120, is_current: true },
      { name: 'iPhone 16 Pro', device: 'ios', ip: '198.51.100.7', city: '上海', utime: Math.floor(Date.now() / 1000) - 86_400 * 2 },
      { name: 'Windows 客户端', device: 'windows', ip: '192.0.2.61', city: '杭州', utime: Math.floor(Date.now() / 1000) - 86_400 * 9 },
    ],
  },
  '/auth/status': { initialized: true },
  '/auth/login': { token: 'mock-token', username: 'demo' },
  '/auth/otp': { enabled: false, token_expire: 11520 },
  '/version': { version: 'v1.2.0-3-g9596437', sha: '9596437' },
  '/system/update/check': () => ROUTES['/system/update'],
  '/system/update': {
    current: 'v1.2.0-3-g9596437',
    latest: 'v1.3.0',
    has_update: true,
    comparable: true,
    notes: ['## What’s Changed', '* 整理记录支持批量重新整理', '* 修复多版本电影海报'].join('\n'),
    url: 'https://github.com/pancras-loe/115-station/releases/tag/v1.3.0',
    published_at: new Date(Date.now() - 86400_000).toISOString(),
    checked_at: new Date().toISOString(),
    enabled: true,
  },
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
