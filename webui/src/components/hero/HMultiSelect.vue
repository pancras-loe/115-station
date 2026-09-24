<script setup lang="ts" generic="V extends string | number">
import { computed, ref } from 'vue'
import {
  ComboboxAnchor,
  ComboboxContent,
  ComboboxEmpty,
  ComboboxGroup,
  ComboboxInput,
  ComboboxItem,
  ComboboxItemIndicator,
  ComboboxLabel,
  ComboboxPortal,
  ComboboxRoot,
  ComboboxViewport,
} from 'reka-ui'
import { Check, ChevronDown, X } from '@lucide/vue'

/**
 * 多选 + 搜索（替代 NSelect multiple filterable）：Reka 的 Combobox 负责键盘与浮层，
 * 外观是 HeroUI 的 select 触发器 + list-box 选项。已选的值以 tag 形式排在输入框前面，点 × 移除。
 * 选项可以分组（{ type: 'group', label, children }），和原来 Naive 的写法一致。
 */
export interface MultiOption<T> {
  label: string
  value: T
}
export interface MultiGroup<T> {
  type: 'group'
  label: string
  key?: string
  children: MultiOption<T>[]
}

const props = withDefaults(
  defineProps<{
    options: (MultiOption<V> | MultiGroup<V>)[]
    placeholder?: string
    disabled?: boolean
    /** 允许添加列表里没有的值（原 NSelect 的 tag 模式）：输入后回车或点「添加」 */
    creatable?: boolean
  }>(),
  { placeholder: '不限' },
)
const model = defineModel<V[]>({ default: () => [] })

const search = ref('')
const open = ref(false)

const flat = computed(() =>
  props.options.flatMap((o) => ('type' in o && o.type === 'group' ? o.children : [o as MultiOption<V>])),
)
const labelOf = (v: V) => flat.value.find((o) => o.value === v)?.label ?? String(v)

/** 自己过滤（ignore-filter）：Reka 自带的按文本过滤不认分组标题，且对「中文（代码）」这种标签匹配不直观 */
const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  const hit = (o: MultiOption<V>) => !q || o.label.toLowerCase().includes(q) || String(o.value).toLowerCase().includes(q)
  return props.options
    .map((o) =>
      'type' in o && o.type === 'group'
        ? { ...o, children: o.children.filter(hit) }
        : hit(o as MultiOption<V>)
          ? o
          : null,
    )
    .filter((o): o is MultiOption<V> | MultiGroup<V> => !!o && (!('type' in o) || o.children.length > 0))
})

/** 可新建时：输入的内容不在已有选项里，就在列表顶上给一个「添加」项 */
const createCandidate = computed(() => {
  const q = search.value.trim()
  if (!props.creatable || !q) return null
  if (flat.value.some((o) => String(o.value) === q) || model.value.some((v) => String(v) === q)) return null
  return q as V
})

function remove(v: V) {
  model.value = model.value.filter((x) => x !== v)
}
</script>

<template>
  <ComboboxRoot
    v-model="model"
    v-model:open="open"
    multiple
    ignore-filter
    :disabled="disabled"
    :reset-search-term-on-select="false"
    class="h-ms"
  >
    <ComboboxAnchor class="select__trigger h-ms-anchor" :data-disabled="disabled || undefined">
      <span v-for="v in model" :key="String(v)" class="tag tag--sm tag--surface h-ms-tag">
        {{ labelOf(v) }}
        <button type="button" class="h-ms-del" :aria-label="`移除 ${labelOf(v)}`" @click.stop="remove(v)">
          <X :size="12" />
        </button>
      </span>
      <ComboboxInput
        v-model="search"
        class="h-ms-input"
        :placeholder="model.length ? '' : placeholder"
        @focus="open = true"
        @keydown.backspace="!search && model.length && remove(model[model.length - 1])"
      />
      <ChevronDown class="select__indicator" :size="16" :data-open="open || undefined" />
    </ComboboxAnchor>

    <ComboboxPortal>
      <ComboboxContent
        class="select__popover h-popover h-ms-pop"
        position="popper"
        :side-offset="6"
        :collision-padding="12"
      >
        <ComboboxViewport class="list-box" data-slot="list-box">
          <ComboboxItem
            v-if="createCandidate !== null"
            :value="createCandidate"
            class="list-box-item list-box-item--default"
            data-slot="list-box-item"
            @select="search = ''"
          >
            <span data-slot="label">添加「{{ createCandidate }}」</span>
          </ComboboxItem>
          <ComboboxEmpty v-if="createCandidate === null" class="h-ms-empty">没有匹配项</ComboboxEmpty>
          <template v-for="o in filtered" :key="'type' in o ? `g-${o.label}` : String(o.value)">
            <ComboboxGroup v-if="'type' in o && o.type === 'group'">
              <ComboboxLabel class="h-ms-group">{{ o.label }}</ComboboxLabel>
              <ComboboxItem
                v-for="c in o.children"
                :key="String(c.value)"
                :value="c.value"
                class="list-box-item list-box-item--default"
                data-slot="list-box-item"
              >
                <span data-slot="label">{{ c.label }}</span>
                <ComboboxItemIndicator class="list-box-item__indicator"><Check :size="14" :stroke-width="2.5" /></ComboboxItemIndicator>
              </ComboboxItem>
            </ComboboxGroup>
            <ComboboxItem
              v-else
              :value="(o as MultiOption<V>).value"
              class="list-box-item list-box-item--default"
              data-slot="list-box-item"
            >
              <span data-slot="label">{{ (o as MultiOption<V>).label }}</span>
              <ComboboxItemIndicator class="list-box-item__indicator"><Check :size="14" :stroke-width="2.5" /></ComboboxItemIndicator>
            </ComboboxItem>
          </template>
        </ComboboxViewport>
      </ComboboxContent>
    </ComboboxPortal>
  </ComboboxRoot>
</template>

<style scoped>
.h-ms {
  width: 100%;
}
.h-ms-anchor {
  width: 100%;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  padding-top: 5px;
  padding-bottom: 5px;
  cursor: text;
  background: var(--default);
  box-shadow: none;
}
.h-ms-tag {
  box-shadow: var(--surface-shadow);
}
.h-ms-del {
  display: inline-grid;
  place-items: center;
  padding: 0;
  border: 0;
  background: none;
  color: var(--muted);
  cursor: pointer;
}
.h-ms-del:hover {
  color: var(--danger);
}
.h-ms-input {
  flex: 1;
  min-width: 80px;
  height: 24px;
  border: 0;
  outline: none;
  background: transparent;
  color: var(--field-foreground);
  font-size: 13px;
}
.h-ms-input::placeholder {
  color: var(--field-placeholder);
}
</style>

<!-- 浮层在 Portal 里，scoped 选不中 -->
<style>
.h-ms-pop {
  overflow-y: auto;
  min-width: var(--reka-combobox-trigger-width);
  max-height: min(320px, var(--reka-combobox-content-available-height, 320px));
  transform-origin: var(--reka-combobox-content-transform-origin);
}
.h-ms-group {
  padding: 8px 10px 4px;
  font-size: 11.5px;
  font-weight: 600;
  color: var(--muted);
}
.h-ms-empty {
  padding: 16px;
  text-align: center;
  font-size: 12.5px;
  color: var(--muted);
}
</style>
