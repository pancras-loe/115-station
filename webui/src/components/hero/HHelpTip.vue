<script setup lang="ts">
import { ref } from 'vue'
import { PopoverContent, PopoverPortal, PopoverRoot, PopoverTrigger } from 'reka-ui'
import { CircleHelp } from '@lucide/vue'

/**
 * 表单标签旁的问号说明。不用 Tooltip：Tooltip 只认 hover，触屏上点了没反应，
 * 而这些说明（「0 = 关闭」「最小 15 秒」这类）恰恰是手机上填表最需要看的。
 *
 * 做法是 Popover：鼠标悬停时打开、移开关闭（和 Tooltip 手感一样），
 * 手指点一下打开、点外面关闭。pointerType 区分两种输入，免得触屏的模拟 hover 抢先把它开了又关。
 */
defineProps<{ text: string }>()
const open = ref(false)
let timer: number | undefined

function hover(on: boolean, e: PointerEvent) {
  if (e.pointerType !== 'mouse') return
  clearTimeout(timer)
  timer = window.setTimeout(() => (open.value = on), on ? 250 : 120)
}
</script>

<template>
  <PopoverRoot v-model:open="open">
    <PopoverTrigger
      class="h-help"
      aria-label="说明"
      @pointerenter="hover(true, $event)"
      @pointerleave="hover(false, $event)"
    >
      <CircleHelp :size="14" />
    </PopoverTrigger>
    <PopoverPortal>
      <PopoverContent
        class="popover h-popover h-help-pop"
        side="top"
        :side-offset="6"
        :collision-padding="12"
        @pointerenter="hover(true, $event)"
        @pointerleave="hover(false, $event)"
        @open-auto-focus.prevent
      >
        {{ text }}
      </PopoverContent>
    </PopoverPortal>
  </PopoverRoot>
</template>

<!-- 面板在 Portal 里且外面包着 Reka 的定位 wrapper，scoped 选不中，用全局类名 -->
<style>
.h-help {
  display: inline-grid;
  place-items: center;
  width: 20px;
  height: 20px;
  margin: -3px 0;
  padding: 0;
  border: 0;
  border-radius: 999px;
  background: none;
  color: color-mix(in oklab, var(--muted) 70%, transparent);
  cursor: help;
  transition: color 150ms ease;
}
.h-help:hover,
.h-help[data-state='open'] {
  color: var(--accent);
}
.h-help:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.h-help-pop {
  max-width: min(300px, calc(100vw - 24px));
  padding: 10px 12px;
  font-size: 12.5px;
  line-height: 1.7;
  color: var(--foreground);
}
</style>
