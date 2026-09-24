<script setup lang="ts">
import { CircleAlert, CircleCheck, Info, TriangleAlert, X } from '@lucide/vue'
import { dismissToast, toasts, type ToastTone } from '@/composables/useFeedback'

/**
 * 轻提示的渲染层，挂在 App.vue 里一次。外观照 HeroUI 的 toast（浮层底色、大圆角、状态图标），
 * 堆叠逻辑没用它的——那套位移是 React 版用 JS 逐条算的，这里用 TransitionGroup 竖排就够了。
 * 顶部居中：底部在手机上被导航栏占着。
 */
const ICON: Record<ToastTone, typeof Info> = {
  success: CircleCheck,
  warning: TriangleAlert,
  danger: CircleAlert,
  accent: Info,
}
</script>

<template>
  <div class="h-toaster" role="region" aria-label="通知" aria-live="polite">
    <TransitionGroup name="h-toast">
      <div
        v-for="t in toasts"
        :key="t.id"
        class="h-toast"
        :class="`tone-${t.tone}`"
        :role="t.tone === 'danger' ? 'alert' : 'status'"
      >
        <component :is="ICON[t.tone]" :size="17" :stroke-width="2" class="h-toast-icon" />
        <span class="h-toast-text">{{ t.text }}</span>
        <button type="button" class="h-toast-close" aria-label="关闭" @click="dismissToast(t.id)">
          <X :size="14" />
        </button>
      </div>
    </TransitionGroup>
  </div>
</template>

<style scoped>
.h-toaster {
  position: fixed;
  top: calc(12px + env(safe-area-inset-top));
  left: 50%;
  z-index: var(--z-index-toast, 100001);
  width: min(440px, calc(100vw - 24px));
  translate: -50% 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  pointer-events: none;
}
.h-toast {
  pointer-events: auto;
  display: flex;
  align-items: flex-start;
  gap: 10px;
  max-width: 100%;
  padding: 11px 12px 11px 16px;
  border-radius: 22px;
  background: var(--overlay);
  box-shadow: var(--overlay-shadow);
  font-size: 13.5px;
  line-height: 1.55;
  color: var(--foreground);
}
.h-toast-icon {
  flex: none;
  margin-top: 1px;
}
.tone-success .h-toast-icon {
  color: var(--success);
}
.tone-warning .h-toast-icon {
  color: var(--warning);
}
.tone-danger .h-toast-icon {
  color: var(--danger);
}
.tone-accent .h-toast-icon {
  color: var(--accent);
}
.h-toast-text {
  flex: 1;
  min-width: 0;
  word-break: break-word;
}
.h-toast-close {
  flex: none;
  display: grid;
  place-items: center;
  width: 22px;
  height: 22px;
  margin-top: -1px;
  border: 0;
  border-radius: 999px;
  background: none;
  color: var(--muted);
  cursor: pointer;
}
.h-toast-close:hover {
  background: var(--default);
  color: var(--foreground);
}

.h-toast-enter-active,
.h-toast-leave-active {
  transition:
    opacity 250ms var(--ease-out-fluid, ease),
    transform 350ms var(--ease-out-fluid, ease);
}
.h-toast-enter-from {
  opacity: 0;
  transform: translateY(-12px) scale(0.96);
}
.h-toast-leave-to {
  opacity: 0;
  transform: scale(0.96);
}
.h-toast-move {
  transition: transform 300ms var(--ease-out-fluid, ease);
}
@media (prefers-reduced-motion: reduce) {
  .h-toast-enter-active,
  .h-toast-leave-active,
  .h-toast-move {
    transition: none;
  }
}
</style>
