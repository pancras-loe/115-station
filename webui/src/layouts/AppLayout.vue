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
  if (['organize', 'transfer', 'redo', 'confirm', 'ignore', 'deepdel'].includes(j.kind)) void refreshRecordStats()
})
onUnmounted(offFinished)
const version = ref('')

onMounted(async () => {
  try {
    const v = await systemApi.version()
    version.value = v.version ? `v${v.version}` : ''
  } catch {
    // 版本号拿不到不影响使用，静默降级
  }
})

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
    <AppSidebar class="sidebar-desktop" :version="version" />

    <!-- 移动端抽屉 -->
    <HDrawer v-model:show="drawerOpen" width="252px" title="导航菜单">
      <AppSidebar :version="version" @navigate="drawerOpen = false" />
    </HDrawer>

    <div class="main">
      <header class="topbar">
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

.sidebar-desktop {
  position: sticky;
  top: 0;
  height: 100vh;
  flex-shrink: 0;
}

.main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
}

/* 顶栏与页面同底色、不画分隔线；滚动时靠半透明 + 模糊和内容区分开 */
.topbar {
  position: sticky;
  top: 0;
  z-index: 10;
  display: flex;
  align-items: center;
  gap: 12px;
  min-height: var(--topbar-h);
  padding: 12px 28px 8px;
  background: color-mix(in oklab, var(--background) 80%, transparent);
  backdrop-filter: saturate(180%) blur(16px);
  -webkit-backdrop-filter: saturate(180%) blur(16px);
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
  font-weight: 600;
  letter-spacing: -0.02em;
  color: var(--foreground);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.desc {
  margin: 2px 0 0;
  font-size: 13px;
  color: var(--muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
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

.content {
  flex: 1;
  padding: 12px 28px 28px;
  min-width: 0;
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
    padding: 8px 20px 24px;
  }
  .topbar {
    padding: 10px 20px 6px;
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
    min-height: 52px;
    padding: calc(8px + env(safe-area-inset-top)) 16px 6px;
  }
  .title {
    font-size: 18px;
  }
  .content {
    padding: 4px 16px calc(var(--tabbar-h) + env(safe-area-inset-bottom) + 20px);
  }
}
</style>
