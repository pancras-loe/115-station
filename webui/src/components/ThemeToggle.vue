<script setup lang="ts">
import { computed } from 'vue'
import { NButton, NTooltip } from 'naive-ui'
import { Moon, Sun, SunMoon } from '@lucide/vue'
import { useThemeStore } from '@/stores/theme'

const theme = useThemeStore()

const current = computed(() => {
  if (theme.mode === 'auto') return { icon: SunMoon, label: '跟随系统' }
  return theme.mode === 'dark' ? { icon: Moon, label: '暗色' } : { icon: Sun, label: '亮色' }
})
</script>

<template>
  <NTooltip>
    <template #trigger>
      <NButton quaternary circle :aria-label="`主题：${current.label}，点击切换`" @click="theme.cycle()">
        <component :is="current.icon" :size="18" />
      </NButton>
    </template>
    主题：{{ current.label }}
  </NTooltip>
</template>
