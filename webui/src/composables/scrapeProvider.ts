import { computed, ref } from 'vue'
import { organizeApi } from '@/api'
import type { ScrapeProvider } from '@/api/organize'

/**
 * 刮削方式（本站刮削 / Emby 刮削）。影视刮削页、海报墙、自动整理的只读概览都要看它，
 * 放在模块级：一处切换，其余页面不必重读就跟着变。
 * 存在刮削配置（setting `scrape`）里，但保存刮削配置时后端不认它，只能经 setScrapeProvider 改。
 */
const provider = ref<ScrapeProvider>('station')
const loaded = ref(false)
let pending: Promise<void> | null = null

async function load(force = false) {
  if (loaded.value && !force) return
  if (pending) return pending
  pending = (async () => {
    try {
      const res = await organizeApi.getScrapeConfig()
      provider.value = res.cfg?.provider === 'emby' ? 'emby' : 'station'
    } catch {
      // 读不到按本站刮削（老行为）
    } finally {
      loaded.value = true
      pending = null
    }
  })()
  return pending
}

async function set(p: ScrapeProvider) {
  const res = await organizeApi.setScrapeProvider(p)
  provider.value = res.provider === 'emby' ? 'emby' : 'station'
  return res
}

export function useScrapeProvider() {
  return {
    provider,
    loaded,
    /** Emby 刮削：本站不写任何元数据 */
    embyScrapes: computed(() => provider.value === 'emby'),
    load,
    set,
  }
}
