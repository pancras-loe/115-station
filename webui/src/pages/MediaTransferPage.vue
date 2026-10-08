<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import HTabs from '@/components/hero/HTabs.vue'
import SubDetail from '@/components/subscribe/SubDetail.vue'
import SubscribeDialog from '@/components/subscribe/SubscribeDialog.vue'
import SearchTab from './transfer/SearchTab.vue'
import SubsTab from './transfer/SubsTab.vue'
import LinkTab from './transfer/LinkTab.vue'
import SettingsTab from './transfer/SettingsTab.vue'
import type { Subscription } from '@/api/subscribe'
import { useSubscriptions } from '@/composables/subscriptions'
import { useTabQuery } from '@/composables/useTabQuery'
import { useQueueStore } from '@/stores/queue'

/**
 * 影视转存（2026-10-08 起含资源订阅）：找资源 / 订阅 / 链接转存 / 设置。
 * 原来订阅是单独一页，新建订阅却得回这边找资源里点，两页来回跳；现在找资源里选中一部
 * 就能订阅、订阅过的在原地打开详情抽屉。所有设置（转存目录、各来源、订阅）收进「设置」
 * 页签，左边列小节、右边一次一张，不用整页长滚。
 */
const route = useRoute()
const router = useRouter()
const queue = useQueueStore()

// 旧地址：?tab=gy 等（最早四个站各一个页签）、?tab=sources&focus=gy（来源设置页签）
const LEGACY = ['gy', 'pansou', 'mukaku', 're0']
const legacy = route.query.tab as string | undefined
if (legacy && LEGACY.includes(legacy)) {
  void router.replace({ query: { tab: 'settings', sec: legacy } })
} else if (legacy === 'sources') {
  const focus = route.query.focus as string | undefined
  void router.replace({ query: { tab: 'settings', sec: focus || 'gy' } })
}

const tab = useTabQuery('search')

const subs = useSubscriptions()
onMounted(() => void subs.load())
const off = queue.onFinished((j) => {
  if (j.kind === 'subscribe') void subs.load()
})
onBeforeUnmount(off)

const TABS = computed(() => [
  { value: 'search', label: '找资源' },
  { value: 'subs', label: '订阅', count: subs.counts.value.active + subs.counts.value.stalled },
  { value: 'link', label: '链接转存' },
  { value: 'settings', label: '设置' },
])

// ---- 订阅详情抽屉：?sub= 打开，找资源与订阅两个页签共用 ----
const initial = Number(route.query.sub) || null
if (initial) subs.openDetail(initial)
let leaving = false
watch(subs.detailOpen, (open) => {
  if (leaving) return
  const id = open && subs.detailId.value ? String(subs.detailId.value) : undefined
  if (route.query.sub !== id) void router.replace({ query: { ...route.query, sub: id } })
})
// 抽屉状态是模块级的：离开页面时关掉，下次进来不会自己弹出来（关的时候别再改地址栏）
onBeforeUnmount(() => {
  leaving = true
  subs.detailOpen.value = false
})

const editing = ref<Subscription | null>(null)
const editOpen = ref(false)
function onEdit(s: Subscription) {
  editing.value = s
  editOpen.value = true
}
function onSaved() {
  void subs.load()
  if (subs.detailOpen.value) {
    // 让抽屉自己的 watch 重新拉：关一下再开会闪
    const id = subs.detailId.value
    subs.detailId.value = null
    setTimeout(() => (subs.detailId.value = id), 0)
  }
}
function onRemoved(id: number) {
  subs.rows.value = subs.rows.value.filter((r) => r.id !== id)
}
</script>

<template>
  <div class="h-tabs-page">
    <HTabs v-model="tab" :items="TABS" />
    <!-- 搜索页用 v-show 保活：去设置里登录一下、或看一眼订阅再切回来，搜到一半的结果还在 -->
    <SearchTab v-show="tab === 'search' || !TABS.some((t) => t.value === tab)" />
    <SubsTab v-if="tab === 'subs'" @create="tab = 'search'" />
    <LinkTab v-if="tab === 'link'" />
    <SettingsTab v-if="tab === 'settings'" />

    <SubDetail
      v-model:show="subs.detailOpen.value"
      :sub-id="subs.detailId.value"
      @edit="onEdit"
      @changed="subs.load()"
      @removed="onRemoved"
    />
    <SubscribeDialog v-model:show="editOpen" :edit="editing" @saved="onSaved" />
  </div>
</template>
