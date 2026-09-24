<script setup lang="ts">
import { TagsInputInput, TagsInputItem, TagsInputItemDelete, TagsInputItemText, TagsInputRoot } from 'reka-ui'
import { X } from '@lucide/vue'

/**
 * 标签输入（替代 NDynamicTags）：Reka 的 TagsInput（回车/逗号添加、退格删最后一个、方向键在标签间移动）
 * + HeroUI 的 tag 与输入框外观。用于文件后缀这类「一组短字符串」。
 * 粘贴 ".mkv,.mp4 .ts" 这种一串也能一次拆开（delimiter 同时认逗号和空白）。
 */
withDefaults(defineProps<{ placeholder?: string; disabled?: boolean }>(), {
  placeholder: '输入后回车添加',
})
const model = defineModel<string[]>({ default: () => [] })
</script>

<template>
  <TagsInputRoot
    v-model="model"
    class="h-tags"
    :disabled="disabled"
    :delimiter="/[,，\s]+/"
    add-on-paste
    add-on-blur
  >
    <TagsInputItem v-for="t in model" :key="t" :value="t" class="tag tag--sm tag--surface h-tag">
      <TagsInputItemText />
      <TagsInputItemDelete class="tag__remove-button h-tag-del" :aria-label="`移除 ${t}`">
        <X />
      </TagsInputItemDelete>
    </TagsInputItem>
    <TagsInputInput class="h-tags-input" :placeholder="model.length ? '' : placeholder" />
  </TagsInputRoot>
</template>

<style scoped>
.h-tags {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  min-height: 36px;
  padding: 5px 6px;
  border-radius: var(--radius-field, 12px);
  background: var(--default);
  transition: background-color 150ms ease;
}
.h-tags:focus-within {
  box-shadow: 0 0 0 2px var(--focus);
}
.h-tag {
  font-family: var(--font-mono);
  box-shadow: var(--surface-shadow);
}
.h-tag[data-state='active'] {
  box-shadow: 0 0 0 2px var(--focus);
}
.h-tag-del {
  display: inline-grid;
  place-items: center;
  border: 0;
  background: none;
  padding: 0;
  color: var(--muted);
  cursor: pointer;
}
.h-tag-del:hover {
  color: var(--danger);
}
.h-tags-input {
  flex: 1;
  min-width: 90px;
  height: 24px;
  border: 0;
  outline: none;
  background: transparent;
  color: var(--field-foreground);
  font-size: 13px;
}
.h-tags-input::placeholder {
  color: var(--field-placeholder);
}
</style>
