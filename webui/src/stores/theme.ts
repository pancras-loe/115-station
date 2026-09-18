import { defineStore } from 'pinia'
import { computed, ref, watchEffect } from 'vue'

export type ThemeMode = 'light' | 'dark' | 'auto'

const STORAGE_KEY = 'ui.theme'
const media = window.matchMedia('(prefers-color-scheme: dark)')

function readStored(): ThemeMode {
  const v = localStorage.getItem(STORAGE_KEY)
  return v === 'light' || v === 'dark' || v === 'auto' ? v : 'auto'
}

export const useThemeStore = defineStore('theme', () => {
  const mode = ref<ThemeMode>(readStored())
  // 系统偏好单独跟踪：auto 模式下用户切换系统主题要立刻生效，不能只在启动时读一次
  const systemDark = ref(media.matches)
  media.addEventListener('change', (e) => {
    systemDark.value = e.matches
  })

  const isDark = computed(() => (mode.value === 'auto' ? systemDark.value : mode.value === 'dark'))

  watchEffect(() => {
    document.documentElement.classList.toggle('dark', isDark.value)
    localStorage.setItem(STORAGE_KEY, mode.value)
  })

  function setMode(next: ThemeMode) {
    mode.value = next
  }

  /** 顺序切换：跟随系统 → 亮色 → 暗色 → 跟随系统 */
  function cycle() {
    mode.value = mode.value === 'auto' ? 'light' : mode.value === 'light' ? 'dark' : 'auto'
  }

  return { mode, isDark, systemDark, setMode, cycle }
})
