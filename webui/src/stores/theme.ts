import { defineStore } from 'pinia'
import { computed, ref, watchEffect } from 'vue'

export type ThemeMode = 'light' | 'dark' | 'auto'
/**
 * 亮色有两套配色：warm 是影院风的暖橙（默认），fresh 是冷调的清爽青蓝。
 * 暗色只有一套，配色只在亮色下生效（跟随系统且系统是亮色时同样生效）。
 */
export type LightPalette = 'warm' | 'fresh'

const STORAGE_KEY = 'ui.theme'
const PALETTE_KEY = 'ui.palette'
const media = window.matchMedia('(prefers-color-scheme: dark)')

// localStorage 在无痕模式 / 禁用站点数据时可能抛异常，读写都兜住
function read(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}
function write(key: string, v: string) {
  try {
    localStorage.setItem(key, v)
  } catch {
    // 存不下就只管这一次
  }
}

function readMode(): ThemeMode {
  const v = read(STORAGE_KEY)
  // 没选过的新访客默认暗色（影院风的主场）；index.html 首帧脚本同一口径
  return v === 'light' || v === 'dark' || v === 'auto' ? v : 'dark'
}
function readPalette(): LightPalette {
  return read(PALETTE_KEY) === 'fresh' ? 'fresh' : 'warm'
}

export const useThemeStore = defineStore('theme', () => {
  const mode = ref<ThemeMode>(readMode())
  const palette = ref<LightPalette>(readPalette())
  // 系统偏好单独跟踪：auto 模式下用户切换系统主题要立刻生效，不能只在启动时读一次
  const systemDark = ref(media.matches)
  media.addEventListener('change', (e) => {
    systemDark.value = e.matches
  })

  const isDark = computed(() => (mode.value === 'auto' ? systemDark.value : mode.value === 'dark'))

  watchEffect(() => {
    const root = document.documentElement
    root.classList.toggle('dark', isDark.value)
    // 配色标记常挂着，CSS 只在非 .dark 时读它（main.css 的 html[data-palette='fresh']:not(.dark)）
    root.dataset.palette = palette.value
    write(STORAGE_KEY, mode.value)
    write(PALETTE_KEY, palette.value)
  })

  function setMode(next: ThemeMode) {
    mode.value = next
  }

  /** 选一套亮色配色，顺带切到亮色 —— 在暗色下点「清爽」却看不到变化会让人以为没生效 */
  function setLight(next: LightPalette) {
    palette.value = next
    mode.value = 'light'
  }

  return { mode, palette, isDark, systemDark, setMode, setLight }
})
