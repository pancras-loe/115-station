<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NConfigProvider, darkTheme, zhCN, dateZhCN } from 'naive-ui'
import { TooltipProvider } from 'reka-ui'
import { useThemeStore } from '@/stores/theme'
import { useAuthStore } from '@/stores/auth'
import { buildNaiveOverrides } from '@/theme/naive'
import HToaster from '@/components/hero/HToaster.vue'
import HDialogHost from '@/components/hero/HDialogHost.vue'

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
    <!-- HeroUI 组件（components/hero/）的提示气泡共用一个延迟计时器 -->
    <TooltipProvider :delay-duration="400">
      <RouterView />
    </TooltipProvider>
    <!-- 全站轻提示与确认框，由 useFeedback() 驱动，store / api 层也能调 -->
    <HToaster />
    <HDialogHost />
  </NConfigProvider>
</template>
