<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { SlidersHorizontal } from '@lucide/vue'
import DiscoverHero from '@/components/transfer/DiscoverHero.vue'
import DiscoverRow from '@/components/transfer/DiscoverRow.vue'
import { resourcesApi, transferApi } from '@/api'
import type { DiscoverList, TmdbCandidate } from '@/api/resources'
import type { OwnedInfo } from '@/api/transfer'

/**
 * 「找资源」没在搜东西时的发现首页：顶上聚光灯横幅（今日趋势里有背景图的前几部），
 * 下面每个榜单一行横向海报带。原来是一个榜单分段器 + 一整块海报墙，一次只能看一个榜单、
 * 切来切去；现在一路往下滑就能逛完（Jellyseerr / MoviePilot「发现」首页的排法）。
 * 「查看全部」与「按条件筛选」交给上层换成整页海报墙（DiscoverPanel），那边能翻页。
 */
const emit = defineEmits<{ pick: [TmdbCandidate]; browse: [string] }>()

/** 横幅取哪个榜单、放几部 */
const HERO_LIST = 'trending_day'
const HERO_MAX = 7
/** 给前十名标名次的榜单：只有趋势是按热度实时排的，热门电影那几个排名意义不大 */
const RANKED = new Set(['trending_day', 'trending_week'])

const lists = ref<DiscoverList[]>([])
const hero = ref<TmdbCandidate[]>([])
const heroOwned = ref<Record<string, OwnedInfo>>({})
const loading = ref(true)
const err = ref('')

onMounted(async () => {
  try {
    const d = await resourcesApi.tmdbDiscoverLists()
    lists.value = d.lists ?? []
  } catch (e) {
    err.value = e instanceof Error ? e.message : '榜单读取失败'
    loading.value = false
    return
  }
  const key = lists.value.some((l) => l.key === HERO_LIST) ? HERO_LIST : lists.value[0]?.key
  if (!key) {
    loading.value = false
    return
  }
  try {
    const d = await resourcesApi.tmdbDiscover(key, 1)
    hero.value = (d.data ?? []).filter((c) => c.backdrop).slice(0, HERO_MAX)
    if (hero.value.length) {
      transferApi
        .owned(hero.value.map((c) => `${c.media_type}:${c.id}`))
        .then((r) => (heroOwned.value = r.data ?? {}))
        .catch(() => {})
    }
  } catch {
    // 横幅拉不到就显示「今天想看点什么」，下面的榜单各自报错
  } finally {
    loading.value = false
  }
})

// 横幅已经是今日趋势了，下面那一行就不再重复
const rows = computed(() => lists.value.filter((l) => !(hero.value.length && l.key === HERO_LIST)))
</script>

<template>
  <div class="home">
    <DiscoverHero :items="hero" :owned="heroOwned" :loading="loading" @pick="(c) => emit('pick', c)">
      <template #search><slot name="search" /></template>
    </DiscoverHero>

    <p v-if="err" class="err">{{ err }}</p>
    <template v-else>
      <div v-if="rows.length" class="toolbar">
        <button type="button" class="filter-btn" @click="emit('browse', 'filter')">
          <SlidersHorizontal :size="14" />按地区 / 题材筛选
        </button>
      </div>
      <DiscoverRow
        v-for="l in rows"
        :key="l.key"
        :list="l"
        :ranked="RANKED.has(l.key)"
        @pick="(c) => emit('pick', c)"
        @more="(k) => emit('browse', k)"
      />
    </template>
  </div>
</template>

<style scoped>
.home {
  display: flex;
  flex-direction: column;
  gap: 26px;
  min-width: 0;
}
.toolbar {
  display: flex;
  justify-content: flex-end;
  margin: -12px 0 -14px;
}
.filter-btn {
  all: unset;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 5px 12px;
  border-radius: 999px;
  font-size: 12.5px;
  background: var(--default);
  color: var(--muted);
  cursor: pointer;
  transition:
    color 0.15s,
    background-color 0.15s;
}
.filter-btn:hover {
  color: var(--foreground);
  background: var(--default-hover);
}
.filter-btn:focus-visible {
  outline: 2px solid var(--focus);
}
.err {
  margin: 0;
  padding: 32px 0;
  text-align: center;
  font-size: 13px;
  color: var(--muted);
}
</style>
