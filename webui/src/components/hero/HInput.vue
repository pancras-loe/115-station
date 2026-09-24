<script setup lang="ts">
/**
 * 单行输入框：HeroUI 的 input-group（可带前后缀），原生 <input>，不需要 Reka。
 * 默认 secondary（灰底）：表单几乎都在白卡片里，primary 的白底加阴影放上去会和卡片糊成一片。
 *
 * API Key / Secret 不要用它，走 SecretInput（避免浏览器把管理员密码自动填进来，见 utils/autofill.ts）。
 */
withDefaults(
  defineProps<{
    placeholder?: string
    readonly?: boolean
    disabled?: boolean
    type?: 'text' | 'url' | 'number' | 'search'
    variant?: 'primary' | 'secondary'
    /** 等宽字体：路径、cid、cron 这类要逐字核对的值 */
    mono?: boolean
  }>(),
  { type: 'text', variant: 'secondary' },
)
const model = defineModel<string>({ default: '' })
const emit = defineEmits<{ enter: [] }>()
</script>

<template>
  <div
    class="input-group input-group--full-width"
    :class="[`input-group--${variant}`, { 'h-input-readonly': readonly }]"
    :data-disabled="disabled || undefined"
  >
    <span v-if="$slots.prefix" class="input-group__prefix" data-slot="input-group-prefix"><slot name="prefix" /></span>
    <input
      v-model="model"
      class="input-group__input"
      :class="{ 'h-input-mono': mono }"
      data-slot="input-group-input"
      :type="type"
      :placeholder="placeholder"
      :readonly="readonly"
      :disabled="disabled"
      @keydown.enter="emit('enter')"
    />
    <span v-if="$slots.suffix" class="input-group__suffix" data-slot="input-group-suffix"><slot name="suffix" /></span>
  </div>
</template>

<style scoped>
.input-group {
  width: 100%;
}
.input-group__input {
  min-width: 0;
}
/* 只读值（从别处统一配置带过来的）：去掉可编辑的暗示，但保留可选中复制 */
.h-input-readonly {
  background: transparent;
  box-shadow: inset 0 0 0 1px var(--border);
}
.h-input-readonly .input-group__input {
  color: var(--muted);
  cursor: default;
}
.h-input-mono {
  font-family: var(--font-mono);
  font-size: 13px;
}
</style>
