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
        meta: { title: '总览面板', desc: '容量 / STRM / 整理 / 任务总览', icon: 'dashboard' },
      },
      {
        path: 'tasks',
        name: 'tasks',
        component: () => import('@/pages/TasksPage.vue'),
        meta: { title: '任务中心', desc: '任务队列与历史 / 整理记录', icon: 'tasks' },
      },
      {
        path: 'subscriptions',
        name: 'tgsub',
        // 订阅管理暂未开放，保留旧地址以兼容收藏，统一回到首页。
        redirect: { name: 'dashboard' },
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
        meta: { title: 'Strm 管理', desc: 'STRM 配置 / 全量同步 / 增量同步', icon: 'sync' },
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
        path: 'upload-download',
        name: 'upload-download',
        component: () => import('@/pages/UploadDownloadPage.vue'),
        meta: { title: '上传下载', desc: '监控上传 / 转存下载', icon: 'transfer' },
      },
      {
        path: 'media-transfer',
        name: 'media-transfer',
        component: () => import('@/pages/MediaTransferPage.vue'),
        meta: { title: '影视转存', desc: '观影种子搜索 / 115 离线下载', icon: 'download' },
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
        meta: { title: '扩展功能', desc: '签到 / TG 搜索 / 封面生成等插件', icon: 'plugin' },
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
