<script setup lang="ts">
import { computed, ref } from 'vue'
import HInput from '@/components/hero/HInput.vue'
import { Eye, EyeOff } from '@lucide/vue'
import { secretProps } from '@/utils/autofill'
import { configApi } from '@/api'
import { toastError } from '@/composables/useFeedback'

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
const props = defineProps<{
  modelValue: string
  /** 表单字段名，必须全局唯一且不含 user/pass/email 字样，否则仍会被启发式命中 */
  name: string
  placeholder?: string
  /** 校验失败时置 'error' */
  status?: 'error' | 'warning'
  /**
   * 后端对已存的密钥只回掩码 `••••`（见 routes.go 的 maskSensitiveJSON），光揭开遮罩看到的还是掩码。
   * 给了它，点「眼睛」时按需向 /config/secret 取明文。
   */
  reveal?: { key: string; field: string }
}>()
const emit = defineEmits<{ 'update:modelValue': [string] }>()

const revealed = ref(false)
/**
 * 取回的明文只在本组件里显示，不写回 v-model：写回会让表单变「已修改」，
 * 而掩码原样存回本来就等于不改。用户一动手输入，走的就是正常的 v-model。
 */
const plain = ref<string | null>(null)
const loading = ref(false)
const shown = computed(() =>
  revealed.value && plain.value !== null && props.modelValue === configApi.SECRET_MASK ? plain.value : props.modelValue,
)

async function toggle() {
  if (revealed.value) {
    revealed.value = false
    return
  }
  if (props.reveal && props.modelValue === configApi.SECRET_MASK && plain.value === null) {
    loading.value = true
    try {
      plain.value = await configApi.revealSecret(props.reveal.key, props.reveal.field)
    } catch (e) {
      toastError(e, '读取密钥失败')
      return
    } finally {
      loading.value = false
    }
  }
  revealed.value = true
}
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
    :model-value="shown"
    :type="type"
    :status="status"
    :class="{ masked }"
    :placeholder="placeholder"
    :input-attrs="secretProps(name)"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <template #suffix>
      <button type="button" class="eye" :disabled="loading" :aria-label="revealed ? '隐藏' : '显示'" @click="toggle">
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
