<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import SecretInput from '@/components/ui/SecretInput.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import ShareOutRow from './ShareOutRow.vue'
import AutoUnlockRows from './AutoUnlockRows.vue'
import FormActions from '@/components/ui/FormActions.vue'
import { resourcesApi } from '@/api'
import type { Re0BrowserState, Re0Checkin, Re0OpState } from '@/api/resources'
import { plainProps } from '@/utils/autofill'
import { toastError, useFeedback } from '@/composables/useFeedback'

/**
 * 癫影（m.dian115.com）：账号密码网页登录。浏览器（CloakBrowser）和 RE0 共用同一个，
 * 下载 / 重新下载在 RE0 那张卡片上；这里只显示状态。
 */
const emit = defineEmits<{ changed: [] }>()
const { message } = useFeedback()
const shareOutRow = ref<InstanceType<typeof ShareOutRow> | null>(null)

const form = ref({ email: '', password: '' })
let savedForm = ''
const saving = ref(false)
const checking = ref(false)
const account = ref<{ user?: string; points?: number; vip?: boolean } | null>(null)
const checkin = ref<Re0Checkin | null>(null)
const checkinSaving = ref(false)
const checkinRunning = ref(false)
const browser = ref<Re0BrowserState | null>(null)

// 「检查状态」「立即签到」要开浏览器、登录：等的时候每秒读一次后端走到哪一步（与 RE0 共用一个操作状态）
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

const browserText = computed(() => {
  const b = browser.value
  if (!b) return '读取中…'
  switch (b.state) {
    case 'ready':
      return `已就绪（CloakBrowser ${b.version}，与 RE0 共用）`
    case 'missing':
      return '还没下载（保存账号后自动下载，约 210MB）'
    case 'downloading':
    case 'verifying':
    case 'extracting':
      return b.total > 0 ? `下载中 ${Math.floor((b.done / b.total) * 100)}%` : '下载中…'
    case 'error':
      return `下载失败：${b.error ?? ''}（到 RE0 设置里重新下载）`
    case 'unsupported':
      return b.error || '这个平台用不了'
  }
  return b.state
})

async function load() {
  try {
    const d = await resourcesApi.dianConfig()
    form.value = { email: d.email ?? '', password: d.password ?? '' }
    savedForm = JSON.stringify(form.value)
  } catch {
    // 首次使用尚无配置
  }
  await Promise.all([loadCheckin(), loadBrowser()])
}

async function loadBrowser() {
  try {
    browser.value = (await resourcesApi.re0Browser()).browser
  } catch {
    browser.value = null
  }
}

async function loadCheckin() {
  try {
    checkin.value = await resourcesApi.dianCheckin()
  } catch {
    checkin.value = null
  }
}

async function save() {
  saving.value = true
  try {
    const d = await resourcesApi.dianSaveConfig(form.value)
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

async function check() {
  if (!form.value.email.trim() || !form.value.password) {
    message.warning('请先填写癫影账号和密码')
    return
  }
  checking.value = true
  try {
    // 查的是后端已保存的配置：改了没保存就先存
    if (JSON.stringify(form.value) !== savedForm) {
      await resourcesApi.dianSaveConfig(form.value)
      savedForm = JSON.stringify(form.value)
      message.info('配置有改动，已先保存')
      emit('changed')
    }
    startOpPoll()
    const d = await resourcesApi.dianCheck()
    account.value = d
    message.success(d.message)
    await loadCheckin()
  } catch (e) {
    account.value = null
    toastError(e, '检查失败')
  } finally {
    stopOpPoll()
    checking.value = false
  }
}

async function toggleCheckin(on: boolean) {
  if (!checkin.value) return
  checkinSaving.value = true
  try {
    await resourcesApi.dianSaveCheckin(on)
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
    const d = await resourcesApi.dianRunCheckin()
    message.success(d.message)
  } catch (e) {
    toastError(e, '签到失败')
  } finally {
    stopOpPoll()
    checkinRunning.value = false
    await loadCheckin()
  }
}

onMounted(load)
onBeforeUnmount(stopOpPoll)
</script>

<template>
  <FieldRow
    label="账号密码"
    wide
    tip="癫影（站名 MADOW）只能网页登录：本站用浏览器以你的账号登录 m.dian115.com，调站点自己的接口查资源、解锁、签到。资源列表按 TMDB 条目读，解锁花站内积分。密码只发给癫影。"
  >
    <div class="app-row">
      <HInput v-model="form.email" placeholder="邮箱" class="w220" :input-attrs="plainProps('dian-email')" />
      <div class="w200">
        <SecretInput v-model="form.password" name="dian-password" :reveal="{ key: 'dian', field: 'password' }" placeholder="密码" />
      </div>
    </div>
  </FieldRow>

  <FieldRow label="浏览器" tip="与 RE0 网页登录共用同一个 CloakBrowser，下载与重新下载在「RE0」小节里。">
    <span class="state" :class="browser?.state">{{ browserText }}</span>
  </FieldRow>

  <FieldRow label="检查登录" tip="会真的打开浏览器登录一次，读出账号与积分。登录态存在浏览器的用户数据目录里，之后不用每次登录。">
    <div class="app-row">
      <HButton variant="tertiary" :loading="checking" @click="check">检查状态</HButton>
      <span v-if="account?.user" class="hint">
        {{ account.user }}　积分 <b>{{ account.points }}</b>{{ account.vip ? '　VIP' : '' }}
      </span>
    </div>
    <div v-if="checking" class="op">⏳ {{ opText }}</div>
  </FieldRow>

  <FieldRow
    label="每日签到"
    :tip="`开启后每天 ${checkin?.hour ?? 8}:00 之后自动签到一次（稳健模式，不选会倒扣积分的运气模式），没签成一小时后再试，结果推送到消息通知。`"
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
    <div v-if="checkin?.last_result" class="hint last">上次（{{ checkin.last_result_at }}）：{{ checkin.last_result }}</div>
  </FieldRow>

  <ShareOutRow ref="shareOutRow" site="dian" />

  <FormActions>
    <HButton variant="primary" :loading="saving" @click="save">保存配置</HButton>
  </FormActions>

  <AutoUnlockRows site="dian" />
</template>

<style scoped>
.app-row {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}
.w200 {
  width: 200px;
}
.w220 {
  width: 220px;
}
.hint {
  color: var(--muted);
  font-size: 12px;
}
.hint b {
  color: var(--foreground);
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
.op {
  margin-top: 6px;
  font-size: 12px;
  color: var(--accent);
}
.last {
  margin-top: 6px;
}
</style>
