<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HModal from '@/components/hero/HModal.vue'
import HSelect, { type SelectOption } from '@/components/hero/HSelect.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import SecretInput from '@/components/ui/SecretInput.vue'
import { pluginsApi } from '@/api'
import type { AliAccount, Ali2115Config } from '@/api/plugins'
import { toastError, useFeedback } from '@/composables/useFeedback'

/**
 * 阿里云盘秒传进 115 的配置：账号（扫码 / 贴 refresh token）、开关、只传视频与字幕。
 * 提交链接不在这里：在「发现与订阅 → 链接转存」贴，和 115 分享同一个入口
 */
const show = defineModel<boolean>('show', { default: false })
const { message, dialog } = useFeedback()

const form = ref<Ali2115Config>({ enabled: false, source: 0, media_only: true })
const accounts = ref<AliAccount[]>([])
const saving = ref(false)

async function load() {
  try {
    const d = await pluginsApi.ali2115Config()
    form.value = { ...d.config }
    accounts.value = d.accounts ?? []
  } catch (e) {
    toastError(e, '读取配置失败')
  }
}

watch(show, (v) => {
  if (v) load()
  else stopQR()
})

const sourceOptions = computed<SelectOption<number>[]>(() => [
  { label: '第一个账号', value: 0 },
  ...accounts.value.map((a) => ({ label: a.name || a.nick || `账号 ${a.id}`, value: a.id })),
])

