<script setup lang="ts">
import { TooltipContent, TooltipPortal, TooltipRoot, TooltipTrigger } from 'reka-ui'

/**
 * Reka 负责交互（悬停延迟、键盘焦点、定位、Esc 关闭），HeroUI 的 .tooltip 负责外观。
 * 动画属性的桥接在 styles/hero-adapter.css。
 * 触屏上没有 hover：Reka 的 Tooltip 在触屏上不会弹出，
 * 所以别把「只有提示里才有」的关键信息放进来。
 */
withDefaults(
  defineProps<{ content?: string; side?: 'top' | 'bottom' | 'left' | 'right'; disabled?: boolean }>(),
  { side: 'top' },
)
</script>

<template>
  <TooltipRoot :disabled="disabled">
    <TooltipTrigger as-child><slot /></TooltipTrigger>
    <TooltipPortal>
      <TooltipContent class="tooltip" :side="side" :side-offset="6" :collision-padding="12">
        <slot name="content">{{ content }}</slot>
      </TooltipContent>
    </TooltipPortal>
  </TooltipRoot>
</template>
