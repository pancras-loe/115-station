<script setup lang="ts">
import { SwitchRoot, SwitchThumb } from 'reka-ui'

/**
 * 开关：Reka 的 SwitchRoot（role=switch、空格切换）当 HeroUI 的轨道，SwitchThumb 当滑块。
 * HeroUI 的选中样式挂在外层 .switch 的 data-selected 上，由这里按 v-model 直接设。
 */
defineProps<{ disabled?: boolean; size?: 'sm' | 'md' | 'lg'; ariaLabel?: string }>()
const model = defineModel<boolean>({ default: false })
</script>

<template>
  <label
    class="switch"
    :class="`switch--${size ?? 'md'}`"
    :data-selected="model || undefined"
    :data-disabled="disabled || undefined"
  >
    <span class="switch__content" data-slot="switch-content">
      <SwitchRoot v-model="model" class="switch__control h-switch-control" :disabled="disabled" :aria-label="ariaLabel">
        <SwitchThumb class="switch__thumb" />
      </SwitchRoot>
      <span v-if="$slots.default" class="h-switch-label"><slot /></span>
    </span>
  </label>
</template>

<style scoped>
.switch {
  /* HeroUI 的 .switch 是 flex-col，放进 FieldRow 的控件列里会撑满整行，点空白处也会切换 */
  display: inline-flex;
}
.h-switch-control {
  padding: 0;
  border: 0;
}
.h-switch-control:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
}
.h-switch-label {
  font-weight: 400;
}
</style>
