<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NButton, NDrawer, NDropdown, NTooltip } from 'naive-ui'
import { h } from 'vue'
import { LogOut, Menu, ScrollText, UserRound } from '@lucide/vue'
import AppSidebar from './AppSidebar.vue'
import ThemeToggle from '@/components/ThemeToggle.vue'
import { useAuthStore } from '@/stores/auth'
import { systemApi } from '@/api'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const drawerOpen = ref(false)
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

const accountOptions = [{ label: '退出登录', key: 'logout', icon: () => h(LogOut, { size: 15 }) }]

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
    <NDrawer v-model:show="drawerOpen" :width="236" placement="left">
      <AppSidebar :version="version" @navigate="drawerOpen = false" />
    </NDrawer>

    <div class="main">
      <header class="topbar">
        <NButton class="menu-btn" quaternary circle aria-label="菜单" @click="drawerOpen = true">
          <Menu :size="19" />
        </NButton>

        <div class="titles">
          <h1 class="title">{{ title }}</h1>
          <p v-if="desc" class="desc">{{ desc }}</p>
        </div>

        <div class="actions">
          <NTooltip>
            <template #trigger>
              <NButton quaternary circle aria-label="实时日志" @click="router.push({ name: 'logs' })">
                <ScrollText :size="18" />
              </NButton>
            </template>
            实时日志
          </NTooltip>

          <ThemeToggle />

          <NDropdown trigger="click" :options="accountOptions" @select="onAccount">
            <NButton quaternary class="account-btn">
              <template #icon><UserRound :size="17" /></template>
              <span class="account-name">{{ auth.username || '账号' }}</span>
            </NButton>
          </NDropdown>
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
  </div>
</template>

<style scoped>
.layout {
  display: flex;
  height: 100%;
  background: var(--c-bg-base);
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

.topbar {
  position: sticky;
  top: 0;
  z-index: 10;
  display: flex;
  align-items: center;
  gap: 12px;
  min-height: var(--topbar-h);
  padding: 10px 20px;
  background: color-mix(in srgb, var(--c-bg-base) 82%, transparent);
  backdrop-filter: saturate(180%) blur(12px);
  border-bottom: 1px solid var(--c-border);
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
  font-size: 17px;
  font-weight: 600;
  letter-spacing: -0.01em;
  color: var(--c-text-1);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.desc {
  margin: 2px 0 0;
  font-size: 12.5px;
  color: var(--c-text-3);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.actions {
  display: flex;
  align-items: center;
  gap: 4px;
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
  padding: 20px;
  min-width: 0;
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
    padding: 16px;
  }
}
</style>
