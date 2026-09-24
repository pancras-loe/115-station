<script setup lang="ts">
import { ref } from 'vue'
import { PopoverContent, PopoverPortal, PopoverRoot, PopoverTrigger } from 'reka-ui'
import HButton from './HButton.vue'

/**
 * 操作前的确认气泡（替代 NPopconfirm）：Reka Popover 负责定位、焦点、Esc/点外面关闭，
 * HeroUI 的 .popover 负责外观。触屏上也能点开（不像 Tooltip 只认 hover）。
 * 默认插槽是触发按钮，#content 是说明文字。
 */
withDefaults(
  defineProps<{
    confirmText?: string
    cancelText?: string
    /** 危险操作：确认按钮用 danger 色 */
    danger?: boolean
    side?: 'top' | 'bottom' | 'left' | 'right'
    disabled?: boolean
  }>(),
  { confirmText: '确定', cancelText: '取消', side: 'bottom' },
)
const emit = defineEmits<{ confirm: [] }>()
const open = ref(false)

function ok() {
  open.value = false
  emit('confirm')
}
</script>

<template>
  <PopoverRoot v-model:open="open">
    <PopoverTrigger as-child :disabled="disabled"><slot /></PopoverTrigger>
    <PopoverPortal>
      <PopoverContent
        class="popover h-popover h-popconfirm"
        :side="side"
        align="end"
        :side-offset="8"
        :collision-padding="12"
      >
        <div class="h-popconfirm-body"><slot name="content" /></div>
        <div class="h-popconfirm-foot">
          <HButton size="sm" variant="tertiary" @click="open = false">{{ cancelText }}</HButton>
          <HButton size="sm" :variant="danger ? 'danger' : 'primary'" @click="ok">{{ confirmText }}</HButton>
        </div>
      </PopoverContent>
    </PopoverPortal>
  </PopoverRoot>
</template>

<!-- 不用 scoped：PopoverContent 外面还包着一层 Reka 的定位 wrapper，scoped 的属性落在 wrapper 上，选不中面板 -->
<style>
.h-popconfirm {
  width: min(320px, calc(100vw - 24px));
  padding: 16px;
}
.h-popconfirm-body {
  font-size: 13px;
  line-height: 1.6;
  color: var(--foreground);
}
.h-popconfirm-foot {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 14px;
}
</style>
