<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import {
  NConfigProvider,
  NDialogProvider,
  NLoadingBarProvider,
  NMessageProvider,
  NNotificationProvider,
  darkTheme,
  zhCN,
  dateZhCN,
} from 'naive-ui'
import { TooltipProvider } from 'reka-ui'
import { useThemeStore } from '@/stores/theme'
import { useAuthStore } from '@/stores/auth'
import { buildNaiveOverrides } from '@/theme/naive'
import FeedbackBridge from '@/components/FeedbackBridge.vue'

const theme = useThemeStore()
const auth = useAuthStore()
auth.bootstrap()

const overrides = ref(buildNaiveOverrides())
// CSS 变量换完一帧后再读，否则拿到的还是切换前的旧色值
watch(
  () => theme.isDark,
  () => requestAnimationFrame(() => (overrides.value = buildNaiveOverrides())),
)

const naiveTheme = computed(() => (theme.isDark ? darkTheme : null))
</script>

<template>
  <NConfigProvider
    :theme="naiveTheme"
    :theme-overrides="overrides"
    :locale="zhCN"
    :date-locale="dateZhCN"
    inline-theme-disabled
  >
    <NLoadingBarProvider>
      <NDialogProvider>
        <NNotificationProvider>
          <NMessageProvider :max="4" placement="top">
            <FeedbackBridge />
            <!-- HeroUI 组件（components/hero/）的提示气泡共用一个延迟计时器 -->
            <TooltipProvider :delay-duration="400">
              <RouterView />
            </TooltipProvider>
          </NMessageProvider>
        </NNotificationProvider>
      </NDialogProvider>
    </NLoadingBarProvider>
  </NConfigProvider>
</template>
