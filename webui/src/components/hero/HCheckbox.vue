<script setup lang="ts">
import { computed } from 'vue'
import { CheckboxIndicator, CheckboxRoot } from 'reka-ui'

/**
 * 复选框：Reka 的 CheckboxRoot 是那个方块按钮（空格切换、aria-checked），
 * HeroUI 的选中/半选样式挂在外层 .checkbox 的 data-selected / data-indeterminate 上，
 * 所以这两个属性由这里根据 props 直接设。
 */
const props = defineProps<{ checked?: boolean; indeterminate?: boolean; disabled?: boolean }>()
const emit = defineEmits<{ 'update:checked': [boolean] }>()

const state = computed(() => (props.indeterminate ? 'indeterminate' : !!props.checked))
</script>

<template>
  <label
    class="checkbox checkbox--secondary"
    :data-selected="(checked && !indeterminate) || undefined"
    :data-indeterminate="indeterminate || undefined"
    :data-disabled="disabled || undefined"
  >
    <span class="checkbox__content" data-slot="checkbox-content">
      <CheckboxRoot
        class="checkbox__control h-checkbox-control"
        :model-value="state"
        :disabled="disabled"
        @update:model-value="(v) => emit('update:checked', v === true)"
      >
        <CheckboxIndicator class="checkbox__indicator" force-mount>
          <svg v-if="indeterminate" viewBox="0 0 12 12" data-slot="checkbox-default-indicator--indeterminate" aria-hidden="true">
            <path d="M2.5 6h7" stroke="currentColor" stroke-width="2" stroke-linecap="round" />
          </svg>
          <svg
            v-else-if="checked"
            viewBox="0 0 17 18"
            fill="none"
            data-slot="checkbox-default-indicator--checkmark"
            aria-hidden="true"
          >
            <polyline
              points="1 9 7 14 15 4"
              stroke="currentColor"
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="3"
            />
          </svg>
        </CheckboxIndicator>
      </CheckboxRoot>
      <span v-if="$slots.default" data-slot="label" class="h-checkbox-label"><slot /></span>
    </span>
  </label>
</template>

<style scoped>
.h-checkbox-control {
  padding: 0;
}
.h-checkbox-label {
  font-weight: 400;
  color: var(--foreground);
}
</style>
