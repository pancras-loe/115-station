<script setup lang="ts">
import { computed } from 'vue'
import { loadTone } from '@/utils/format'

const props = defineProps<{ percent: number }>()

// 最小 2% 宽度：0 值时也画出一条，否则「有这个指标但是 0」和「没这个指标」看起来一样
const width = computed(() => `${Math.max(2, Math.min(100, props.percent))}%`)
const tone = computed(() => loadTone(props.percent))
</script>

<template>
  <div class="track" role="progressbar" :aria-valuenow="Math.round(percent)" aria-valuemin="0" aria-valuemax="100">
    <div class="fill" :class="tone" :style="{ width }" />
  </div>
</template>

<style scoped>
.track {
  height: 6px;
  border-radius: 999px;
  background: var(--c-bg-hover);
  overflow: hidden;
}
.fill {
  height: 100%;
  border-radius: 999px;
  transition: width 0.4s cubic-bezier(0.4, 0, 0.2, 1), background-color 0.2s;
}
.fill.normal {
  background: linear-gradient(90deg, var(--c-primary), var(--c-primary-hover));
}
.fill.warning {
  background: var(--c-warning);
}
.fill.danger {
  background: var(--c-danger);
}
</style>
