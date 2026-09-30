<script setup lang="ts">
import { nextTick } from 'vue'
import HTabs from '@/components/hero/HTabs.vue'
import StrmOverview from './strm/StrmOverview.vue'
import ConfigTab from './strm/ConfigTab.vue'
import FullSyncTab from './strm/FullSyncTab.vue'
import IncrSyncTab from './strm/IncrSyncTab.vue'
import DeepDeleteTab from './strm/DeepDeleteTab.vue'
import { STRM_TABS } from './strm/tabs'
import { useFullSetting } from './strm/fullSetting'
import { useDeepDelSetting } from './strm/deepDelSetting'
import { useStrmRuns } from './strm/useStrmRuns'
import { useTabQuery } from '@/composables/useTabQuery'

/**
 * 路由路径仍是 /sync（旧版就是这个地址，用户可能已收藏），页面名字改叫「Strm 管理」：
 * 这里已经不只是同步，STRM 直链配置、失效 STRM 检测都在同一页。
 *
 * 布局（2026-09-30 改）：顶部一行状态条（增量 / 全量 / 失效 STRM / 深度删除 现在怎样，只看不做），
 * 下面四个页签。原来「现在好不好」要先猜对页签、再翻到表单底部才看得到。
 * 「立即同步」「开始全量」在各自页签里，逻辑共用 useStrmRuns（这里持有一份）。
 * 页签值（config / full / incr / deepdel）沿用，收藏的 ?tab= 照样能打开。
 */
const tab = useTabQuery('config')

// 全量配置由页面持有：两个同步页签和总览共用一份（cid / 本地目录 / 后缀是同一套）
const full = useFullSetting()
const deepdel = useDeepDelSetting()
const runs = useStrmRuns(full)

async function goto(key: string, anchor?: string) {
  tab.value = key
  if (!anchor) return
  // 等页签切换的过渡动画把新内容挂上来再滚
  await nextTick()
  window.setTimeout(() => {
    document.getElementById(`strm-${anchor}`)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  }, 200)
}

const TAB_ITEMS = STRM_TABS.map((t) => ({ value: t.key, label: t.label }))
</script>

<template>
  <div class="page">
    <StrmOverview :full="full" :runs="runs" :deepdel="deepdel" @goto="goto" />

    <HTabs v-model="tab" :items="TAB_ITEMS" />

    <Transition name="tab" mode="out-in">
      <ConfigTab v-if="tab === 'config'" key="config" />
      <FullSyncTab v-else-if="tab === 'full'" key="full" :full="full" :runs="runs" />
      <IncrSyncTab v-else-if="tab === 'incr'" key="incr" :runs="runs" />
      <!-- 深度删除自己的配置（setting「deepdel」），不共用 full -->
      <DeepDeleteTab v-else key="deepdel" :setting="deepdel" />
    </Transition>
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.tab-enter-active,
.tab-leave-active {
  transition:
    opacity 0.16s ease,
    transform 0.16s ease;
}
.tab-enter-from {
  opacity: 0;
  transform: translateY(5px);
}
.tab-leave-to {
  opacity: 0;
  transform: translateY(-3px);
}
</style>
