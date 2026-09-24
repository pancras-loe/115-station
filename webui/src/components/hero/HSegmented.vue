<script setup lang="ts" generic="V extends string | number | boolean">
import { computed } from 'vue'
import { RadioGroupItem, RadioGroupRoot } from 'reka-ui'

/**
 * 分段单选（替代 NRadioGroup + NRadioButton）。
 * 语义是 radiogroup（Reka：方向键切换、Tab 只进一次），外观借 HeroUI 页签的分段胶囊：
 * 灰色底槽里，选中那格浮起一块白底。这是 HeroUI v3 里「几个互斥选项」最常见的样子。
 *
 * 选项多、文字长时在手机上横向滚动，而不是折成两行。
 */
export interface SegmentOption<T> {
  label: string
  value: T
  disabled?: boolean
}

const props = withDefaults(
  defineProps<{
    options: SegmentOption<V>[]
    disabled?: boolean
    size?: 'sm' | 'md'
    ariaLabel?: string
    /** 撑满父容器宽度，各格等分 */
    block?: boolean
  }>(),
  { size: 'md' },
)
const model = defineModel<V>()

/** Reka 的 RadioGroup 值是字符串；布尔、数字值在这里来回转（后端有存 'true' 字符串也有存布尔的） */
const inner = computed({
  get: () => (model.value === undefined ? undefined : String(model.value)),
  set: (v) => {
    const hit = props.options.find((o) => String(o.value) === v)
    if (hit) model.value = hit.value
  },
})
</script>

<template>
  <div class="tabs__list-container h-seg-wrap" :class="{ 'h-seg-block': block }">
    <RadioGroupRoot
      v-model="inner"
      class="tabs__list h-seg"
      :class="`h-seg--${size}`"
      orientation="horizontal"
      :disabled="disabled"
      :aria-label="ariaLabel"
    >
      <RadioGroupItem
        v-for="o in options"
        :key="String(o.value)"
        :value="String(o.value)"
        :disabled="o.disabled"
        class="tabs__tab h-seg-item"
        :data-selected="String(o.value) === inner || undefined"
      >
        {{ o.label }}
      </RadioGroupItem>
    </RadioGroupRoot>
  </div>
</template>

<style scoped>
.h-seg-wrap {
  display: inline-block;
  max-width: 100%;
  overflow-x: auto;
  scrollbar-width: none;
  vertical-align: middle;
}
.h-seg-wrap::-webkit-scrollbar {
  display: none;
}
.h-seg-block {
  display: block;
}
.h-seg {
  width: max-content;
  min-width: 100%;
}
.h-seg-item {
  width: auto;
  flex: 1 0 auto;
  white-space: nowrap;
  border: 0;
  background: transparent;
}
.h-seg-item[data-selected] {
  background: var(--segment);
  box-shadow: var(--surface-shadow);
}
.h-seg--sm .h-seg-item {
  height: 28px;
  padding: 0 12px;
  font-size: 12.5px;
}
</style>
