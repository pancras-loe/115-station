<script setup lang="ts">
import { onMounted, ref } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import CandidateGrid from '@/components/transfer/CandidateGrid.vue'
import { resourcesApi, transferApi } from '@/api'
import type { DiscoverList, TmdbCandidate } from '@/api/resources'
import type { OwnedInfo } from '@/api/transfer'

/**
 * 找资源页没在搜东西时的趋势 / 热门榜单（TMDB）。点一部和搜索结果里点一部是同一回事：
 * 条目自带 TMDB 编号，直接进找资源。榜单清单由后端给（tmdbdiscover.go），这里不写死。
 */
const emit = defineEmits<{ pick: [TmdbCandidate] }>()

const LS_KEY = 'transfer.discover.list'
const lists = ref<DiscoverList[]>([])
const current = ref('')
const items = ref<TmdbCandidate[]>([])
const owned = ref<Record<string, OwnedInfo>>({})
const page = ref(1)
const hasMore = ref(false)
const loading = ref(false)
const loadingMore = ref(false)
const hint = ref('')
// 切页签很快时，晚回来的旧榜单别盖掉新的
let seq = 0

async function load(more = false) {
  const my = ++seq
  const list = current.value
  if (more) loadingMore.value = true
  else {
    loading.value = true
    items.value = []
    page.value = 1
    hint.value = ''
  }
  try {
    const p = more ? page.value + 1 : 1
    const d = await resourcesApi.tmdbDiscover(list, p)
    if (my !== seq) return
    const got = d.data ?? []
    // 榜单翻页期间排名会变，同一部可能在两页各出现一次
    const seen = new Set(items.value.map((c) => `${c.media_type}:${c.id}`))
    const fresh = got.filter((c) => !seen.has(`${c.media_type}:${c.id}`))
    items.value = [...items.value, ...fresh]
    page.value = p
    hasMore.value = !!d.has_more
    if (!items.value.length) hint.value = '这个榜单暂时是空的'
    if (fresh.length) {
      transferApi
        .owned(fresh.map((c) => `${c.media_type}:${c.id}`))
        .then((r) => (owned.value = { ...owned.value, ...(r.data ?? {}) }))
        .catch(() => {})
    }
  } catch (e) {
    if (my === seq && !more) hint.value = e instanceof Error ? e.message : '榜单读取失败'
  } finally {
    if (my === seq) {
      loading.value = false
      loadingMore.value = false
    }
  }
}

function switchTo(key: string) {
  current.value = key
  try {
    localStorage.setItem(LS_KEY, key)
  } catch {
    // 存不了就每次从第一个榜单开始
  }
  void load()
}

onMounted(async () => {
  try {
    lists.value = (await resourcesApi.tmdbDiscoverLists()).lists ?? []
  } catch (e) {
    hint.value = e instanceof Error ? e.message : '榜单读取失败'
    return
  }
  let saved = ''
  try {
    saved = localStorage.getItem(LS_KEY) ?? ''
  } catch {
    // 同上
  }
  const first = lists.value.find((l) => l.key === saved)?.key ?? lists.value[0]?.key
  if (first) switchTo(first)
})
</script>

<template>
  <div class="discover">
    <HSegmented
      v-if="lists.length"
      :model-value="current"
      size="sm"
      aria-label="榜单"
      :options="lists.map((l) => ({ label: l.label, value: l.key }))"
      @update:model-value="(v) => v && v !== current && switchTo(v)"
    />
    <CandidateGrid :items="items" :owned="owned" :loading="loading" :hint="hint" no-skip @pick="(c) => emit('pick', c)" />
    <div v-if="hasMore && !loading" class="more">
      <HButton variant="ghost" size="sm" :loading="loadingMore" @click="load(true)">加载更多</HButton>
    </div>
  </div>
</template>

<style scoped>
.discover {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.more {
  display: flex;
  justify-content: center;
}
</style>
