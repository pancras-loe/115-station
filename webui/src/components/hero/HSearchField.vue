<script setup lang="ts">
import { Search, X } from '@lucide/vue'

/**
 * 搜索框：HeroUI search-field 的结构，原生 <input type="search">，不需要 Reka。
 * 回车触发 search；清空按钮清完也触发一次，和原来 NInput 的 @clear 行为一致。
 */
withDefaults(defineProps<{ placeholder?: string; variant?: 'primary' | 'secondary' }>(), {
  variant: 'secondary',
})
const model = defineModel<string>({ default: '' })
const emit = defineEmits<{ search: [] }>()

function clear() {
  model.value = ''
  emit('search')
}
</script>

<template>
  <div class="search-field" :class="`search-field--${variant}`" :data-empty="!model || undefined">
    <div class="search-field__group h-search-group">
      <Search class="search-field__search-icon" data-slot="search-field-search-icon" />
      <input
        v-model="model"
        type="search"
        class="search-field__input h-search-input"
        data-slot="search-field-input"
        enterkeyhint="search"
        :placeholder="placeholder"
        @keydown.enter="emit('search')"
      />
      <button
        v-if="model"
        type="button"
        slot="clear"
        class="close-button search-field__clear-button"
        data-slot="search-field-clear-button"
        aria-label="清空"
        @click="clear"
      >
        <X data-slot="close-button-icon" />
      </button>
    </div>
  </div>
</template>

<style scoped>
.h-search-group {
  width: 100%;
}
.h-search-input {
  min-width: 0;
}
</style>
