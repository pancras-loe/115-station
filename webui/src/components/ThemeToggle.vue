<script setup lang="ts">
import { computed } from 'vue'
import { Moon, Sun, SunMoon } from '@lucide/vue'
import { useThemeStore } from '@/stores/theme'
import HButton from '@/components/hero/HButton.vue'
import HTooltip from '@/components/hero/HTooltip.vue'

const theme = useThemeStore()

const current = computed(() => {
  if (theme.mode === 'auto') return { icon: SunMoon, label: '跟随系统' }
  return theme.mode === 'dark' ? { icon: Moon, label: '暗色' } : { icon: Sun, label: '亮色' }
})
</script>

<template>
  <HTooltip :content="`主题：${current.label}`" side="bottom">
    <HButton variant="ghost" icon-only :aria-label="`主题：${current.label}，点击切换`" @click="theme.cycle()">
      <component :is="current.icon" :size="18" />
    </HButton>
  </HTooltip>
</template>
