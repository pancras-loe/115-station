<script setup lang="ts">
/** 独立的转圈（替代单独使用的 NSpin）。颜色跟随文字色 */
withDefaults(defineProps<{ size?: 'sm' | 'md' | 'lg' }>(), { size: 'md' })
</script>

<template>
  <svg
    class="spinner spinner--current"
    :class="size !== 'md' && `spinner--${size}`"
    viewBox="0 0 24 24"
    fill="none"
    role="status"
    aria-label="加载中"
  >
    <circle cx="12" cy="12" r="9" stroke="currentColor" stroke-opacity="0.25" stroke-width="3" />
    <path d="M21 12a9 9 0 0 0-9-9" stroke="currentColor" stroke-width="3" stroke-linecap="round" />
  </svg>
</template>
