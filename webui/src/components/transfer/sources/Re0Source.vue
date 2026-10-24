<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import HAlert from '@/components/hero/HAlert.vue'
import SecretInput from '@/components/ui/SecretInput.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import ShareOutRow from './ShareOutRow.vue'
import AutoUnlockRows from './AutoUnlockRows.vue'
import FormActions from '@/components/ui/FormActions.vue'
import LoginBadge from '@/components/transfer/LoginBadge.vue'
import { resourcesApi } from '@/api'
import type { Re0BrowserState, Re0CheckResult, Re0Checkin, Re0OpState } from '@/api/resources'
import { plainProps } from '@/utils/autofill'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { copyText } from '@/utils/clipboard'

const emit = defineEmits<{ changed: [] }>()
const { message, dialog } = useFeedback()
const shareOutRow = ref<InstanceType<typeof ShareOutRow> | null>(null)

const form = ref({
  base_url: 'https://re0.me',
  client_id: '',
  client_secret: '',
  username: '',
  password: '',
  use_proxy: false,
})
const channel = ref<'' | 'openapi' | 'web'>('')
const authorized = ref(false)
const authorizedAs = ref('')
// 授权里缺的权限：2026-10 之前授权的没有 meta（检查状态）/ write（签到），要重新授权一次
const missingScopes = ref<string[]>([])
const account = ref<Re0CheckResult | null>(null)
const checkin = ref<Re0Checkin | null>(null)
const checkinSaving = ref(false)
const checkinRunning = ref(false)
const saving = ref(false)
const checking = ref(false)
const egress = ref<{ ip: string; ipv6: boolean; exact: boolean } | null>(null)
const egressLoading = ref(false)
// OpenAPI 已停止申请：没配过的折叠起来，配过 / 授权过的老用户默认展开
const showOpenapi = ref(false)

const browser = ref<Re0BrowserState | null>(null)
const browserBusy = ref(false)
let pollTimer: ReturnType<typeof setTimeout> | undefined

// 「检查状态」「立即签到」要开浏览器、过验证、登录，可能要一两分钟：等的时候每秒读一次后端走到哪一步
const op = ref<Re0OpState | null>(null)
const opNow = ref(Date.now())
let opTimer: ReturnType<typeof setInterval> | undefined
const opText = computed(() => {
  if (!op.value) return '等待开始…'
  const secs = Math.max(0, Math.round(opNow.value / 1000 - op.value.since))
  return `${op.value.step}（${op.value.what}，已用 ${secs} 秒）`
})

function startOpPoll() {
  stopOpPoll()
  op.value = null
  const tick = async () => {
    opNow.value = Date.now()
    try {
      const d = await resourcesApi.re0Browser()
      op.value = d.op ?? op.value
      browser.value = d.browser
    } catch {
      // 读不到进度不影响操作本身
    }
  }
  void tick()
  opTimer = setInterval(tick, 1000)
}

function stopOpPoll() {
  clearInterval(opTimer)
  opTimer = undefined
  op.value = null
}

const downloading = computed(() => ['downloading', 'verifying', 'extracting'].includes(browser.value?.state ?? ''))
const pct = computed(() => {
  const b = browser.value
  return b && b.total > 0 ? Math.min(100, Math.floor((b.done / b.total) * 100)) : 0
})

function mb(n: number) {
  return `${(n / 1024 / 1024).toFixed(n >= 100 * 1024 * 1024 ? 0 : 1)}MB`
}

const browserText = computed(() => {
  const b = browser.value
  if (!b) return ''
  switch (b.state) {
    case 'ready':
      return `已就绪（CloakBrowser ${b.version}）`
    case 'missing':
      return '还没下载'
    case 'downloading':
      return b.total > 0
        ? `下载中 ${pct.value}%（${mb(b.done)} / ${mb(b.total)}，${mb(b.speed)}/s）`
        : '准备下载…'
    case 'verifying':
      return '校验中…'
    case 'extracting':
      return '解压中…'
    case 'error':
      return '下载失败'
    case 'unsupported':
      return '这个平台用不了'
  }
  return b.state
})

