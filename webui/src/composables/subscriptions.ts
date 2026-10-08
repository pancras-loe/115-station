import { computed, ref } from 'vue'
import * as subscribeApi from '@/api/subscribe'
import type { Subscription } from '@/api/subscribe'
import { toastError } from '@/composables/useFeedback'

/**
 * 订阅列表与详情抽屉的共享状态。订阅并进影视转存页之后，「找资源」「订阅」两个页签
 * 都要用它：找资源里选中一部片要知道订阅过没有、点了直接在原地打开详情抽屉，
 * 不再跳到另一个页面。模块级单例，页签切来切去不重复拉。
 */
const rows = ref<Subscription[]>([])
const loading = ref(false)
const loaded = ref(false)

async function load() {
  loading.value = true
  try {
    rows.value = (await subscribeApi.list()).data ?? []
    loaded.value = true
  } catch (e) {
    toastError(e, '读取订阅失败')
  } finally {
    loading.value = false
  }
}

const counts = computed(() => {
  const n = { all: rows.value.length, active: 0, stalled: 0, paused: 0, done: 0 }
  for (const r of rows.value) n[r.state]++
  return n
})

function findSub(tmdbId: number, type: string) {
  return rows.value.find((r) => r.tmdb_id === tmdbId && r.media_type === type) ?? null
}

// ---- 详情抽屉（页面挂一份，?sub= 打开） ----
const detailId = ref<number | null>(null)
const detailOpen = ref(false)
function openDetail(id: number) {
  detailId.value = id
  detailOpen.value = true
}

export function useSubscriptions() {
  return { rows, loading, loaded, load, counts, findSub, detailId, detailOpen, openDetail }
}
