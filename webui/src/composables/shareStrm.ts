import { computed, ref } from 'vue'
import { shareStrmApi } from '@/api'
import type { ShareStrmConfigResp } from '@/api/sharestrm'

/**
 * 分享 STRM 的配置（后端 sharestrm.go）。模块级状态：侧栏决定显不显示「分享 STRM」、
 * 链接转存页决定「生成 STRM」灰不灰，都看这一份；改了开关后调 reload。
 */
const resp = ref<ShareStrmConfigResp | null>(null)
let inflight: Promise<void> | null = null

async function load(force = false) {
  if (inflight && !force) return inflight
  inflight = shareStrmApi
    .config()
    .then((d) => {
      resp.value = d
    })
    .catch(() => {
      // 读不到按没开处理：入口灰掉，后端自己还会拦
    })
  return inflight
}

export function useShareStrm() {
  if (!resp.value) load()
  return {
    resp,
    enabled: computed(() => !!resp.value?.config.enabled),
    /** 现在不能生成的原因（没开 / 没填直连域名 / 目录和媒体库重叠），能生成返回空串 */
    blocked: computed(() => {
      const d = resp.value
      if (!d || !d.config.enabled) return '分享 STRM 没开：到「扩展功能 → 分享 STRM」打开'
      if (!d.domain_ok) return '还没填直连域名（Strm 管理 → 配置）'
      return d.root_problem
    }),
    defaultAction: computed(() => (resp.value?.config.enabled ? resp.value.config.default_action : 'transfer')),
    reload: () => load(true),
  }
}