async function load() {
  try {
    const d = await resourcesApi.re0Config()
    form.value = {
      base_url: d.base_url || 'https://re0.me',
      client_id: d.client_id ?? '',
      client_secret: d.client_secret ?? '',
      username: d.username ?? '',
      password: d.password ?? '',
      use_proxy: !!d.use_proxy,
    }
    channel.value = d.channel ?? ''
    authorized.value = !!d.authorized
    authorizedAs.value = d.authorized_as ?? ''
    missingScopes.value = d.missing_scopes ?? []
    showOpenapi.value = !!(d.client_id || d.authorized)
    savedForm = JSON.stringify(form.value)
  } catch {
    // 首次使用尚无配置
  }
  await Promise.all([loadCheckin(), loadBrowser()])
}

async function loadBrowser() {
  clearTimeout(pollTimer)
  try {
    const d = await resourcesApi.re0Browser()
    browser.value = d.browser
  } catch {
    browser.value = null
  }
  // 下载中每 1.5 秒刷一次进度
  if (downloading.value) pollTimer = setTimeout(loadBrowser, 1500)
}

async function downloadBrowser(force: boolean) {
  if (force) {
    const ok = await dialog.confirm({
      title: '重新下载浏览器？',
      content: '会删掉本地的浏览器重新下载（约 210MB）。只在浏览器打不开、怀疑文件损坏时用。',
      actions: [
        { label: '取消', value: false, variant: 'tertiary' },
        { label: '重新下载', value: true, variant: 'danger' },
      ],
    })
    if (!ok) return
  }
  browserBusy.value = true
  try {
    const d = await resourcesApi.re0BrowserDownload(force)
    browser.value = d.browser
    message.info(d.message)
  } catch (e) {
    toastError(e, '下载失败')
  } finally {
    browserBusy.value = false
    await loadBrowser()
  }
}

async function loadCheckin() {
  try {
    checkin.value = await resourcesApi.re0Checkin()
  } catch {
    checkin.value = null
  }
}

async function toggleCheckin(on: boolean) {
  if (!checkin.value) return
  checkinSaving.value = true
  try {
    await resourcesApi.re0SaveCheckin(on)
    checkin.value.enabled = on
    message.success(on ? `已开启：每天 ${checkin.value.hour}:00 后自动签到` : '已关闭自动签到')
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    checkinSaving.value = false
  }
}

async function runCheckin() {
  checkinRunning.value = true
  startOpPoll()
  try {
    const d = await resourcesApi.re0RunCheckin()
    message.success(d.message)
  } catch (e) {
    toastError(e, '签到失败')
  } finally {
    stopOpPoll()
    checkinRunning.value = false
    await loadCheckin()
  }
}

