<script setup lang="ts">
import { computed } from 'vue'

/**
 * HeroUI 的 .button 类 + 原生 <button>。
 * 按下缩放、hover 只在有鼠标的设备上生效（@media (hover: hover)）都由它的 CSS 负责；
 * 这里只把 props 翻译成 BEM 修饰类，外加 loading 时的 data-pending。
 */
const props = withDefaults(
  defineProps<{
    variant?: 'primary' | 'secondary' | 'tertiary' | 'ghost' | 'outline' | 'danger' | 'danger-soft'
    size?: 'sm' | 'md' | 'lg'
    iconOnly?: boolean
    fullWidth?: boolean
    loading?: boolean
    disabled?: boolean
    type?: 'button' | 'submit' | 'reset'
  }>(),
  { variant: 'secondary', size: 'md', type: 'button' },
)

const cls = computed(() => [
  'button',
  `button--${props.variant}`,
  props.size !== 'md' && `button--${props.size}`,
  props.iconOnly && 'button--icon-only',
  props.fullWidth && 'button--full-width',
])
</script>

<template>
  <button
    :class="cls"
    :type="type"
    :disabled="disabled"
    :data-pending="loading || undefined"
    :aria-busy="loading || undefined"
  >
    <!-- loading 时用转圈顶替图标，而不是在文字旁多插一个：按钮宽度不跳 -->
    <svg v-if="loading" class="spinner spinner--sm spinner--current" data-slot="spinner" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <circle cx="12" cy="12" r="9" stroke="currentColor" stroke-opacity="0.25" stroke-width="3" />
      <path d="M21 12a9 9 0 0 0-9-9" stroke="currentColor" stroke-width="3" stroke-linecap="round" />
    </svg>
    <slot v-else name="icon" />
    <slot />
  </button>
</template>
