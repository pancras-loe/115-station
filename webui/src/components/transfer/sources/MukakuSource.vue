<script setup lang="ts">
import { onMounted, ref } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import SecretInput from '@/components/ui/SecretInput.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import LoginBadge from '@/components/transfer/LoginBadge.vue'
import { resourcesApi } from '@/api'
import { plainProps } from '@/utils/autofill'
import { toastError, useFeedback } from '@/composables/useFeedback'

const emit = defineEmits<{ changed: [] }>()
const { message } = useFeedback()

const baseUrl = ref('')
const token = ref('')
const hasToken = ref(false)
const tokenAt = ref('')
const savingBase = ref(false)

const login = ref({ username: '', password: '', code: '' })
const captcha = ref({ img: '', key: '' })
const logging = ref(false)

async function load() {
  try {
    const d = await resourcesApi.mkConfig()
    baseUrl.value = d.base_url ?? ''
    login.value.username = d.username ?? ''
    hasToken.value = !!d.has_token
    tokenAt.value = d.token_at ?? ''
  } catch {
    // 首次使用尚无配置
  }
}

async function saveBase() {
  savingBase.value = true
  try {
    await resourcesApi.mkSaveConfig({ base_url: baseUrl.value.trim() })
    message.success('保存成功')
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    savingBase.value = false
  }
}

async function saveToken() {
  const v = token.value.trim()
  try {
    await resourcesApi.mkSaveConfig({ token: v })
    token.value = ''
    message.success(v ? 'token 已保存' : 'token 已清除')
    await load()
    emit('changed')
  } catch (e) {
    toastError(e, 'token 保存失败')
  }
}

async function refreshCaptcha() {
  try {
    const d = await resourcesApi.mkCaptcha()
    captcha.value = { img: d.img, key: d.key || '' }
  } catch (e) {
    toastError(e, '验证码获取失败')
  }
}

async function doLogin() {
  if (!login.value.username || !login.value.password) {
    message.warning('请填写账号和密码')
    return
  }
  if (!captcha.value.key || !login.value.code) {
    message.warning('请先获取并输入图形验证码')
    return
  }
  logging.value = true
  try {
    await resourcesApi.mkLogin({ ...login.value, key: captcha.value.key })
    message.success('登录成功，token 已保存')
    login.value.code = ''
    captcha.value = { img: '', key: '' }
    await load()
    emit('changed')
  } catch (e) {
    toastError(e, '登录失败')
    refreshCaptcha() // 验证码一次性，失败后必须换新的
  } finally {
    logging.value = false
  }
}

onMounted(load)
</script>

<template>
  <FieldRow label="站点地址" tip="不太灵影视（bt0 系）镜像地址，默认 https://web5.mukaku.com，某域名失效时换 web1-web4。">
    <div class="h-field-row">
      <HInput v-model="baseUrl" placeholder="不太灵影视站点地址" :input-attrs="plainProps('mk-base-url')" />
      <HButton variant="primary" :loading="savingBase" @click="saveBase">保存</HButton>
    </div>
  </FieldRow>

  <FieldRow
    label="VIP Token"
    tip="资源仅 VIP 可见。浏览器登录后 F12 → Application → Local Storage → 复制 token 粘贴到这里。也可用下方验证码登录自动获取。"
  >
    <div class="token-row">
      <div class="h-field-row">
        <HInput
          v-model="token"
          :placeholder="hasToken ? '已保存（粘贴新值可覆盖）' : '粘贴浏览器 localStorage 里的 token'"
          :input-attrs="plainProps('mk-vip-token')"
        />
        <HButton variant="primary" @click="saveToken">保存</HButton>
      </div>
      <LoginBadge :on="hasToken" :sub="hasToken ? tokenAt : '资源需 VIP'" />
    </div>
  </FieldRow>

  <FieldRow
    label="验证码登录"
    tip="填写站内账号密码，点「获取验证码」输入图中字符后登录，token 自动保存。验证码有时效，过期就点图片刷新。"
  >
    <div class="login-row">
      <HInput v-model="login.username" placeholder="用户名 / 邮箱" class="w150" :input-attrs="plainProps('mk-account')" />
      <div class="w130"><SecretInput v-model="login.password" name="mk-secret" placeholder="密码" /></div>
      <HInput v-model="login.code" placeholder="验证码" class="w90" :input-attrs="plainProps('mk-captcha-code')" @enter="doLogin" />
      <img v-if="captcha.img" :src="captcha.img" class="captcha" alt="验证码" title="点击刷新" @click="refreshCaptcha" />
      <HButton variant="tertiary" @click="refreshCaptcha">获取验证码</HButton>
      <HButton variant="primary" :loading="logging" @click="doLogin">登录</HButton>
    </div>
  </FieldRow>
</template>

<style scoped>
.token-row,
.login-row {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}
.token-row {
  flex-direction: column;
  align-items: flex-start;
  gap: 7px;
}
.token-row > :first-child {
  width: 100%;
}
.w150 {
  width: 150px;
}
.w130 {
  width: 130px;
}
.w90 {
  width: 90px;
}
.captcha {
  height: 32px;
  border-radius: var(--r-sm);
  cursor: pointer;
}
</style>
