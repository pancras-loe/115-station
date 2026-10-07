import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

/**
 * 路径沿用旧版前端（/sync、/plugins、/subscriptions …）。
 * 这些地址用户可能已经收藏，换成新的会直接 404 —— 不要「顺手」改成更规整的命名。
 */
const routes: RouteRecordRaw[] = [
  {
    path: '/login',
    name: 'login',
    component: () => import('@/pages/LoginPage.vue'),
    meta: { public: true, title: '登录' },
  },
  {
    path: '/',
    component: () => import('@/layouts/AppLayout.vue'),
    children: [
      {
        path: '',
        name: 'dashboard',
        component: () => import('@/pages/DashboardPage.vue'),
        meta: { title: '总览面板', desc: '最新入库 / 媒体库 / 容量与任务', icon: 'dashboard', immersive: true },
      },
      {
        path: 'tasks',
        name: 'tasks',
        component: () => import('@/pages/TasksPage.vue'),
        meta: { title: '任务中心', desc: '任务队列与历史 / 整理记录', icon: 'tasks' },
      },
      {
        // 旧版是 TG 关键词订阅（2026-10-07 删除），地址沿用给按 TMDB 条目的资源订阅
        path: 'subscriptions',
        name: 'subscriptions',
        component: () => import('@/pages/SubscriptionsPage.vue'),
        meta: { title: '资源订阅', desc: '按影片订阅：定时找缺的集，转存后自动整理入库', icon: 'subscribe' },
      },
      {
        path: 'accounts',
        name: 'accounts',
        component: () => import('@/pages/AccountsPage.vue'),
        meta: { title: '账号与媒体库', desc: '管理 115 账号与媒体库位置', icon: 'accounts' },
      },
      {
        path: 'sync',
        name: 'sync',
        component: () => import('@/pages/SyncPage.vue'),
        meta: { title: 'Strm 管理', desc: '同步状态 / STRM 配置 / 全量 · 增量 · 深度删除', icon: 'sync' },
      },
      {
        path: 'organize',
        name: 'organize',
        component: () => import('@/pages/OrganizePage.vue'),
        meta: { title: '自动整理', desc: '基础配置 / 识别规则 / 分类策略 / 洗版 / 重命名', icon: 'organize' },
        // 整理记录搬到了任务中心：旧收藏 /organize?tab=records[&status=…] 照样能打开
        beforeEnter: (to) =>
          to.query.tab === 'records' ? { name: 'tasks', query: to.query, replace: true } : true,
      },
      {
        path: 'files',
        name: 'files',
        component: () => import('@/pages/FilesPage.vue'),
        meta: { title: '网盘文件', desc: '浏览 115 网盘 / 手动整理与移动', icon: 'files' },
      },
      {
        path: 'local',
        name: 'local',
        component: () => import('@/pages/LocalFilesPage.vue'),
        meta: { title: '本地文件', desc: '本地媒体库的影片与剧集 / 手动刮削', icon: 'local' },
      },
      {
        path: 'scrape',
        name: 'scrape',
        component: () => import('@/pages/ScrapePage.vue'),
        meta: { title: '影视刮削', desc: 'NFO 与海报 / 媒体信息探测 / 演职人员补全', icon: 'scrape' },
      },
      {
        path: 'upload-download',
        name: 'upload-download',
        component: () => import('@/pages/UploadDownloadPage.vue'),
        meta: { title: '监控上传', desc: '把本地新产生的 NFO / 图片回传 115', icon: 'transfer' },
        // 「转存下载」页签已并进影视转存的「链接转存」：旧地址 ?tab=download 带过去
        beforeEnter: (to) => (to.query.tab === 'download' ? { name: 'media-transfer', query: { tab: 'link' } } : true),
      },
      {
        path: 'media-transfer',
        name: 'media-transfer',
        component: () => import('@/pages/MediaTransferPage.vue'),
        meta: { title: '影视转存', desc: '按影片聚合各资源站 / 链接转存与离线下载', icon: 'download' },
      },
      {
        path: 'settings',
        name: 'settings',
        component: () => import('@/pages/SettingsPage.vue'),
        meta: { title: '系统配置', desc: 'TMDB / 代理 / EMBY 配置', icon: 'settings' },
      },
      {
        path: 'message',
        name: 'message',
        component: () => import('@/pages/MessagePage.vue'),
        meta: { title: '消息配置', desc: '企业微信与 TG 机器人', icon: 'message' },
      },
      {
        path: 'plugins',
        name: 'plugins',
        component: () => import('@/pages/PluginsPage.vue'),
        meta: { title: '扩展功能', desc: '签到 / 媒体库封面等插件', icon: 'plugin' },
      },
      {
        path: 'logs',
        name: 'logs',
        component: () => import('@/pages/LogsPage.vue'),
        meta: { title: '实时日志', desc: '同步与整理操作的服务端与本地日志', icon: 'logs', hideInNav: true },
      },
    ],
  },
  // 后端 NoRoute 会把任意路径回退到 index.html，前端兜底重定向到首页
  { path: '/:pathMatch(.*)*', redirect: '/' },
]

export const router = createRouter({
  history: createWebHistory(),
  routes,
  scrollBehavior: () => ({ top: 0 }),
})

router.beforeEach((to) => {
  const auth = useAuthStore()
  if (to.meta.public) {
    return auth.isAuthed ? { name: 'dashboard' } : true
  }
  if (!auth.isAuthed) {
    // 带上来路，登录后跳回原目标（深链接刷新不丢失）
    return { name: 'login', query: to.fullPath === '/' ? {} : { redirect: to.fullPath } }
  }
  return true
})

router.afterEach((to) => {
  const t = to.meta.title as string | undefined
  document.title = t ? `${t} · StrmStation` : 'StrmStation'
})