async function save() {
  saving.value = true
  try {
    const d = await resourcesApi.re0SaveConfig(form.value)
    savedForm = JSON.stringify(form.value)
    message.success(d?.message || '保存成功')
    emit('changed')
    await load()
    shareOutRow.value?.reload() // 能不能发取决于刚存的账号配置
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}

// 上次保存 / 读到的表单：「检查状态」查的是后端已保存的配置，改了没保存就先存，免得查的是旧配置
let savedForm = ''

async function check() {
  checking.value = true
  try {
    if (JSON.stringify(form.value) !== savedForm) {
      if (!form.value.username.trim() && !form.value.client_id.trim()) {
        message.warning('请先填写 RE0 账号和密码')
        return
      }
      await resourcesApi.re0SaveConfig(form.value)
      savedForm = JSON.stringify(form.value)
      message.info('配置有改动，已先保存')
      emit('changed')
      void loadBrowser()
    }
    if (channel.value !== 'openapi') startOpPoll()
    const d = await resourcesApi.re0Check()
    authorized.value = d.authorized
    account.value = d.authorized && d.channel !== 'web' ? d : null
    missingScopes.value = d.missing_scopes ?? []
    if (d.user && d.channel !== 'web') authorizedAs.value = d.user
    message[d.authorized && !missingScopes.value.length ? 'success' : 'warning'](
      d.message || (d.authorized ? '授权有效' : '尚未授权'),
    )
    await loadCheckin()
    emit('changed')
  } catch (e) {
    toastError(e, '检查失败')
  } finally {
    stopOpPoll()
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
onBeforeUnmount(() => {
  clearTimeout(pollTimer)
  stopOpPoll()
})
</script>

<template>
  <FieldRow
    label="账号密码"
    wide
    tip="RE0 已停止开放 OpenAPI 申请，改用网页登录：本站用浏览器（CloakBrowser）以你的账号登录 re0.me，在页面上签到、查资源、解锁。只在本机使用，密码不会发给除 RE0 以外的任何地方。已授权 OpenAPI 的老用户仍优先走 OpenAPI。"
  >
    <div class="app-row">
      <HInput v-model="form.base_url" placeholder="站点地址" class="w180" :input-attrs="plainProps('re0-base-url')" />
      <HInput v-model="form.username" placeholder="账号 / 邮箱" class="w200" :input-attrs="plainProps('re0-username')" />
      <div class="w200">
        <SecretInput
          v-model="form.password"
          name="re0-password"
          :reveal="{ key: 're0', field: 'password' }"
          placeholder="密码"
        />
      </div>
    </div>
    <div class="app-row sub">
      <HSwitch v-model="form.use_proxy" size="sm" aria-label="通过代理访问 RE0" />
      <span class="hint">浏览器通过「系统配置 → 代理」访问 RE0（默认直连；代理不支持带账号密码）</span>
    </div>
  </FieldRow>

  <FieldRow
    label="浏览器"
    tip="网页登录需要一个专用浏览器（CloakBrowser，约 210MB）。授权条款不允许随镜像分发，所以由本站从官方地址（GitHub，走系统代理）下载到数据目录，只下一次；中断了下次接着下，下载后用官方签名校验。GitHub 下载慢或被限流时，在「系统配置 → 代理」填 GitHub Token。"
  >
    <div class="app-row">
      <span class="state" :class="browser?.state">{{ browserText || '读取中…' }}</span>
      <HButton
        v-if="browser && (browser.state === 'missing' || browser.state === 'error')"
        variant="primary"
        size="sm"
        :loading="browserBusy"
        @click="downloadBrowser(false)"
      >
        下载浏览器
      </HButton>
      <HButton
        v-if="browser?.state === 'ready'"
        variant="ghost"
        size="sm"
        :loading="browserBusy"
        @click="downloadBrowser(true)"
      >
        重新下载
      </HButton>
    </div>
    <div v-if="downloading && browser" class="bar" :class="{ busy: browser.state !== 'downloading' || !browser.total }">
      <span :style="{ width: browser.state === 'downloading' && browser.total > 0 ? `${pct}%` : '30%' }" />
    </div>
    <div v-if="downloading && browser" class="hint dl">
      {{ browser.source ? `来源：${browser.source}` : '' }}{{ browser.reason ? `　触发：${browser.reason}` : '' }}
    </div>
    <div v-if="browser?.state === 'error' && browser.error" class="hint err">{{ browser.error }}</div>
    <div v-if="browser?.state === 'unsupported'" class="hint err">{{ browser.error }}</div>
  </FieldRow>

  <FieldRow
    label="检查登录"
    :tip="channel === 'openapi' ? '当前走 OpenAPI（已授权）。' : '会真的打开浏览器登录一次（首次可能要过 Cloudflare 验证，最长约 1 分半）。'"
  >
    <div class="app-row">
      <HButton variant="tertiary" :loading="checking" @click="check">检查状态</HButton>
      <span class="hint">当前通道：{{ channel === 'openapi' ? 'OpenAPI' : channel === 'web' ? '网页登录' : '未配置' }}</span>
    </div>
    <div v-if="checking && channel !== 'openapi'" class="op">⏳ {{ opText }}</div>
  </FieldRow>

  <FieldRow
    label="每日签到"
    :tip="`开启后每天 ${checkin?.hour ?? 8}:00 之后自动签到一次（普通模式，不赌），没签成一小时后再试，结果推送到消息通知。`"
  >
    <div class="app-row">
      <HSwitch
        :model-value="!!checkin?.enabled"
        :disabled="!checkin || checkinSaving"
        aria-label="自动签到"
        @update:model-value="toggleCheckin"
      />
      <HButton variant="tertiary" :loading="checkinRunning" :disabled="!!checkin?.blocked" @click="runCheckin">立即签到</HButton>
      <span v-if="checkin?.blocked" class="hint">{{ checkin.blocked }}</span>
      <span v-else-if="checkin?.done_today" class="hint">今天已签到</span>
    </div>
    <div v-if="checkinRunning" class="op">⏳ {{ opText }}</div>
    <div v-if="checkin?.last_result" class="hint last">
      上次（{{ checkin.last_result_at }}）：{{ checkin.last_result }}
    </div>
  </FieldRow>

  <ShareOutRow ref="shareOutRow" site="re0" />

  <div class="openapi-toggle">
    <HButton variant="ghost" size="sm" @click="showOpenapi = !showOpenapi">
      {{ showOpenapi ? '收起' : '展开' }} OpenAPI（站方已停止申请，仅已授权的老用户）
    </HButton>
  </div>

  <template v-if="showOpenapi">
    <FieldRow
      label="应用配置"
      wide
      tip="RE0 官方 OpenAPI。在 re0.me「个人面板 → OPENAPI → 我的应用」已有审核通过的应用才能用。创建时「授权结果投递」选浏览器跳转，「固定 Redirect URI 白名单」留空。"
    >
      <div class="app-row">
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
      tip="保存应用信息后点「授权」，跳转 RE0 官方授权页确认一次。授权后以你的身份查询 / 预览 / 解锁资源并每日签到，Token 自动续期。RE0 OpenAPI 只对有效 V / 长期 V 用户开放。"
    >
      <div class="app-row">
        <HButton variant="tertiary" @click="authorize">授权 RE0 账号</HButton>
        <LoginBadge :on="authorized" />
        <span v-if="authorized && authorizedAs" class="hint">{{ authorizedAs }}</span>
      </div>
      <div v-if="account" class="account">
        <span v-if="account.points != null">积分 <b>{{ account.points }}</b></span>
        <span v-if="account.level != null && account.level !== ''">等级 {{ account.level }}</span>
        <span v-if="account.app_name">应用 {{ account.app_name }}</span>
        <span v-if="account.banned" class="danger">账号处于封禁状态</span>
      </div>
      <HAlert v-if="authorized && missingScopes.length" status="warning" class="scope-alert">
        这次授权缺少 {{ missingScopes.join(' / ') }} 权限（RE0 新版接口要求「检查状态」带 meta、签到要 write），请点「授权 RE0 账号」重新授权一次。
      </HAlert>
    </FieldRow>

    <FieldRow
      label="出口 IP"
      tip="应用的「预期服务端出口 IP」填这个：本站访问 RE0 时对方看到的公网地址。家用宽带的 IP 重新拨号会变，变了要回 RE0 控制台改，否则请求会被拒。"
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
  </template>

  <FormActions>
    <HButton variant="primary" :loading="saving" @click="save">保存配置</HButton>
  </FormActions>

  <AutoUnlockRows site="re0" />
</template>

<style scoped>
.app-row {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}
.app-row.sub {
  margin-top: 8px;
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
.hint.err {
  color: var(--danger);
  margin-top: 6px;
  word-break: break-all;
}
.state {
  font-size: 13px;
}
.state.ready {
  color: var(--success);
}
.state.error,
.state.unsupported {
  color: var(--danger);
}
.bar {
  margin-top: 8px;
  max-width: 420px;
  height: 4px;
  border-radius: 999px;
  background: var(--default);
  overflow: hidden;
}
.bar > span {
  display: block;
  height: 100%;
  background: var(--accent);
  transition: width 300ms ease;
}
.bar.busy > span {
  animation: re0-busy 1.2s ease-in-out infinite;
}
@keyframes re0-busy {
  from {
    transform: translateX(-100%);
  }
  to {
    transform: translateX(340%);
  }
}
.op {
  margin-top: 6px;
  font-size: 12px;
  color: var(--accent);
}
.dl {
  margin-top: 4px;
}
.account {
  display: flex;
  gap: 16px;
  flex-wrap: wrap;
  margin-top: 6px;
  font-size: 13px;
  color: var(--muted);
}
.account b {
  color: var(--foreground);
}
.danger {
  color: var(--danger);
}
.scope-alert {
  margin-top: 8px;
}
.last {
  margin-top: 6px;
}
.openapi-toggle {
  margin: 4px 0 8px;
}
</style>
