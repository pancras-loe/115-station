<script setup lang="ts" generic="V extends string | number">
import { computed } from 'vue'
import {
  SelectContent,
  SelectIcon,
  SelectItem,
  SelectItemIndicator,
  SelectItemText,
  SelectPortal,
  SelectRoot,
  SelectTrigger,
  SelectValue,
  SelectViewport,
} from 'reka-ui'
import { Check, ChevronDown } from '@lucide/vue'

/**
 * 下拉选择：Reka 的 Select（键盘上下、首字母跳转、弹层定位）+ HeroUI 的 select / list-box 外观。
 * 默认 secondary 变体（灰底无阴影）：我们的表单几乎都摆在白色卡片里，
 * HeroUI 的 primary 是白底 + 阴影，放在白卡片上只剩一圈影子。
 */
export interface SelectOption<T> {
  label: string
  value: T
  disabled?: boolean
}

const props = withDefaults(
  defineProps<{
    options: SelectOption<V>[]
    placeholder?: string
    disabled?: boolean
    variant?: 'primary' | 'secondary'
    ariaLabel?: string
  }>(),
  { variant: 'secondary' },
)
const model = defineModel<V>()

/**
 * Reka 的 Select 值只认字符串，而且空串被它保留为「未选择」（选中空串那项会显示成占位符）。
 * 我们的选项里「不限」常用 '' 表示，所以对内统一编码一遍：数字转字符串，空串换成哨兵。
 */
const EMPTY = '__h_empty__'
const enc = (v: V | undefined) => (v === undefined ? undefined : String(v) === '' ? EMPTY : String(v))
const inner = computed({
  get: () => enc(model.value),
  set: (v) => {
    const hit = props.options.find((o) => enc(o.value) === v)
    if (hit) model.value = hit.value
  },
})
</script>

<template>
  <SelectRoot v-model="inner" :disabled="disabled">
    <div class="select" :class="`select--${variant}`">
      <SelectTrigger class="select__trigger h-select-trigger" :aria-label="ariaLabel">
        <SelectValue class="select__value" :placeholder="placeholder" />
        <SelectIcon as-child>
          <ChevronDown class="select__indicator" :size="16" data-slot="select-default-indicator" />
        </SelectIcon>
      </SelectTrigger>
    </div>
    <SelectPortal>
      <SelectContent
        class="select__popover h-popover"
        position="popper"
        :side-offset="6"
        :collision-padding="12"
      >
        <SelectViewport class="list-box" data-slot="list-box">
          <SelectItem
            v-for="o in options"
            :key="enc(o.value)"
            :value="enc(o.value)!"
            :disabled="o.disabled"
            class="list-box-item list-box-item--default"
            data-slot="list-box-item"
          >
            <SelectItemText data-slot="label">{{ o.label }}</SelectItemText>
            <SelectItemIndicator class="list-box-item__indicator" data-slot="list-box-item-indicator">
              <Check :size="14" :stroke-width="2.5" />
            </SelectItemIndicator>
          </SelectItem>
        </SelectViewport>
      </SelectContent>
    </SelectPortal>
  </SelectRoot>
</template>

<style scoped>
.h-select-trigger {
  width: 100%;
  align-items: center;
  gap: 6px;
}
</style>
