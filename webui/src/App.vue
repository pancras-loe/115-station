<script setup lang="ts">
import { watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { TooltipProvider } from 'reka-ui'
import { useThemeStore } from '@/stores/theme'
import { useAuthStore } from '@/stores/auth'
import HToaster from '@/components/hero/HToaster.vue'
import HDialogHost from '@/components/hero/HDialogHost.vue'

// 主题 store 负责给 <html> 切 .dark；HeroUI 的主题变量据此换成暗色，全站颜色都从那里取
useThemeStore()
const auth = useAuthStore()
auth.bootstrap()

// 路由守卫只在切页时才查登录态：令牌过期后 api 层清了登录态，人还停在原页面，
// 轮询一直报「登录已过期」却不跳。这里盯着登录态，一掉线就当场回登录页（带上来路）
const route = useRoute()
const router = useRouter()
watch(
  () => auth.isAuthed,
  (ok) => {
    if (ok || route.meta.public) return
    const back = route.fullPath
    router.replace({ name: 'login', query: back === '/' ? {} : { redirect: back } })
  },
)
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
