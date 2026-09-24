<script setup lang="ts">
import { ref } from 'vue'

/**
 * 单行 / 多行输入框：HeroUI 的 input-group（可带前后缀），原生 <input>/<textarea>，不需要 Reka。
 * 默认 secondary（灰底）：表单几乎都在白卡片里，primary 的白底加阴影放上去会和卡片糊成一片。
 *
 * API Key / Secret 不要直接用它，走 SecretInput（它在这个组件上加了防自动填充的属性，见 utils/autofill.ts）。
 */
const props = withDefaults(
  defineProps<{
    placeholder?: string
    readonly?: boolean
    disabled?: boolean
    type?: 'text' | 'url' | 'number' | 'search' | 'password'
    variant?: 'primary' | 'secondary'
    /** 等宽字体：路径、cid、cron 这类要逐字核对的值 */
    mono?: boolean
    /** 右侧转圈（后台在解析 / 校验输入） */
    loading?: boolean
    status?: 'error' | 'warning'
    /** 多行：给出行数就渲染 textarea（可拖高） */
    rows?: number
    /** 透传到原生 input 上的属性（name / autocomplete / data-* …） */
    inputAttrs?: Record<string, string | boolean | undefined>
    /** 出一个清空按钮 */
    clearable?: boolean
  }>(),
  { type: 'text', variant: 'secondary' },
)
const model = defineModel<string>({ default: '' })
const emit = defineEmits<{ enter: []; clear: []; blur: [] }>()

const el = ref<HTMLInputElement | HTMLTextAreaElement | null>(null)
/** el：原生 input / textarea，调用方要读写光标位置时用（如重命名模板的「插入变量」） */
defineExpose({ focus: () => el.value?.focus(), el })

function onEnter(e: KeyboardEvent) {
  // 输入法组字时的回车是选词，不是提交
  if (e.isComposing || props.rows) return
  emit('enter')
}
function clear() {
  model.value = ''
  emit('clear')
  el.value?.focus()
}
</script>

<template>
  <div
    class="input-group input-group--full-width"
    :class="[`input-group--${variant}`, { 'h-input-readonly': readonly, 'h-input-multi': rows }]"
    :data-disabled="disabled || undefined"
    :data-invalid="status === 'error' || undefined"
  >
    <span v-if="$slots.prefix" class="input-group__prefix h-input-affix" data-slot="input-group-prefix">
      <slot name="prefix" />
    </span>
    <textarea
      v-if="rows"
      ref="el"
      v-model="model"
      class="input-group__input h-input-el"
      :class="{ 'h-input-mono': mono }"
      data-slot="input-group-textarea"
      :rows="rows"
      :placeholder="placeholder"
      :readonly="readonly"
      :disabled="disabled"
      v-bind="inputAttrs"
      @blur="emit('blur')"
    />
    <input
      v-else
      ref="el"
      v-model="model"
      class="input-group__input h-input-el"
      :class="{ 'h-input-mono': mono }"
      data-slot="input-group-input"
      :type="type"
      :placeholder="placeholder"
      :readonly="readonly"
      :disabled="disabled"
      v-bind="inputAttrs"
      @keydown.enter="onEnter"
      @blur="emit('blur')"
    />
    <span
      v-if="$slots.suffix || loading || (clearable && model)"
      class="input-group__suffix h-input-affix"
      data-slot="input-group-suffix"
    >
      <svg v-if="loading" class="spinner spinner--sm spinner--current" viewBox="0 0 24 24" fill="none" aria-hidden="true">
        <circle cx="12" cy="12" r="9" stroke="currentColor" stroke-opacity="0.25" stroke-width="3" />
        <path d="M21 12a9 9 0 0 0-9-9" stroke="currentColor" stroke-width="3" stroke-linecap="round" />
      </svg>
      <button v-if="clearable && model" type="button" class="h-input-clear" aria-label="清空" @click="clear">
        <svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true">
          <path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" />
        </svg>
      </button>
      <slot name="suffix" />
    </span>
  </div>
</template>

<style scoped>
.input-group {
  width: 100%;
}
.h-input-el {
  min-width: 0;
}
/* 前后缀在 HeroUI 里画了分隔竖线（给「https://」这类文字前缀用的）；
   我们的前后缀多是图标 / 单位 / 转圈，去掉竖线，贴着输入区 */
.h-input-affix {
  border: 0;
  gap: 6px;
  padding: 0 10px;
}
.h-input-multi {
  align-items: stretch;
}
.h-input-multi .h-input-el {
  resize: vertical;
  line-height: 1.6;
}
/* 只读值（从别处统一配置带过来的）：去掉可编辑的暗示，但保留可选中复制 */
.h-input-readonly {
  background: transparent;
  box-shadow: inset 0 0 0 1px var(--border);
}
.h-input-readonly .h-input-el {
  color: var(--muted);
  cursor: default;
}
.h-input-mono {
  font-family: var(--font-mono);
  font-size: 13px;
}
.h-input-clear {
  display: grid;
  place-items: center;
  width: 20px;
  height: 20px;
  padding: 0;
  border: 0;
  border-radius: 999px;
  background: color-mix(in oklab, var(--muted) 25%, transparent);
  color: var(--surface);
  cursor: pointer;
}
.h-input-clear:hover {
  background: var(--muted);
}
</style>
