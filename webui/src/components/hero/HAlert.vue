<script setup lang="ts">
import { computed } from 'vue'
import { CircleAlert, CircleCheck, Info, TriangleAlert } from '@lucide/vue'

const props = withDefaults(
  defineProps<{
    status?: 'default' | 'accent' | 'success' | 'warning' | 'danger'
    title?: string
  }>(),
  { status: 'default' },
)

const icon = computed(
  () =>
    ({ success: CircleCheck, warning: TriangleAlert, danger: CircleAlert })[props.status as string] ?? Info,
)
</script>

<template>
  <div class="alert" :class="`alert--${status}`" role="alert">
    <div class="alert__indicator"><component :is="icon" :size="16" :stroke-width="2" /></div>
    <div class="alert__content">
      <div v-if="title" class="alert__title">{{ title }}</div>
      <div class="alert__description"><slot /></div>
      <div v-if="$slots.actions" class="h-alert-actions"><slot name="actions" /></div>
    </div>
  </div>
</template>

<style scoped>
.h-alert-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 10px;
}
</style>
