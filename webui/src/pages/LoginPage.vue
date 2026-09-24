<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import { Eye, EyeOff, KeyRound, UserRound } from '@lucide/vue'
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
const showPwd = ref(false)

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

      <!-- 登录页是全站唯一「要」浏览器自动填充的地方：name / autocomplete 按标准写，
           密码框用真正的 type=password（其余页面的密钥框刻意避开它，见 SecretInput） -->
      <form class="form" @submit.prevent="submit">
        <label class="field">
          <span class="field-label">账号</span>
          <HInput
            v-model="username"
            placeholder="请输入账号"
            :input-attrs="{ name: 'username', autocomplete: 'username' }"
          >
            <template #prefix><UserRound :size="16" /></template>
          </HInput>
        </label>

        <label class="field">
          <span class="field-label">密码</span>
          <HInput
            v-model="password"
            :type="showPwd ? 'text' : 'password'"
            placeholder="请输入密码"
            :input-attrs="{ name: 'password', autocomplete: 'current-password' }"
          >
            <template #prefix><KeyRound :size="16" /></template>
            <template #suffix>
              <button type="button" class="eye" :aria-label="showPwd ? '隐藏密码' : '显示密码'" @click="showPwd = !showPwd">
                <component :is="showPwd ? EyeOff : Eye" :size="16" />
              </button>
            </template>
          </HInput>
        </label>

        <HButton variant="primary" size="lg" full-width type="submit" :loading="submitting" class="submit">
          登录
        </HButton>
      </form>
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
    radial-gradient(60rem 30rem at 15% -10%, color-mix(in oklab, var(--accent) 16%, transparent), transparent 70%),
    radial-gradient(50rem 28rem at 95% 110%, color-mix(in oklab, var(--accent) 12%, transparent), transparent 70%);
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
  background: var(--surface);
  border-radius: var(--r-card);
  box-shadow: var(--overlay-shadow);
}

.brand {
  display: flex;
  flex-direction: column;
  align-items: center;
  margin-bottom: 26px;
}
.brand-mark {
  border-radius: 15px;
  box-shadow: 0 10px 24px -8px color-mix(in oklab, var(--accent) 60%, transparent);
  margin-bottom: 14px;
}
.brand-name {
  font-size: 22px;
  font-weight: 700;
  letter-spacing: -0.03em;
  color: var(--foreground);
}
.brand-name span {
  color: var(--accent);
}
.brand-sub {
  margin-top: 5px;
  font-size: 12.5px;
  color: var(--muted);
}

.notice {
  margin-bottom: 20px;
  padding: 12px 14px;
  border-radius: 16px;
  background: var(--warning-soft);
  font-size: 12.5px;
  line-height: 1.6;
  color: color-mix(in oklab, var(--foreground) 75%, var(--muted));
}
.notice strong {
  display: block;
  margin-bottom: 4px;
  color: var(--foreground);
}
.notice p {
  margin: 0;
}
.notice code {
  font-family: var(--font-mono);
  font-size: 11.5px;
  padding: 1px 5px;
  border-radius: 4px;
  background: color-mix(in oklab, var(--warning) 16%, transparent);
}

.form {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.field-label {
  font-size: 13px;
  font-weight: 500;
  color: color-mix(in oklab, var(--foreground) 80%, var(--muted));
}
.form :deep(.input-group) {
  min-height: 44px;
}
.eye {
  display: grid;
  place-items: center;
  padding: 0;
  border: 0;
  background: none;
  color: var(--muted);
  cursor: pointer;
}
.eye:hover {
  color: var(--accent);
}
.submit {
  margin-top: 8px;
}
/* 手机：卡片贴满宽度，少一圈留白 */
@media (max-width: 480px) {
  .auth {
    padding: 16px;
  }
  .auth-card {
    padding: 28px 20px 24px;
  }
}
</style>
