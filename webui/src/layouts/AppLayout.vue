<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { LogOut, Menu, ScrollText, UserRound } from '@lucide/vue'
import AppSidebar from './AppSidebar.vue'
import MobileTabBar from './MobileTabBar.vue'
import HButton from '@/components/hero/HButton.vue'
import HTooltip from '@/components/hero/HTooltip.vue'
import HDrawer from '@/components/hero/HDrawer.vue'
import HDropdown from '@/components/hero/HDropdown.vue'
import ThemeToggle from '@/components/ThemeToggle.vue'
import TaskQueuePanel from '@/components/TaskQueuePanel.vue'
import { useAuthStore } from '@/stores/auth'
import { useQueueStore } from '@/stores/queue'
import { refreshRecordStats } from '@/stores/recordStats'
import { displayVersion, refreshUpdateStatus } from '@/stores/update'
import { systemApi } from '@/api'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const drawerOpen = ref(false)

// 侧栏 / 底栏「任务中心」上的待确认角标：进站拉一次，产生或处理整理记录的任务跑完再拉。
// 定时整理也是队列任务，所以后台识别出新的待确认条目同样能刷到
const queue = useQueueStore()
onMounted(refreshRecordStats)
const offFinished = queue.onFinished((j) => {
  if (['organize', 'orgpick', 'libredo', 'libepisode', 'filemove', 'transfer', 'redo', 'confirm', 'ignore', 'deepdel'].includes(j.kind)) void refreshRecordStats()
})
onUnmounted(offFinished)
const version = ref('')

onMounted(async () => {
  try {
    const v = await systemApi.version()
    version.value = displayVersion(v.version)
  } catch {
    // 版本号拿不到不影响使用，静默降级
  }
})
// 更新检测结果（后端定时查，这里只读缓存）：侧栏底部版本号旁的小点
onMounted(refreshUpdateStatus)

// 侧栏：默认收成图标栏（悬停浮出），用户点图钉可以固定展开
const PIN_KEY = 'ui.sidebar.pinned'
function readPinned() {
  try {
    return localStorage.getItem(PIN_KEY) === '1'
  } catch {
    return false
  }
}
const pinned = ref(readPinned())
function togglePin() {
  pinned.value = !pinned.value
  try {
    localStorage.setItem(PIN_KEY, pinned.value ? '1' : '0')
  } catch {
    // 无痕模式写不进去就只管这一次
  }
}

// 沉浸页（总览）：页面顶部的大图横幅一直铺到顶栏底下，顶栏在没滚动时透明、字变浅色。
// 滚过横幅上沿就恢复成普通的毛玻璃顶栏
const immersive = computed(() => route.meta.immersive === true)
const scrolled = ref(false)
function onScroll() {
  scrolled.value = window.scrollY > 24
}
onMounted(() => {
  onScroll()
  window.addEventListener('scroll', onScroll, { passive: true })
})
onUnmounted(() => window.removeEventListener('scroll', onScroll))
const overHero = computed(() => immersive.value && !scrolled.value)

const title = computed(() => (route.meta.title as string) ?? '')
const desc = computed(() => (route.meta.desc as string) ?? '')

// 移动端：切页后自动收起抽屉
watch(() => route.fullPath, () => (drawerOpen.value = false))

const accountOptions = [{ label: '退出登录', key: 'logout', icon: LogOut, danger: true }]

function onAccount(key: string) {
  if (key === 'logout') {
    auth.logout()
    router.push({ name: 'login' })
  }
}
</script>

<template>
  <div class="layout">
    <!-- 桌面端固定侧栏 -->
    <div class="sidebar-desktop" :class="{ 'is-pinned': pinned }">
      <AppSidebar :version="version" :docked="true" :pinned="pinned" @toggle-pin="togglePin" />
    </div>

    <!-- 移动端抽屉 -->
    <HDrawer v-model:show="drawerOpen" width="252px" title="导航菜单">
      <AppSidebar :version="version" @navigate="drawerOpen = false" />
    </HDrawer>

    <div class="main" :class="{ 'is-immersive': immersive }">
      <header class="topbar" :class="{ 'on-dark': overHero, 'is-over-hero': overHero }">
        <HButton class="menu-btn" variant="ghost" icon-only aria-label="菜单" @click="drawerOpen = true">
          <Menu :size="19" />
        </HButton>

        <div class="titles">
          <h1 class="title">{{ title }}</h1>
          <p v-if="desc" class="desc">{{ desc }}</p>
        </div>

        <div class="actions">
          <TaskQueuePanel />

          <HTooltip content="实时日志" side="bottom">
            <HButton variant="ghost" icon-only aria-label="实时日志" @click="router.push({ name: 'logs' })">
              <ScrollText :size="18" />
            </HButton>
          </HTooltip>

          <ThemeToggle />

          <HDropdown :options="accountOptions" @select="onAccount">
            <HButton variant="ghost" class="account-btn" :aria-label="auth.username || '账号'">
              <template #icon><UserRound :size="17" /></template>
              <span class="account-name">{{ auth.username || '账号' }}</span>
            </HButton>
          </HDropdown>
        </div>
      </header>

      <main class="content">
        <RouterView v-slot="{ Component }">
          <Transition name="page" mode="out-in">
            <component :is="Component" />
          </Transition>
        </RouterView>
      </main>
    </div>

    <!-- 手机底部导航（≤720px 显示），「更多」打开同一个侧栏抽屉 -->
    <MobileTabBar class="tabbar-mobile" @more="drawerOpen = true" />
  </div>
