<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import HChip from '@/components/hero/HChip.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import CopyBox from '@/components/ui/CopyBox.vue'
import { authApi } from '@/api'
import type { OtpSetup, OtpStatus } from '@/api/auth'
import { toastError, useFeedback } from '@/composables/useFeedback'

/**
 * 登录安全：二步验证（TOTP）的绑定与关闭，形态参考 MoviePilot 的「双重验证」。
 * 绑定 = 生成密钥 → 验证器扫码 → 回填一次验证码才生效；关闭要再输一次登录密码。
 * 丢了验证器只能靠容器环境变量 AUTH_OTP_RESET=true 重启关掉，网页上没有后门。
 */
const { message } = useFeedback()

const st = ref<OtpStatus | null>(null)
const setup = ref<OtpSetup | null>(null)
const code = ref('')
const password = ref('')
const busy = ref(false)
const disabling = ref(false)

async function load() {
  try {
    st.value = await authApi.otpStatus()
  } catch (e) {
    toastError(e, '读取二步验证状态失败')
  }
}
onMounted(load)

/** 令牌有效期按天 / 小时 / 分钟说人话 */
const expireText = computed(() => {
  const m = st.value?.token_expire ?? 0
  if (!m) return '—'
  if (m % 1440 === 0) return `${m / 1440} 天`
  if (m % 60 === 0) return `${m / 60} 小时`
  return `${m} 分钟`
})

async function start() {
  busy.value = true
  try {
    setup.value = await authApi.otpGenerate()
    code.value = ''
  } catch (e) {
    toastError(e, '生成二维码失败')
  } finally {
    busy.value = false
  }
}

async function confirm() {
  if (!/^\d{6}$/.test(code.value.trim())) {
    message.warning('请输入验证器上的 6 位验证码')
    return
  }
  busy.value = true
  try {
    await authApi.otpEnable(code.value.trim())
    message.success('二步验证已开启，下次登录需要输入验证码')
    setup.value = null
    code.value = ''
    await load()
  } catch (e) {
    toastError(e, '开启失败')
  } finally {
    busy.value = false
  }
}

async function disable() {
  if (!password.value) {
    message.warning('请输入登录密码')
    return
  }
  busy.value = true
  try {
    await authApi.otpDisable(password.value)
    message.success('二步验证已关闭')
    password.value = ''
    disabling.value = false
    await load()
  } catch (e) {
    toastError(e, '关闭失败')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <SectionCard title="二步验证" hint="登录时除了密码，还要输入手机身份验证器上的 6 位动态验证码">
    <template #extra>
      <HChip v-if="st" :color="st.enabled ? 'success' : 'default'" size="sm">{{ st.enabled ? '已开启' : '未开启' }}</HChip>
    </template>

    <!-- 已开启：只能关闭（要更换验证器就先关再重新绑定） -->
    <template v-if="st?.enabled">
      <p class="desc">登录需要验证码。要换手机 / 验证器，先关闭再重新绑定。</p>
      <template v-if="disabling">
        <FieldRow label="登录密码" hint="确认是你本人在操作">
          <HInput
            v-model="password"
            type="password"
            placeholder="输入登录密码"
            :input-attrs="{ name: 'password', autocomplete: 'current-password' }"
            @enter="disable"
          />
        </FieldRow>
        <div class="actions">
          <HButton variant="danger" :loading="busy" @click="disable">关闭二步验证</HButton>
          <HButton variant="tertiary" @click="(disabling = false), (password = '')">取消</HButton>
        </div>
      </template>
      <div v-else class="actions">
        <HButton variant="danger-soft" @click="disabling = true">关闭二步验证</HButton>
      </div>
    </template>

    <!-- 未开启：生成 → 扫码 → 回填验证码 -->
    <template v-else-if="st">
      <div v-if="!setup" class="actions">
        <HButton variant="primary" :loading="busy" @click="start">开启二步验证</HButton>
      </div>
      <div v-else class="setup">
        <img class="qr" :src="setup.qr" alt="二步验证二维码" width="180" height="180" />
        <div class="setup-side">
          <ol class="steps">
            <li>用 Google Authenticator、Microsoft Authenticator、1Password 等身份验证器扫描左侧二维码</li>
            <li>扫不了就手动添加，密钥（基于时间）：</li>
          </ol>
          <CopyBox :value="setup.secret" />
          <FieldRow label="验证码" hint="输入验证器上显示的 6 位数字，验证通过后才生效">
            <HInput
              v-model="code"
              mono
              placeholder="6 位数字"
              :input-attrs="{ name: 'otp-setup', autocomplete: 'one-time-code', inputmode: 'numeric', maxlength: '6' }"
              @enter="confirm"
            />
          </FieldRow>
          <div class="actions">
            <HButton variant="primary" :loading="busy" @click="confirm">验证并开启</HButton>
            <HButton variant="tertiary" @click="setup = null">取消</HButton>
          </div>
        </div>
      </div>
    </template>

    <HAlert status="default" title="丢了验证器怎么办">
      在 docker-compose 的环境变量里加 <code>AUTH_OTP_RESET=true</code> 重启一次即可关闭二步验证，登录后记得去掉这个变量。
    </HAlert>
  </SectionCard>

  <SectionCard title="登录有效期" hint="与 MoviePilot 一致：登录后令牌有效 8 天，到期需要重新登录">
    <FieldRow label="当前有效期">
      <span>{{ expireText }}</span>
    </FieldRow>
    <p class="desc">
      由环境变量 <code>ACCESS_TOKEN_EXPIRE_MINUTES</code>（分钟）控制，默认 11520（8 天）。改动只影响之后的登录，已发出的令牌按签发时的有效期过期。
    </p>
  </SectionCard>
</template>

<style scoped>
.desc {
  margin: 0;
  font-size: 13px;
  line-height: 1.6;
  color: var(--muted);
}
.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.setup {
  display: flex;
  gap: 20px;
  align-items: flex-start;
}
/* 二维码固定白底：深色模式下扫码器也认得出 */
.qr {
  flex-shrink: 0;
  padding: 8px;
  border-radius: var(--r-lg);
  background: #fff;
}
.setup-side {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.steps {
  margin: 0;
  padding-left: 18px;
  font-size: 13px;
  line-height: 1.7;
  color: color-mix(in oklab, var(--foreground) 80%, var(--muted));
}
code {
  font-family: var(--font-mono);
  font-size: 12px;
  padding: 1px 5px;
  border-radius: 4px;
  background: var(--default);
}
@media (max-width: 720px) {
  .setup {
    flex-direction: column;
    align-items: center;
  }
  .setup-side {
    width: 100%;
  }
}
</style>
