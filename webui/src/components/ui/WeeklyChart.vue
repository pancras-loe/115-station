<script setup lang="ts">
import { computed, ref } from 'vue'
import type { WeeklyPoint } from '@/types/dashboard'

const props = defineProps<{ data: WeeklyPoint[] }>()

// 单序列（每日入库数），所以不需要图例也不需要分类色板——一个主色即可。
// 形态选柱状：离散的 7 天、比较的是每日量级，不是趋势走向。
const W = 100
const H = 46
const GAP = 2

const max = computed(() => Math.max(1, ...props.data.map((d) => d.count)))
const bandW = computed(() => (props.data.length ? W / props.data.length : W))
const barW = computed(() => Math.max(1, bandW.value - GAP))

const bars = computed(() =>
  props.data.map((d, i) => {
    const h = (d.count / max.value) * H
    return {
      ...d,
      x: i * bandW.value + GAP / 2,
      // 0 值也留 1.5 高度：否则「当天 0 部」和「没有这一天」在图上无法区分
      y: H - Math.max(d.count > 0 ? 3 : 1.5, h),
      h: Math.max(d.count > 0 ? 3 : 1.5, h),
      isMax: d.count === max.value && d.count > 0,
    }
  }),
)

const hover = ref<number | null>(null)
const hasData = computed(() => props.data.some((d) => d.count > 0))
</script>

<template>
  <div class="chart">
    <svg :viewBox="`0 0 ${W} ${H + 2}`" preserveAspectRatio="none" class="plot" role="img"
      :aria-label="`近 7 天每日入库数，最高 ${max} 部`">
      <g v-for="(b, i) in bars" :key="b.day">
        <!-- 命中区比柱本身宽：细柱直接当热区会很难指中 -->
        <rect
          :x="i * bandW" y="0" :width="bandW" :height="H + 2"
          fill="transparent" @mouseenter="hover = i" @mouseleave="hover = null"
        />
        <rect
          :x="b.x" :y="b.y" :width="barW" :height="b.h" rx="1.2"
          class="bar" :class="{ zero: b.count === 0, hot: hover === i }"
        />
      </g>
      <line :x1="0" :y1="H + 1" :x2="W" :y2="H + 1" class="baseline" />
    </svg>

    <div class="axis">
      <span v-for="(b, i) in bars" :key="b.day" class="tick" :class="{ hot: hover === i }">
        {{ b.day }}
      </span>
    </div>

    <!-- 选择性直接标注：只标峰值，不是每根柱子都挂数字 -->
    <div class="readout">
      <template v-if="hover !== null">
        <span class="readout-day">{{ bars[hover].day }}</span>
        <span class="readout-val">{{ bars[hover].count }} 部</span>
      </template>
      <template v-else-if="hasData">
        <span class="readout-day">峰值</span>
        <span class="readout-val">{{ max }} 部 / 天</span>
      </template>
      <span v-else class="readout-empty">近 7 天无入库</span>
    </div>
  </div>
</template>

<style scoped>
.chart {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.plot {
  width: 100%;
  height: 92px;
  overflow: visible;
}
.bar {
  fill: var(--c-primary);
  opacity: 0.85;
  transition: opacity 0.15s, fill 0.15s;
}
.bar.zero {
  fill: var(--c-border-strong);
  opacity: 1;
}
.bar.hot {
  opacity: 1;
}
.baseline {
  stroke: var(--c-border);
  stroke-width: 1;
  vector-effect: non-scaling-stroke;
}

.axis {
  display: flex;
}
.tick {
  flex: 1;
  text-align: center;
  font-size: 10.5px;
  color: var(--c-text-3);
  font-variant-numeric: tabular-nums;
  transition: color 0.15s;
}
.tick.hot {
  color: var(--c-text-1);
  font-weight: 500;
}

.readout {
  display: flex;
  align-items: baseline;
  gap: 8px;
  padding-top: 6px;
  border-top: 1px solid var(--c-border);
  font-size: 12px;
  min-height: 26px;
}
.readout-day {
  color: var(--c-text-3);
}
.readout-val {
  color: var(--c-text-1);
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}
.readout-empty {
  color: var(--c-text-3);
}
</style>