async function save() {
  if (form.value.enabled && !accounts.value.length) {
    message.warning('先登录一个阿里云盘账号')
    return
  }
  saving.value = true
  try {
    await pluginsApi.saveAli2115(form.value)
    message.success('已保存')
    show.value = false
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}

// ---- 扫码登录 ----
const qr = ref<{ t: string; ck: string; image: string } | null>(null)
const qrText = ref('')
let qrTimer: ReturnType<typeof setTimeout> | undefined

const QR_STATUS: Record<string, string> = {
  NEW: '用阿里云盘 App 扫码',
  SCANED: '已扫码，请在手机上确认登录',
  EXPIRED: '二维码过期了，重新取一个',
  CANCELED: '已取消，重新取一个',
}

function stopQR() {
  clearTimeout(qrTimer)
  qr.value = null
  qrText.value = ''
}

async function startQR() {
  stopQR()
  try {
    qr.value = await pluginsApi.aliQRCode()
    qrText.value = QR_STATUS.NEW
    poll()
  } catch (e) {
    toastError(e, '取二维码失败，可以改用 refresh token 登录')
  }
}

function poll() {
  qrTimer = setTimeout(async () => {
    const q = qr.value
    if (!q || !show.value) return
    try {
      const d = await pluginsApi.aliQRCheck(q.t, q.ck)
      if (d.status === 'CONFIRMED') {
        message.success(d.message || '已登录')
        stopQR()
        await load()
        return
      }
      qrText.value = QR_STATUS[d.status] ?? d.status
      if (d.status === 'EXPIRED' || d.status === 'CANCELED') return
    } catch (e) {
      qrText.value = e instanceof Error ? e.message : '查询扫码状态失败'
      return
    }
    poll()
  }, 2000)
}

onBeforeUnmount(stopQR)

// ---- refresh token 登录 ----
const token = ref('')
const adding = ref(false)

async function addToken() {
  if (!token.value.trim()) return
  adding.value = true
  try {
    const d = await pluginsApi.aliAddToken(token.value.trim())
    message.success(d.message)
    token.value = ''
    await load()
  } catch (e) {
    toastError(e, '登录失败')
  } finally {
    adding.value = false
  }
}

// ---- 账号操作 ----
const testing = ref<number | null>(null)

async function test(a: AliAccount) {
  testing.value = a.id
  try {
    const d = await pluginsApi.aliTestAccount(a.id)
    message.success(d.message)
  } catch (e) {
    toastError(e, '账号不能用')
  } finally {
    testing.value = null
    await load()
  }
}

async function remove(a: AliAccount) {
  const ok = await dialog.confirm({
    title: '删除阿里云盘账号',
    content: `删掉「${a.name}」的登录信息。阿里云盘里的文件不受影响。`,
    actions: [
      { label: '取消', value: false, variant: 'tertiary' },
      { label: '删除', value: true, variant: 'danger' },
    ],
  })
  if (!ok) return
  try {
    await pluginsApi.aliDeleteAccount(a.id)
    if (form.value.source === a.id) form.value.source = 0
    await load()
  } catch (e) {
    toastError(e, '删除失败')
  }
}
</script>

<template>
  <HModal v-model:show="show" title="阿里云盘秒传" width="560px">
    <HAlert status="default">
      在「发现与订阅 → 链接转存」贴阿里云盘分享，按文件的 SHA1 秒传进 115 转存文件夹，进来后自动整理入库。
      文件内容不经过本站；115 上没有的文件秒传不了，会跳过并列在任务结果里。
      要传的文件会先存进你阿里云盘根目录的「115-Station秒传」里，传完就删。
    </HAlert>

    <FieldRow label="启用" tip="关着时贴阿里云盘分享会提示先来这里开启。">
      <HSwitch v-model="form.enabled" aria-label="启用阿里云盘秒传" />
    </FieldRow>
    <FieldRow
      label="只传视频与字幕"
      tip="图片、nfo 这类小文件 115 上多半没有，秒传不成还要多花一次 115 写请求；整理入库后刮削会补上。"
    >
      <HSwitch v-model="form.media_only" aria-label="只传视频与字幕" />
    </FieldRow>
    <FieldRow v-if="accounts.length > 1" label="默认账号" tip="存分享用哪个阿里云盘账号。">
      <HSelect v-model="form.source" :options="sourceOptions" aria-label="默认账号" />
    </FieldRow>

    <div class="sec">阿里云盘账号</div>
    <ul v-if="accounts.length" class="accts">
      <li v-for="a in accounts" :key="a.id">
        <div class="who">
          <b>{{ a.name || a.nick }}</b>
          <span v-if="a.last_err" class="err">{{ a.last_err }}</span>
          <span v-else-if="a.last_ok" class="ok">上次可用 {{ new Date(a.last_ok).toLocaleString('zh-CN') }}</span>
        </div>
        <HButton size="sm" variant="tertiary" :loading="testing === a.id" @click="test(a)">检查</HButton>
        <HButton size="sm" variant="tertiary" @click="remove(a)">删除</HButton>
      </li>
    </ul>
    <p v-else class="none">还没登录阿里云盘账号。</p>

    <div class="login">
      <div class="qr">
        <img v-if="qr" :src="qr.image" alt="阿里云盘登录二维码" width="168" height="168" />
        <HButton v-else size="sm" variant="secondary" @click="startQR">扫码登录</HButton>
        <span v-if="qrText" class="qr-text">{{ qrText }}</span>
        <HButton v-if="qr" size="sm" variant="tertiary" @click="startQR">换一个</HButton>
      </div>
      <FieldRow label="或贴 refresh token" wide tip="阿里云盘网页版登录后，开发者工具 → 应用 → 本地存储 token 里的 refresh_token。扫码不成时用它。">
        <div class="tok">
          <SecretInput v-model="token" name="ali-refresh-token" placeholder="refresh_token" />
          <HButton size="sm" variant="secondary" :loading="adding" :disabled="!token.trim()" @click="addToken">登录</HButton>
        </div>
      </FieldRow>
    </div>

    <template #footer>
      <div class="foot-right">
        <HButton variant="tertiary" @click="show = false">取消</HButton>
        <HButton variant="primary" :loading="saving" @click="save">保存</HButton>
      </div>
    </template>
  </HModal>
</template>

<style scoped>
.sec {
  margin: 14px 0 6px;
  font-size: 12.5px;
  font-weight: 600;
  color: var(--muted);
}
.accts {
  list-style: none;
  margin: 0;
  padding: 0;
}
.accts li {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px 0;
  border-top: 1px solid var(--border);
}
.who {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
  font-size: 13px;
}
.who .err {
  font-size: 12px;
  color: var(--danger);
  word-break: break-all;
}
.who .ok {
  font-size: 12px;
  color: var(--muted);
}
.none {
  margin: 0;
  font-size: 13px;
  color: var(--muted);
}
.login {
  margin-top: 10px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.qr {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
}
.qr img {
  border-radius: 8px;
  background: #fff; /* 二维码要白底才扫得出，暗色模式也一样 */
}
.qr-text {
  font-size: 13px;
  color: var(--muted);
}
.tok {
  display: flex;
  gap: 8px;
  align-items: center;
}
.tok > :first-child {
  flex: 1;
  min-width: 0;
}
.foot-right {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
