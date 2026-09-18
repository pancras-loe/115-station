<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import {
  NButton,
  NCollapse,
  NCollapseItem,
  NInput,
  NInputGroup,
  NInputNumber,
  NRadioButton,
  NRadioGroup,
  NSelect,
  NTag,
} from 'naive-ui'
import { QrCode, ShieldCheck } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import MeterBar from '@/components/ui/MeterBar.vue'
import QrLoginModal from '@/components/QrLoginModal.vue'
import { storageApi } from '@/api'
import { plainProps } from '@/utils/autofill'
import { DEVICE_OPTIONS } from '@/types/storage'
import type { StorageCheck } from '@/types/storage'
import { bytes } from '@/utils/format'
import { toastError, useFeedback } from '@/composables/useFeedback'

const { message } = useFeedback()

const DEFAULT_COOKIE_PATH = '/config/115-cookies.txt'

const form = ref({
  cookie_path: DEFAULT_COOKIE_PATH,
  device: 'web',
  interval: 3.0,
  openapi_enabled: false,
  app_id: '',
})

const account = ref<StorageCheck | null>(null)
const checking = ref(false)
const saving = ref(false)
const qrShow = ref(false)

/** OPENAPI 启用且填了 AppID 才走开放平台授权，否则回退 Cookie 扫码 */
const qrChannel = computed<'openapi' | 'cookie'>(() =>
  form.value.openapi_enabled && form.value.app_id.trim() ? 'openapi' : 'cookie',
)

const usedPct = computed(() => {
  const a = account.value
  if (!a?.total_size) return 0
  return Math.min(100, ((a.used_size ?? 0) / a.total_size) * 100)
})

const vipLabel = computed(() => {
  const a = account.value
  if (!a) return '-'
  if (a.vip_forever === 1 || a.vip_forever === true) return '终身会员'
  if ((a.vip ?? 0) > 0) {
    return a.vip_expire && a.vip_expire > 0
      ? `会员，到期 ${new Date(a.vip_expire * 1000).toLocaleDateString('zh-CN')}`
      : '会员'
  }
  return '非会员'
})

const vipBadge = computed(() => {
  const a = account.value
  if (!a) return null
  if (a.vip_forever === 1 || a.vip_forever === true) return '终身VIP'
  return (a.vip ?? 0) > 0 ? 'VIP' : null
})

async function load() {
  try {
    const res = await storageApi.list()
    const acc = (res.data ?? []).find((s) => s.type === '115')
    if (acc) {
      form.value = {
        cookie_path: acc.cookie_path || DEFAULT_COOKIE_PATH,
        // 保存过的设备值不在选项里时保持默认网页端
        device: DEVICE_OPTIONS.some((o) => o.value === acc.device) ? acc.device : 'web',
        interval: acc.interval || 3.0,
        openapi_enabled: !!acc.openapi_enabled,
        app_id: acc.app_id || '',
      }
    }
    // 静默刷新账号卡片（真实容量/会员/头像）；未绑定时失败是正常的，不提示
    const chk = await storageApi.check().catch(() => null)
    if (chk?.valid) account.value = chk
  } catch {
    // 列表拿不到就保持默认表单，不打断页面
  }
}

async function check() {
  checking.value = true
  try {
    const data = await storageApi.check()
    if (!data.valid) {
      message.error('Cookie 无效：' + (data.message || '未知原因'))
      return
    }
    account.value = data
    message.success('Cookie 检测成功')
  } catch (e) {
    toastError(e, 'Cookie 检测失败')
  } finally {
    checking.value = false
  }
}

