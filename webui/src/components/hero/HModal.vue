<script setup lang="ts">
import { computed } from 'vue'
import {
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogOverlay,
  DialogPortal,
  DialogRoot,
  DialogTitle,
} from 'reka-ui'
import { X } from '@lucide/vue'

/**
 * 弹窗（替代 NModal preset="card"）。
 * Reka 的 DialogOverlay 当 HeroUI 的 backdrop，DialogContent 当 container（铺满屏、自身不接事件），
 * 真正的白色面板是里面的 .modal__dialog。点面板以外的地方落在 Overlay 上，Reka 判为「点外面」关闭。
 *
 * data-placement="auto" 是 HeroUI 自带的响应式：窄屏贴底（底部抽屉），sm 以上居中。
 * 正文区自己滚动，页头页脚固定——长列表弹窗（TMDB 候选）在手机上也点得到底部按钮。
 */
const props = withDefaults(
  defineProps<{
    title?: string
    description?: string
    /** 面板最大宽度，默认 560px */
    width?: string
    /** 点遮罩不关闭（表单填到一半的弹窗用） */
    persistent?: boolean
  }>(),
  { width: '560px' },
)
const open = defineModel<boolean>('show', { required: true })

const dialogStyle = computed(() => ({ maxWidth: props.width }))
</script>

<template>
  <DialogRoot v-model:open="open">
    <DialogPortal>
      <DialogOverlay class="modal__backdrop modal__backdrop--opaque h-modal-backdrop" />
      <DialogContent
        class="modal__container h-modal-container"
        data-placement="auto"
        v-bind="description ? {} : { 'aria-describedby': undefined }"
        @pointer-down-outside="(e) => persistent && e.preventDefault()"
        @interact-outside="(e) => persistent && e.preventDefault()"
      >
        <div class="modal__dialog modal__dialog--scroll-inside h-modal-dialog" data-placement="auto" :style="dialogStyle">
          <header v-if="title || $slots.header" class="modal__header h-modal-header">
            <slot name="header">
              <DialogTitle class="modal__heading h-modal-title">{{ title }}</DialogTitle>
            </slot>
            <DialogDescription v-if="description" class="h-modal-desc">{{ description }}</DialogDescription>
          </header>
          <DialogClose class="close-button modal__close-trigger" aria-label="关闭">
            <X :size="16" />
          </DialogClose>
          <div class="modal__body modal__body--scroll-inside h-modal-body"><slot /></div>
          <footer v-if="$slots.footer" class="modal__footer h-modal-footer"><slot name="footer" /></footer>
        </div>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>

<style scoped>
.h-modal-container {
  position: fixed;
  inset: 0;
  z-index: var(--z-index-overlay, 100000);
  margin: 0 auto;
  width: 100%;
  height: 100dvh;
}
.h-modal-dialog {
  gap: 16px;
  max-height: calc(100dvh - 32px);
}
.h-modal-header {
  gap: 4px;
  padding-right: 32px;
}
.h-modal-title {
  margin: 0;
  font-size: 16px;
  font-weight: 600;
}
.h-modal-desc {
  margin: 0;
  font-size: 13px;
  color: var(--muted);
}
.h-modal-body {
  color: var(--foreground);
}
.h-modal-footer {
  flex-wrap: wrap;
}

@media (min-width: 640px) {
  .h-modal-dialog {
    max-height: calc(100dvh - 80px);
  }
}

/* 手机：底部抽屉——去掉下方圆角并吃掉安全区，按钮不被 Home 条挡住 */
@media (max-width: 639px) {
  .h-modal-container {
    padding: 0;
    padding-top: 24px;
  }
  .h-modal-dialog {
    max-width: none !important;
    border-bottom-left-radius: 0;
    border-bottom-right-radius: 0;
    padding: 20px 16px calc(16px + env(safe-area-inset-bottom));
    max-height: calc(100dvh - 24px);
  }
}
</style>
