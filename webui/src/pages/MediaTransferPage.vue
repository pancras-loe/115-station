<script setup lang="ts">
import { useRoute, useRouter } from 'vue-router'
import HTabs from '@/components/hero/HTabs.vue'
import SearchTab from './transfer/SearchTab.vue'
import SourcesTab from './transfer/SourcesTab.vue'
import { useTabQuery } from '@/composables/useTabQuery'

/**
 * 影视转存：原来观影 / 盘搜 / 不太灵 / RE0 各占一个页签，找一部片要搜四遍。
 * 现在按影片搜一次、所有来源一起出结果；各站的账号与地址收进「来源设置」。
 */
const route = useRoute()
const router = useRouter()

// 旧页签地址（?tab=gy 等，可能被收藏了）：带到来源设置里对应的那张卡片
const LEGACY = ['gy', 'pansou', 'mukaku', 're0']
const legacy = route.query.tab as string | undefined
if (legacy && LEGACY.includes(legacy)) {
  router.replace({ query: { tab: 'sources', focus: legacy } })
}

const tab = useTabQuery('search')

const TABS = [
  { value: 'search', label: '找资源' },
  { value: 'sources', label: '来源设置' },
]
</script>

<template>
  <div class="h-tabs-page">
    <HTabs v-model="tab" :items="TABS" />
    <!-- 搜索页用 v-show 保活：去来源设置登录一下再切回来，搜到一半的结果还在 -->
    <SearchTab v-show="tab !== 'sources'" />
    <SourcesTab v-if="tab === 'sources'" />
  </div>
</template>
