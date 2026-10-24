<script setup lang="ts">
import { computed } from 'vue'
import HSelect from '@/components/hero/HSelect.vue'
import { useStagingSubs } from '@/composables/stagingSubs'

/**
 * 「转存到」：找资源与链接转存提交前选落进转存目录本身还是某个绑定了媒体库的子目录。
 * 没有任何绑定时不显示（绝大多数用户只有一个库）。选择记在本机，下次打开沿用
 */
const { options, current, setPick, showPicker: show, needPick } = useStagingSubs()
const model = computed({
  get: () => current.value,
  set: (v: string) => setPick(v),
})
</script>

<template>
  <label v-if="show" class="subdir-pick">
    <span class="lbl">转存到</span>
    <HSelect v-model="model" :options="options" placeholder="选转存到哪个库" aria-label="转存到" />
    <RouterLink v-if="needPick && !current" class="hint" :to="{ name: 'media-transfer', query: { tab: 'sources', sec: 'folder' } }">
      设个默认就不用每次选
    </RouterLink>
  </label>
</template>

<style scoped>
.subdir-pick {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.hint {
  font-size: 12px;
  white-space: nowrap;
}
.lbl {
  font-size: 13px;
  color: var(--muted);
  white-space: nowrap;
}
.subdir-pick :deep(.select) {
  min-width: 160px;
}
</style>
