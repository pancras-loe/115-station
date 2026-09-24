<script setup lang="ts">
import { NumberFieldDecrement, NumberFieldIncrement, NumberFieldInput, NumberFieldRoot } from 'reka-ui'
import { Minus, Plus } from '@lucide/vue'

/**
 * 数字输入（替代 NInputNumber）：Reka 的 NumberField（上下键、滚轮、边界夹取）+ HeroUI number-field 外观。
 * 两侧 −/+ 在手机上比敲键盘好用；#suffix 放单位（秒、MB …）。
 * 清空时 v-model 是 null，和原来 NInputNumber 的行为一致，调用方已经按 `?? 0` 处理。
 */
withDefaults(
  defineProps<{
    min?: number
    max?: number
    step?: number
    disabled?: boolean
    placeholder?: string
    variant?: 'primary' | 'secondary'
    ariaLabel?: string
  }>(),
  { step: 1, variant: 'secondary' },
)
const model = defineModel<number | null>({ default: null })

</script>

<template>
  <NumberFieldRoot
    v-model="model"
    class="number-field"
    :class="`number-field--${variant}`"
    :min="min"
    :max="max"
    :step="step"
    :disabled="disabled"
    :format-options="{ useGrouping: false }"
    locale="zh-CN"
  >
    <div class="number-field__group h-num-group">
      <NumberFieldDecrement slot="decrement" class="number-field__decrement-button" aria-label="减少">
        <Minus :size="14" data-slot="number-field-decrement-button-icon" />
      </NumberFieldDecrement>
      <div class="h-num-mid">
        <NumberFieldInput
          class="number-field__input h-num-input"
          data-slot="number-field-input"
          :placeholder="placeholder"
          :aria-label="ariaLabel"
        />
        <span v-if="$slots.suffix" class="h-num-suffix"><slot name="suffix" /></span>
      </div>
      <NumberFieldIncrement slot="increment" class="number-field__increment-button" aria-label="增加">
        <Plus :size="14" data-slot="number-field-increment-button-icon" />
      </NumberFieldIncrement>
    </div>
  </NumberFieldRoot>
</template>

<style scoped>
.number-field {
  display: inline-flex;
  width: 180px;
  max-width: 100%;
}
.h-num-group {
  width: 100%;
}
.h-num-mid {
  display: flex;
  align-items: center;
  min-width: 0;
  height: 100%;
}
.h-num-input {
  flex: 1;
  width: 100%;
  text-align: center;
}
.h-num-suffix {
  flex: none;
  padding-right: 10px;
  font-size: 12.5px;
  color: var(--field-placeholder);
}
</style>
