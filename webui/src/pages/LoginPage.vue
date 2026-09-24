<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NButton, NForm, NFormItem, NInput } from 'naive-ui'
import { KeyRound, UserRound } from '@lucide/vue'
import ThemeToggle from '@/components/ThemeToggle.vue'
import { useAuthStore } from '@/stores/auth'
import { toastError, useFeedback } from '@/composables/useFeedback'
import BrandMark from '@/components/BrandMark.vue'

const auth = useAuthStore()
const router = useRouter()
const route = useRoute()
const { message } = useFeedback()

const username = ref('')
const password = ref('')
const submitting = ref(false)

// 账号来源是容器环境变量 AUTH_USER / AUTH_PASSWORD（未配置时首启生成随机密码，
// 见容器日志）。网页注册功能已移除，所以「未初始化」只能给配置指引，不能给注册入口。
const notInitialized = computed(() => auth.initialized === false)

async function submit() {
  if (!username.value || !password.value) {
    message.warning('请输入账号和密码')
    return
  }
  submitting.value = true
  try {
    await auth.login(username.value, password.value)
    message.success('登录成功')
    const redirect = route.query.redirect
    router.replace(typeof redirect === 'string' ? redirect : '/')
  } catch (e) {
    toastError(e, '登录失败')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="auth">
    <div class="auth-bg" aria-hidden="true" />
    <div class="auth-toggle"><ThemeToggle /></div>

    <div class="auth-card">
      <div class="brand">
        <BrandMark :size="56" class="brand-mark" />
        <div class="brand-name">Strm<span>Station</span></div>
        <div class="brand-sub">网盘媒体库管理 · STRM 自动生成</div>
      </div>

      <div v-if="notInitialized" class="notice">
        <strong>尚未设置管理员账号</strong>
        <p>
          请在 docker-compose 中配置环境变量 <code>AUTH_USER</code> / <code>AUTH_PASSWORD</code>
          后重启容器；未配置时首次启动已生成随机账号，见容器日志。
        </p>
      </div>

      <NForm class="form" @submit.prevent="submit">
        <NFormItem label="账号" :show-feedback="false">
          <NInput
            v-model:value="username"
            placeholder="请输入账号"
            autocomplete="username"
            size="large"
            :input-props="{ name: 'username' }"
          >
            <template #prefix><UserRound :size="16" /></template>
          </NInput>
        </NFormItem>

        <NFormItem label="密码" :show-feedback="false">
          <NInput
            v-model:value="password"
            type="password"
            show-password-on="click"
            placeholder="请输入密码"
            autocomplete="current-password"
            size="large"
            :input-props="{ name: 'password' }"
            @keyup.enter="submit"
          >
            <template #prefix><KeyRound :size="16" /></template>
          </NInput>
        </NFormItem>

        <NButton
          type="primary"
          size="large"
          block
          attr-type="submit"
          :loading="submitting"
          class="submit"
        >
          登录
        </NButton>
      </NForm>
    </div>
  </div>
</template>

<style scoped>
.auth {
  position: relative;
  min-height: 100vh;
  display: grid;
  place-items: center;
  padding: 24px;
  overflow: hidden;
}

/* 背景光晕用主色的极低透明度做，暗色下自动跟着令牌变，不需要两套图 */
.auth-bg {
  position: absolute;
  inset: 0;
  background:
    radial-gradient(60rem 30rem at 15% -10%, color-mix(in srgb, var(--c-primary) 16%, transparent), transparent 70%),
    radial-gradient(50rem 28rem at 95% 110%, color-mix(in srgb, var(--c-primary) 12%, transparent), transparent 70%);
  pointer-events: none;
}

.auth-toggle {
  position: absolute;
  top: 20px;
  right: 20px;
}

.auth-card {
  position: relative;
  width: 100%;
  max-width: 400px;
  padding: 36px 32px 32px;
  background: var(--c-bg-elevated);
  border: 1px solid var(--c-border);
  border-radius: var(--r-xl);
  box-shadow: var(--shadow-lg);
}

.brand {
  display: flex;
  flex-direction: column;
  align-items: center;
  margin-bottom: 26px;
}
.brand-mark {
  border-radius: 15px;
  box-shadow: 0 10px 24px -8px color-mix(in srgb, var(--c-primary) 60%, transparent);
  margin-bottom: 14px;
}
.brand-name {
  font-size: 22px;
  font-weight: 700;
  letter-spacing: -0.03em;
  color: var(--c-text-1);
}
.brand-name span {
  color: var(--c-primary);
}
.brand-sub {
  margin-top: 5px;
  font-size: 12.5px;
  color: var(--c-text-3);
}

.notice {
  margin-bottom: 20px;
  padding: 12px 14px;
  border-radius: var(--radius);
  background: var(--c-warning-soft);
  border: 1px solid color-mix(in srgb, var(--c-warning) 30%, transparent);
  font-size: 12.5px;
  line-height: 1.6;
  color: var(--c-text-2);
}
.notice strong {
  display: block;
  margin-bottom: 4px;
  color: var(--c-text-1);
}
.notice p {
  margin: 0;
}
.notice code {
  font-family: var(--font-mono);
  font-size: 11.5px;
  padding: 1px 5px;
  border-radius: 4px;
  background: color-mix(in srgb, var(--c-warning) 16%, transparent);
}

.form :deep(.n-form-item) {
  margin-bottom: 16px;
}
.submit {
  margin-top: 8px;
}
</style>
