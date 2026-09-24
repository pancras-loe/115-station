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
/* HeroUI 的 alert 默认是白底加阴影（给页面底色上用的）；我们的提示多半摆在白卡片里，
   换成对应状态的淡色底、去掉阴影，不然在卡片上几乎看不出来 */
.alert {
  box-shadow: none;
  border-radius: 18px;
}
.alert--default {
  background: var(--surface-secondary);
}
.alert--accent {
  background: var(--accent-soft);
}
.alert--success {
  background: var(--success-soft);
}
.alert--warning {
  background: var(--warning-soft);
}
.alert--danger {
  background: var(--danger-soft);
}
.alert__description {
  color: color-mix(in oklab, var(--foreground) 78%, var(--muted));
  line-height: 1.7;
}
.h-alert-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 10px;
}
</style>