async function save() {
  saving.value = true
  try {
    await storageApi.save({ name: '115主号', type: '115', ...form.value })
    message.success('保存成功')
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}

async function saveAndScan() {
  await save()
  qrShow.value = true
}

function reset() {
  form.value = {
    cookie_path: DEFAULT_COOKIE_PATH,
    device: 'web',
    interval: 3.0,
    openapi_enabled: false,
    app_id: '',
  }
  account.value = null
  message.info('配置已重置（尚未保存）')
}

onMounted(load)
</script>

<template>
  <div class="page">
    <SectionCard title="115 账号" hint="扫码绑定 · 通道与风控参数">
      <FieldRow
        label="Cookie 路径"
        tip="指定 115 Cookie 文件路径，扫码登录后自动写入该文件。"
      >
        <NInputGroup>
          <NInput
            v-model:value="form.cookie_path"
            placeholder="/config/115-cookies.txt"
            :input-props="plainProps('acc-cookie-path')"
          />
          <NButton type="primary" ghost :loading="checking" @click="check">
            <template #icon><ShieldCheck :size="15" /></template>
            检测可用性
          </NButton>
        </NInputGroup>
      </FieldRow>

      <FieldRow
        label="Cookie 设备"
        tip="网页端 Cookie 与 115Browser UA 配套，兼容性最好（默认推荐）。App 端 Cookie 有设备槽位校验，与桌面 UA 不匹配会报“服务器开小差”。避免选常用设备导致被挤下线。"
      >
        <NInputGroup>
          <NSelect v-model:value="form.device" :options="DEVICE_OPTIONS" />
          <NButton type="primary" @click="qrShow = true">
            <template #icon><QrCode :size="15" /></template>
            二维码登录
          </NButton>
        </NInputGroup>
      </FieldRow>

      <FieldRow
        label="启用 OPENAPI"
        tip="强烈推荐！使用 115 官方开放平台接口，无 UA 风控、无“服务器开小差”，token 自动续期。Cookie 通道作为回退保留。"
      >
        <NRadioGroup v-model:value="form.openapi_enabled">
          <NRadioButton :value="true">启用（推荐）</NRadioButton>
          <NRadioButton :value="false">禁用</NRadioButton>
        </NRadioGroup>
      </FieldRow>

      <FieldRow
        label="开放平台 AppID"
        tip="在 115 开放平台（open.115.com）申请应用后获取。填入后点击“保存并扫码授权”，用 115 App 扫码完成 OAuth 授权。"
      >
        <NInputGroup>
          <NInput
            v-model:value="form.app_id"
            placeholder="请输入 115 开放平台 AppID"
            :input-props="plainProps('acc-open-app-id')"
          />
          <NButton type="primary" ghost @click="saveAndScan">保存并扫码授权</NButton>
        </NInputGroup>
      </FieldRow>

      <FieldRow
        label="API 请求间隔"
        tip="设置 API 请求间隔可以减少风控概率。读接口默认 1s，写接口建议 ≥3s。"
      >
        <NInputNumber v-model:value="form.interval" :min="0.5" :step="0.5" style="width: 150px">
          <template #suffix>秒</template>
        </NInputNumber>
      </FieldRow>

      <FormActions>
        <NButton type="primary" :loading="saving" @click="save">保存配置</NButton>
        <NButton @click="reset">重置配置</NButton>
      </FormActions>
    </SectionCard>

    <SectionCard v-if="account" title="账号状态">
      <div class="acc">
        <div class="acc-avatar">
          <img v-if="account.avatar" :src="account.avatar" alt="" />
          <span v-else>115</span>
        </div>

        <div class="acc-body">
          <div class="acc-head">
            <span class="acc-name">{{ account.username || '-' }}</span>
            <NTag v-if="vipBadge" size="small" type="warning" :bordered="false">{{ vipBadge }}</NTag>
            <span class="acc-channel">通道：{{ account.channel || 'Cookie' }}</span>
          </div>

          <dl class="acc-meta">
            <div><dt>UID</dt><dd>{{ account.user_id ?? '-' }}</dd></div>
            <div><dt>会员</dt><dd>{{ vipLabel }}</dd></div>
            <div><dt>容量</dt><dd>{{ account.capacity || '-' }}</dd></div>
          </dl>

          <template v-if="account.total_size">
            <MeterBar :percent="usedPct" />
            <div class="acc-usage">
              已用 {{ usedPct.toFixed(1) }}% · {{ bytes(account.used_size) }} /
              {{ bytes(account.total_size) }}
            </div>
          </template>

          <NCollapse v-if="account.devices?.length" class="acc-devices">
            <NCollapseItem :title="`登录设备（${account.devices.length}）`" name="d">
              <div v-for="(d, i) in account.devices" :key="i" class="dev">
                <span class="dev-dot" :class="{ current: d.is_current }" />
                <span class="dev-name">{{ d.name || d.device || '未知设备' }}</span>
                <span class="dev-ip">{{ d.ip }}{{ d.city ? `（${d.city}）` : '' }}</span>
                <span class="dev-time">
                  {{ d.utime && d.utime > 0 ? new Date(d.utime * 1000).toLocaleString('zh-CN') : '' }}
                </span>
              </div>
            </NCollapseItem>
          </NCollapse>
        </div>
      </div>
    </SectionCard>

    <QrLoginModal
      v-model:show="qrShow"
      :channel="qrChannel"
      :device="form.device"
      :app-id="form.app_id"
      @success="(message.success('115 账号绑定成功'), check())"
    />
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.acc {
  display: flex;
  gap: 16px;
}
.acc-avatar {
  width: 56px;
  height: 56px;
  flex-shrink: 0;
  border-radius: 50%;
  overflow: hidden;
  display: grid;
  place-items: center;
  background: var(--c-primary-soft);
  color: var(--c-primary);
  font-size: 13px;
  font-weight: 700;
}
.acc-avatar img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.acc-body {
  flex: 1;
  min-width: 0;
}
.acc-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
}
.acc-name {
  font-size: 15px;
  font-weight: 600;
  color: var(--c-text-1);
}
.acc-channel {
  margin-left: auto;
  font-size: 12px;
  color: var(--c-text-3);
}

.acc-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 26px;
  margin: 0 0 12px;
}
.acc-meta > div {
  display: flex;
  gap: 6px;
  font-size: 13px;
}
.acc-meta dt {
  color: var(--c-text-3);
}
.acc-meta dd {
  margin: 0;
  color: var(--c-text-1);
}

.acc-usage {
  margin-top: 6px;
  font-size: 11.5px;
  color: var(--c-text-3);
}

.acc-devices {
  margin-top: 12px;
}
.dev {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 3px 0;
  font-size: 12px;
  color: var(--c-text-2);
}
.dev-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  flex-shrink: 0;
  background: var(--c-text-4);
}
.dev-dot.current {
  background: var(--c-success);
}
.dev-name {
  font-weight: 500;
  color: var(--c-text-1);
}
.dev-ip {
  color: var(--c-text-3);
}
.dev-time {
  margin-left: auto;
  color: var(--c-text-3);
  font-variant-numeric: tabular-nums;
}

@media (max-width: 720px) {
  .dev-time {
    display: none;
  }
}
</style>
