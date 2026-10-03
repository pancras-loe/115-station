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
import { copyText } from '@/utils/clipboard'

const emit = defineEmits<{ changed: [] }>()
const { message } = useFeedback()

const form = ref({ base_url: 'https://re0.me', client_id: '', client_secret: '' })
const authorized = ref(false)
const saving = ref(false)
const checking = ref(false)
const egress = ref<{ ip: string; ipv6: boolean; exact: boolean } | null>(null)
const egressLoading = ref(false)

async function load() {
  try {
    const d = await resourcesApi.re0Config()
    form.value = {
      base_url: d.base_url || 'https://re0.me',
      client_id: d.client_id ?? '',
      client_secret: d.client_secret ?? '',
    }
    authorized.value = !!d.authorized
  } catch {
    // 首次使用尚无配置
  }
}

async function save() {
  saving.value = true
  try {
    await resourcesApi.re0SaveConfig(form.value)
    message.success('保存成功')
    emit('changed')
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}

async function check() {
  checking.value = true
  try {
    const d = await resourcesApi.re0Check()
    authorized.value = d.authorized
    message[d.authorized ? 'success' : 'warning'](d.message || (d.authorized ? '授权有效' : '尚未授权'))
    emit('changed')
  } catch (e) {
    toastError(e, '状态检查失败')
  } finally {
    checking.value = false
  }
}

async function authorize() {
  try {
    await resourcesApi.re0SaveConfig(form.value)
    // 回调地址取浏览器当前访问的地址：回跳只经过这个浏览器，内网地址也能用
    const d = await resourcesApi.re0OAuthStart(`${location.origin}/api/re0/oauth/callback`)
    if (!d.authorize_url) throw new Error('未取得授权地址，请先保存应用配置')
    // OAuth 要跳到 RE0 官方页面确认，回调再跳回来
    window.open(d.authorize_url, '_blank', 'noopener')
    message.info('已打开 RE0 授权页，完成后回来点「检查状态」')
  } catch (e) {
    toastError(e, '发起授权失败')
  }
}

async function queryEgress() {
  egressLoading.value = true
  try {
    egress.value = await resourcesApi.re0EgressIP()
  } catch (e) {
    toastError(e, '查询出口 IP 失败')
  } finally {
    egressLoading.value = false
  }
}

async function copyEgress() {
  if (!egress.value) return
  if (await copyText(egress.value.ip)) message.success('已复制')
  else message.error('复制失败，请手动选中')
}

onMounted(load)
</script>

<template>
  <FieldRow
    label="应用配置"
    wide
    tip="RE0 官方 OpenAPI。需先在 re0.me「个人面板 → OPENAPI → 我的应用」创建应用并等站方审核通过，再把 client_id 和应用 Secret 填到这里。创建时「授权结果投递」选浏览器跳转，「固定 Redirect URI 白名单」留空。"
  >
    <div class="app-row">
      <HInput v-model="form.base_url" placeholder="站点地址" class="w180" :input-attrs="plainProps('re0-base-url')" />
      <HInput v-model="form.client_id" placeholder="client_id（app_xxx）" class="w200" :input-attrs="plainProps('re0-client-id')" />
      <div class="w200">
        <SecretInput
          v-model="form.client_secret"
          name="re0-client-secret"
          :reveal="{ key: 're0', field: 'client_secret' }"
          placeholder="应用 Secret"
        />
      </div>
    </div>
  </FieldRow>

  <FieldRow
    label="账号授权"
    tip="保存应用信息后点「授权」，跳转 RE0 官方授权页确认一次。授权后以你的身份查询 / 解锁资源（消耗站内积分，每次解锁前都会先确认），Token 自动续期。"
  >
    <div class="app-row">
      <HButton variant="primary" @click="authorize">授权 RE0 账号</HButton>
      <HButton variant="tertiary" :loading="checking" @click="check">检查状态</HButton>
      <LoginBadge :on="authorized" />
    </div>
  </FieldRow>

  <FieldRow
    label="出口 IP"
    tip="创建应用时「预期服务端出口 IP」填这个：本站访问 RE0 时对方看到的公网地址。家用宽带的 IP 重新拨号会变，变了要回 RE0 控制台改，否则请求会被拒。"
  >
    <div class="app-row">
      <HButton variant="tertiary" :loading="egressLoading" @click="queryEgress">查询</HButton>
      <template v-if="egress">
        <code class="ip">{{ egress.ip }}</code>
        <HButton variant="ghost" size="sm" @click="copyEgress">复制</HButton>
        <span v-if="egress.ipv6" class="hint">这是 IPv6 地址，访问 RE0 走的是 IPv6</span>
        <span v-if="!egress.exact" class="hint">站点未返回出口信息，此为通用查询结果，IPv4 / IPv6 可能与实际不同</span>
      </template>
    </div>
  </FieldRow>

  <FormActions>
    <HButton variant="primary" :loading="saving" @click="save">保存配置</HButton>
  </FormActions>
</template>

<style scoped>
.app-row {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}
.w180 {
  width: 180px;
}
.w200 {
  width: 200px;
}
.ip {
  font-family: var(--font-mono);
  user-select: all;
}
.hint {
  color: var(--muted);
  font-size: 12px;
}
</style>
