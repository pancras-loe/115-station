<script setup lang="ts">
import { DialogContent, DialogOverlay, DialogPortal, DialogRoot, DialogTitle } from 'reka-ui'

/**
 * 侧边抽屉（替代 NDrawer）：Reka 的 Dialog 管焦点锁定、Esc、点遮罩关闭，外观是 HeroUI 的浮层 + 模糊遮罩。
 * 目前只有移动端导航在用（从左侧滑出）。标题只给读屏器，界面上不显示。
 */
withDefaults(defineProps<{ width?: string; title?: string }>(), { width: '260px', title: '菜单' })
const open = defineModel<boolean>('show', { required: true })
</script>

<template>
  <DialogRoot v-model:open="open">
    <DialogPortal>
      <DialogOverlay class="h-drawer-backdrop" />
      <DialogContent class="h-drawer" :style="{ width }" :aria-describedby="undefined">
        <DialogTitle class="sr-only">{{ title }}</DialogTitle>
        <slot />
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>

<!-- Portal 里的元素，scoped 选不中 -->
<style>
.h-drawer-backdrop {
  position: fixed;
  inset: 0;
  z-index: var(--z-index-overlay, 100000);
  background: var(--backdrop);
  backdrop-filter: blur(4px);
}
.h-drawer-backdrop[data-state='open'] {
  animation: h-fade-in 200ms ease-out;
}
.h-drawer-backdrop[data-state='closed'] {
  animation: h-fade-out 150ms ease-in;
}
.h-drawer {
  position: fixed;
  top: 0;
  bottom: 0;
  left: 0;
  z-index: var(--z-index-overlay, 100000);
  max-width: 86vw;
  display: flex;
  flex-direction: column;
  padding-top: env(safe-area-inset-top);
  padding-bottom: env(safe-area-inset-bottom);
  background: var(--background);
  border-radius: 0 28px 28px 0;
  box-shadow: var(--overlay-shadow);
  overflow: hidden;
  outline: none;
}
.h-drawer[data-state='open'] {
  animation: h-drawer-in 280ms var(--ease-out-fluid, cubic-bezier(0.22, 1, 0.36, 1));
}
.h-drawer[data-state='closed'] {
  animation: h-drawer-out 180ms ease-in;
}
@keyframes h-drawer-in {
  from {
    transform: translateX(-100%);
  }
}
@keyframes h-drawer-out {
  to {
    transform: translateX(-100%);
  }
}
@keyframes h-fade-in {
  from {
    opacity: 0;
  }
}
@keyframes h-fade-out {
  to {
    opacity: 0;
  }
}
@media (prefers-reduced-motion: reduce) {
  .h-drawer,
  .h-drawer-backdrop {
    animation: none !important;
  }
}
</style>
