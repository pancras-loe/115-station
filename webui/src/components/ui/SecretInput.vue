<script setup lang="ts">
import { computed, ref } from 'vue'
import HInput from '@/components/hero/HInput.vue'
import { Eye, EyeOff } from '@lucide/vue'
import { secretProps } from '@/utils/autofill'

/**
 * 密钥输入框。除了遮罩与「点击查看」，它的职责是**挡掉浏览器凭据自动填充** ——
 * 见 utils/autofill.ts 里对成因的说明。所有 API Key / Secret / token / 第三方
 * 站点密码都必须走这个组件，直接写 `<input type="password">` 会重新踩坑。
 *
 * 遮罩**不用 `type="password"`**：浏览器只要看见密码框，点一下就弹「本站已保存的
 * 账号密码」下拉——而这里要填的是第三方密钥，选中哪一条都是错的。autofill.ts 那套
 * 属性能挡住「自动填进去」，挡不住这个下拉，因为下拉是用户主动点出来的。
 * 改成普通 text + CSS 文本遮罩后，浏览器根本不把它当凭据字段，下拉和「是否保存密码」
 * 一起消失。老浏览器不支持 `-webkit-text-security` 时退回 `type="password"`
 * （宁可弹下拉，也不能把密钥明文摊在屏幕上）。
 *
 * 唯一的例外是登录页：那里需要自动填充。
 */
defineProps<{
  modelValue: string
  /** 表单字段名，必须全局唯一且不含 user/pass/email 字样，否则仍会被启发式命中 */
  name: string
  placeholder?: string
  /** 校验失败时置 'error' */
  status?: 'error' | 'warning'
}>()
const emit = defineEmits<{ 'update:modelValue': [string] }>()

const revealed = ref(false)
const input = ref<InstanceType<typeof HInput> | null>(null)
defineExpose({ focus: () => input.value?.focus() })
const cssMaskable =
  typeof CSS !== 'undefined' &&
  typeof CSS.supports === 'function' &&
  CSS.supports('-webkit-text-security', 'disc')

const type = computed(() => (cssMaskable || revealed.value ? 'text' : 'password'))
const masked = computed(() => cssMaskable && !revealed.value)
</script>

<template>
  <HInput
    ref="input"
    :model-value="modelValue"
    :type="type"
    :status="status"
    :class="{ masked }"
    :placeholder="placeholder"
    :input-attrs="secretProps(name)"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <template #suffix>
      <button type="button" class="eye" :aria-label="revealed ? '隐藏' : '显示'" @click="revealed = !revealed">
        <component :is="revealed ? EyeOff : Eye" :size="15" />
      </button>
    </template>
  </HInput>
</template>

<style scoped>
.masked :deep(input) {
  -webkit-text-security: disc;
}
.eye {
  display: grid;
  place-items: center;
  padding: 0;
  border: 0;
  background: none;
  cursor: pointer;
  color: var(--muted);
  transition: color 0.15s;
}
.eye:hover {
  color: var(--accent);
}
</style>
