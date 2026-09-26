<script setup lang="ts">
import StrmTabBar from './strm/StrmTabBar.vue'
import ConfigTab from './strm/ConfigTab.vue'
import FullSyncTab from './strm/FullSyncTab.vue'
import IncrSyncTab from './strm/IncrSyncTab.vue'
import DeepDeleteTab from './strm/DeepDeleteTab.vue'
import { STRM_TABS } from './strm/tabs'
import { useFullSetting } from './strm/fullSetting'
import { useTabQuery } from '@/composables/useTabQuery'

/**
 * 路由路径仍是 /sync（旧版就是这个地址，用户可能已收藏），页面名字改叫「Strm 管理」：
 * 这里已经不只是同步，STRM 直链配置、失效 STRM 检测都在同一页。
 */
const tab = useTabQuery('config')

// 全量配置由页面持有，两个同步页签共用一份（cid / 本地目录 / 后缀是同一套）
const full = useFullSetting()
</script>

<template>
  <div class="page">
    <StrmTabBar v-model="tab" :tabs="STRM_TABS" />

    <Transition name="tab" mode="out-in">
      <ConfigTab v-if="tab === 'config'" key="config" />
      <FullSyncTab v-else-if="tab === 'full'" key="full" :full="full" />
      <IncrSyncTab v-else-if="tab === 'incr'" key="incr" :full="full" />
      <!-- 深度删除自己持有配置（setting「deepdel」），不共用 full -->
      <DeepDeleteTab v-else key="deepdel" />
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
