<script setup lang="ts">
import { computed } from 'vue'
import { loadTone } from '@/utils/format'

const props = defineProps<{ percent: number }>()

// 最小 2% 宽度：0 值时也画出一条，否则「有这个指标但是 0」和「没这个指标」看起来一样
const width = computed(() => `${Math.max(2, Math.min(100, props.percent))}%`)
const color = computed(() => ({ normal: 'accent', warning: 'warning', danger: 'danger' })[loadTone(props.percent)] ?? 'accent')
</script>

<template>
  <!-- HeroUI 的 .meter 自带 label/output 两格网格；这里只用它的轨道与填充 -->
  <div
    class="meter meter--md"
    :class="`meter--${color}`"
    role="meter"
    :aria-valuenow="Math.round(percent)"
    aria-valuemin="0"
    aria-valuemax="100"
  >
    <div class="meter__track"><div class="meter__fill" :style="{ width }" /></div>
  </div>
</template>
