<script setup lang="ts">
import { computed } from 'vue'

/** HeroUI 的 .chip：小标签/计数。soft 是默认观感（淡底 + 同色字） */
const props = withDefaults(
  defineProps<{
    color?: 'default' | 'accent' | 'success' | 'warning' | 'danger'
    variant?: 'primary' | 'secondary' | 'tertiary' | 'soft'
    size?: 'sm' | 'md' | 'lg'
  }>(),
  { color: 'default', variant: 'soft', size: 'sm' },
)

const cls = computed(() => ['chip', `chip--${props.color}`, `chip--${props.variant}`, `chip--${props.size}`])
</script>

<template>
  <span :class="cls"><span class="chip__label"><slot /></span></span>
</template>
