<script setup lang="ts">
import { NInput } from 'naive-ui'
import { secretProps } from '@/utils/autofill'

/**
 * 密钥输入框。除了遮罩与「点击查看」，它的职责是**挡掉浏览器凭据自动填充** ——
 * 见 utils/autofill.ts 里对成因的说明。所有 API Key / Secret / token / 第三方
 * 站点密码都必须走这个组件，直接写 `<NInput type="password">` 会重新踩坑。
 *
 * 唯一的例外是登录页：那里需要自动填充。
 */
defineProps<{
  modelValue: string
  /** 表单字段名，必须全局唯一且不含 user/pass/email 字样，否则仍会被启发式命中 */
  name: string
  placeholder?: string
}>()
const emit = defineEmits<{ 'update:modelValue': [string] }>()
</script>

<template>
  <NInput
    :value="modelValue"
    type="password"
    show-password-on="click"
    :placeholder="placeholder"
    :input-props="secretProps(name)"
    @update:value="emit('update:modelValue', $event)"
  />
</template>
