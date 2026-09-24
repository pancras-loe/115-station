<script setup lang="ts">
import { computed } from 'vue'
import { SliderRange, SliderRoot, SliderThumb, SliderTrack } from 'reka-ui'

/**
 * 滑块（替代 NSlider）。Reka 的 Slider 管拖动、键盘方向键、定位；外观照 HeroUI：
 * 20px 高的圆角灰槽、accent 填充、槽内一颗白色胶囊把手。
 * 没用 @heroui/styles 的 slider.css：它的把手定位依赖 React Aria 注入的 left 百分比与边框留白，
 * 和 Reka 的定位方式对不上。
 */
const props = withDefaults(
  defineProps<{ min?: number; max?: number; step?: number; disabled?: boolean; ariaLabel?: string }>(),
  { min: 0, max: 100, step: 1 },
)
const model = defineModel<number>({ default: 0 })

// Reka 的 Slider 值是数组（为多把手准备），单值在这里包一层
const inner = computed({
  get: () => [model.value ?? props.min],
  set: (v: number[] | undefined) => {
    if (v && v.length) model.value = v[0]
  },
})
</script>

<template>
  <SliderRoot
    v-model="inner"
    class="h-slider"
    :min="min"
    :max="max"
    :step="step"
    :disabled="disabled"
  >
    <SliderTrack class="h-slider-track">
      <SliderRange class="h-slider-fill" />
    </SliderTrack>
    <SliderThumb class="h-slider-thumb" :aria-label="ariaLabel" />
  </SliderRoot>
</template>

<style scoped>
.h-slider {
  position: relative;
  display: flex;
  align-items: center;
  width: 100%;
  height: 24px;
  touch-action: none;
  user-select: none;
}
.h-slider[data-disabled] {
  opacity: 0.5;
}
.h-slider-track {
  position: relative;
  flex: 1;
  height: 8px;
  border-radius: 999px;
  background: var(--default);
  overflow: hidden;
}
.h-slider-fill {
  position: absolute;
  height: 100%;
  border-radius: 999px;
  background: var(--accent);
}
.h-slider-thumb {
  display: block;
  width: 20px;
  height: 20px;
  border-radius: 999px;
  background: var(--white, #fff);
  box-shadow:
    0 0 0 1px color-mix(in oklab, var(--foreground) 10%, transparent),
    0 2px 6px rgb(0 0 0 / 0.18);
  cursor: grab;
  transition: transform 120ms ease;
}
.h-slider-thumb:active {
  cursor: grabbing;
  transform: scale(1.08);
}
.h-slider-thumb:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
}
</style>
