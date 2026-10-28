import { computed, ref } from 'vue'
import { configApi } from '@/api'
import type { MediaServerCaps } from '@/api/config'

/**
 * 当前媒体服务器（Emby / Jellyfin）与它做不了的功能。模块级状态，各页面共用一份。
 * key 与后端 mediaserver.go 的 msFeat* 一一对应
 */
export type MsFeature = 'pre_probe' | 'probe_log' | 'deep_delete' | 'people_merge'

const caps = ref<MediaServerCaps | null>(null)
let inflight: Promise<void> | null = null

async function load(force = false) {
  if (inflight && !force) return inflight
  inflight = configApi
    .mediaServerCaps()
    .then((d) => {
      caps.value = d
    })
    .catch(() => {
      // 读不到按 Emby 显示，不挡功能（后端入口自己还会拦）
    })
  return inflight
}

export function useMediaServer() {
  if (!caps.value) load()
  return {
    /** 显示用的名字：没配或读不到时是 Emby */
    name: computed(() => caps.value?.name || 'Emby'),
    kind: computed(() => caps.value?.kind || ''),
    isJellyfin: computed(() => caps.value?.kind === 'jellyfin'),
    /** 做不了时返回原因，能做返回空串 */
    blocked: (f: MsFeature) => caps.value?.unsupported?.[f] || '',
    /** 改了服务器配置后重新读 */
    reload: () => load(true),
  }
}
