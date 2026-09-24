<script setup lang="ts">
import { PopoverContent, PopoverPortal, PopoverRoot, PopoverTrigger } from 'reka-ui'

/**
 * 点击打开的浮层面板（替代 NPopover trigger="click"）。默认插槽是触发元素，#content 是面板内容。
 * 面板样式（宽度、内边距）由调用方给 content-class，因为它在 Portal 里，调用方的 scoped 样式选不中——
 * 调用方用 :deep 或全局类名写。
 */
withDefaults(
  defineProps<{
    side?: 'top' | 'bottom' | 'left' | 'right'
    align?: 'start' | 'center' | 'end'
    contentClass?: string
  }>(),
  { side: 'bottom', align: 'start' },
)
const open = defineModel<boolean>('open', { default: false })
</script>

<template>
  <PopoverRoot v-model:open="open">
    <PopoverTrigger as-child><slot /></PopoverTrigger>
    <PopoverPortal>
      <PopoverContent
        class="popover h-popover"
        :class="contentClass"
        :side="side"
        :align="align"
        :side-offset="6"
        :collision-padding="12"
      >
        <slot name="content" />
      </PopoverContent>
    </PopoverPortal>
  </PopoverRoot>
</template>
