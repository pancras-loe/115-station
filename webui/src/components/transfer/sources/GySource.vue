<script setup lang="ts">
import { onMounted, ref } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import SecretInput from '@/components/ui/SecretInput.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import LoginBadge from '@/components/transfer/LoginBadge.vue'
import { resourcesApi } from '@/api'
import { plainProps } from '@/utils/autofill'
import { toastError, useFeedback } from '@/composables/useFeedback'

const emit = defineEmits<{ changed: [] }>()
const { message } = useFeedback()

const form = ref({ base_url: '', username: '', password: '' })
const loggedIn = ref(false)
const saving = ref(false)
const authing = ref(false)

async function load() {
  try {
    const d = await resourcesApi.gyConfig()
    form.value = { base_url: d.base_url ?? '', username: d.username ?? '', password: d.password ?? '' }
    loggedIn.value = !!d.logged_in
    // 登录态异步校准，不阻塞表单回填
    resourcesApi.gyCheck().then((r) => (loggedIn.value = r.logged_in)).catch(() => {})
  } catch {
    // 首次使用尚无配置
  }
}

async function save() {
  saving.value = true
  try {
    await resourcesApi.gySaveConfig(form.value)
    message.success('保存成功')
    emit('changed')
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}

async function auth() {
  authing.value = true
  try {
    if (loggedIn.value) {
      await resourcesApi.gyLogout()
      loggedIn.value = false
      message.success('已退出登录')
      return
    }
    await resourcesApi.gySaveConfig(form.value)
    const d = await resourcesApi.gyLogin()
    loggedIn.value = true
    message.success(d.message || '登录成功')
  } catch (e) {
    toastError(e, '登录失败')
    loggedIn.value = false
  } finally {
    authing.value = false
    emit('changed')
  }
}

onMounted(load)
</script>

<template>
  <FieldRow label="站点地址" tip="观影常更换域名。搜索失败时到站点首页看最新地址，改这里后保存并重新登录。">
    <HInput v-model="form.base_url" placeholder="观影站点地址" :input-attrs="plainProps('gy-base-url')" />
  </FieldRow>

  <FieldRow
    label="账号"
    tip="观影站内账号（搜索 / 详情需登录）。登录即测试账号有效性；服务端自动通过站点反爬验证并保持登录态，会话失效后自动重登。"
  >
    <div class="auth-row">
      <!-- 不用 .h-field-row：它只让第一个子项伸缩，SecretInput 的根是 width:100% 的输入框组，
           两个输入框会互相挤，其中一个被压成 0 宽（现场：只看得到账号框，没有密码框） -->
      <div class="cred-row">
        <HInput v-model="form.username" placeholder="用户名 / 邮箱" :input-attrs="plainProps('gy-account')" />
        <SecretInput v-model="form.password" name="gy-secret" :reveal="{ key: 'guanying', field: 'password' }" placeholder="密码" />
        <HButton :variant="loggedIn ? 'tertiary' : 'primary'" :loading="authing" @click="auth">
          {{ loggedIn ? '退出登录' : '登录' }}
        </HButton>
      </div>
      <LoginBadge :on="loggedIn" :sub="loggedIn ? form.username : ''" />
    </div>
  </FieldRow>

  <FormActions>
    <HButton variant="primary" :loading="saving" @click="save">保存配置</HButton>
  </FormActions>
</template>

<style scoped>
.auth-row {
  display: flex;
  flex-direction: column;
  gap: 7px;
}
.cred-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) auto;
  align-items: center;
  gap: 8px;
}
@media (max-width: 720px) {
  .cred-row {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