</template>

<style scoped>
.layout {
  display: flex;
  min-height: 100%;
  background: var(--background);
}

/* 占位列：未固定时只占 72px，悬停展开的侧栏是绝对定位浮在内容上的；固定后占位列展开到完整宽度（见 AppSidebar） */
.sidebar-desktop {
  position: sticky;
  top: 0;
  z-index: 30;
  width: 72px;
  height: 100vh;
  flex-shrink: 0;
  /* 和侧栏自己的宽度同一条曲线、同一个时长：固定 / 取消固定时两者一起走，内容区平滑让位 */
  transition: width 240ms cubic-bezier(0.2, 0.8, 0.2, 1);
}
.sidebar-desktop.is-pinned {
  width: var(--sidebar-w);
}

.main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
}

/* 顶栏固定高度（--topbar-real）：沉浸页要把横幅往上拉整整一个顶栏高，高度不能随内容浮动 */
.topbar {
  --topbar-real: 68px;
  position: sticky;
  top: 0;
  z-index: 10;
  display: flex;
  align-items: center;
  gap: 12px;
  height: var(--topbar-real);
  padding: 0 28px;
  box-sizing: border-box;
  background: color-mix(in oklab, var(--background) 78%, transparent);
  backdrop-filter: saturate(180%) blur(18px);
  -webkit-backdrop-filter: saturate(180%) blur(18px);
  border-bottom: 1px solid color-mix(in oklab, var(--separator) 70%, transparent);
  transition:
    background-color 240ms ease,
    border-color 240ms ease;
}
/* 沉浸页：顶栏不占位（负的下外边距），横幅从页面最顶上开始 */
.is-immersive .topbar {
  margin-bottom: calc(-1 * var(--topbar-real));
}
.topbar.is-over-hero {
  background: transparent;
  border-bottom-color: transparent;
  backdrop-filter: none;
  -webkit-backdrop-filter: none;
}

.menu-btn {
  display: none;
}

.titles {
  min-width: 0;
  flex: 1;
}
.title {
  margin: 0;
  font-size: 20px;
  font-weight: 650;
  line-height: 1.3;
  letter-spacing: -0.02em;
  color: var(--foreground);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.desc {
  margin: 1px 0 0;
  font-size: 12.5px;
  line-height: 1.5;
  color: var(--muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.is-over-hero .title {
  text-shadow: 0 1px 12px rgb(0 0 0 / 0.45);
}

.actions {
  display: flex;
  align-items: center;
  gap: 2px;
  flex-shrink: 0;
}
.account-name {
  max-width: 120px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* --content-px 让沉浸页的横幅能按同一个数值左右出血到边 */
.content {
  --content-px: 28px;
  flex: 1;
  padding: 20px var(--content-px) 36px;
  min-width: 0;
}
.is-immersive .content {
  padding-top: 0;
}

.tabbar-mobile {
  display: none;
}

/* 切页淡入：位移只有 4px，够表达「换了内容」又不会让人等动画 */
.page-enter-active,
.page-leave-active {
  transition: opacity 0.16s ease, transform 0.16s ease;
}
.page-enter-from {
  opacity: 0;
  transform: translateY(4px);
}
.page-leave-to {
  opacity: 0;
}

/* 平板：侧栏收进抽屉，左上角出菜单按钮 */
@media (max-width: 960px) {
  .sidebar-desktop {
    display: none;
  }
  .menu-btn {
    display: inline-flex;
  }
  .desc {
    display: none;
  }
  .account-name {
    display: none;
  }
  .content {
    --content-px: 20px;
    padding-top: 12px;
  }
  .topbar {
    --topbar-real: 60px;
    padding: 0 20px;
  }
}

/* 手机：导航交给底栏，顶栏只留标题和两个图标按钮 */
@media (max-width: 720px) {
  .menu-btn {
    display: none;
  }
  .tabbar-mobile {
    display: grid;
  }
  .topbar {
    --topbar-real: calc(52px + env(safe-area-inset-top));
    padding: env(safe-area-inset-top) 16px 0;
  }
  .title {
    font-size: 18px;
  }
  .content {
    --content-px: 16px;
    padding: 8px var(--content-px) calc(var(--tabbar-h) + env(safe-area-inset-bottom) + 20px);
  }
}
</style>
