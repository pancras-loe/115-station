<script setup lang="ts">
import { TooltipProvider } from 'reka-ui'
import { useThemeStore } from '@/stores/theme'
import { useAuthStore } from '@/stores/auth'
import HToaster from '@/components/hero/HToaster.vue'
import HDialogHost from '@/components/hero/HDialogHost.vue'

// 主题 store 负责给 <html> 切 .dark；HeroUI 的主题变量据此换成暗色，全站颜色都从那里取
useThemeStore()
const auth = useAuthStore()
auth.bootstrap()
</script>

<template>
  <!-- HeroUI 组件（components/hero/）的提示气泡共用一个延迟计时器 -->
  <TooltipProvider :delay-duration="400">
    <RouterView />
  </TooltipProvider>
  <!-- 全站轻提示与确认框，由 useFeedback() 驱动，store / api 层也能调 -->
  <HToaster />
  <HDialogHost />
</template>
