<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import {
  NAlert,
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
import { FolderPlus, QrCode, ShieldCheck } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import MeterBar from '@/components/ui/MeterBar.vue'
import QrLoginModal from '@/components/QrLoginModal.vue'
import Cid115Input from '@/components/Cid115Input.vue'
import LocalPathInput from '@/components/LocalPathInput.vue'
import { configApi, organizeApi, storageApi } from '@/api'
import { useFullSetting } from '@/pages/strm/fullSetting'
import { plainProps } from '@/utils/autofill'
import { DEVICE_OPTIONS } from '@/types/storage'
import type { StorageCheck } from '@/types/storage'
import { bytes } from '@/utils/format'
import { toastError, useFeedback } from '@/composables/useFeedback'

const { message, dialog } = useFeedback()
const media = useFullSetting()
const cidInput = ref<InstanceType<typeof Cid115Input> | null>(null)
const cidValue = ref({ cid: '', path: '' })

watch(
  () => [media.model.value.cid, media.model.value.cid_path] as const,
  async ([cid, savedPath]) => {
    if (!cid) {
      cidValue.value = { cid: '', path: '' }
      return
    }
    const displayPath = savedPath || cid
    if (cid !== cidValue.value.cid || displayPath !== cidValue.value.path) {
      cidValue.value = { cid, path: displayPath }
    }
    // 兼容旧配置：过去只保存 cid，进入页面时自动反查一次可读路径。
    if (!savedPath) {
      try {
        const resolved = await storageApi.path115(cid)
        if (media.model.value.cid === cid && resolved.path) {
          cidValue.value = { cid, path: resolved.path }
        }
      } catch {
        // Cookie 暂不可用时保留 cid；重新选择目录或下次保存仍可补齐。
      }
    }
  },
  { immediate: true },
)

/**
 * 还没配置的工作目录（转存 / 待整理 / 已存在 / 冗余）。
 * 四个都配好了就不显示一键创建；媒体库没保存时后端会拒绝，按钮也不出。
 */
const missingWs = ref<string[]>([])
const creatingWs = ref(false)

async function loadMissingWs() {
  try {
    const [org, share] = await Promise.all([
      configApi.getSetting<Record<string, string>>('org-basic', {}),
      configApi.getSetting<Record<string, string>>('share', {}),
    ])
    const out: string[] = []
    if (!share.folder) out.push('转存')
    if (!org.pending) out.push('待整理')
    if (!org.existing) out.push('已存在')
    if (!org.redundant) out.push('冗余')
    missingWs.value = out
  } catch {
    missingWs.value = []
  }
}

async function createWs() {
  const ok = await dialog.confirm({
    title: '一键创建工作目录',
    content: `将在 115 网盘根目录下创建 /StrmStation/{${missingWs.value.join('、')}} 并写入配置。已配置的目录保持不动，同名目录已存在则直接复用。`,
    actions: [
      { label: '取消', value: false, variant: 'tertiary' },
      { label: '创建', value: true, variant: 'primary' },
    ],
  })
  if (!ok) return
  creatingWs.value = true
  try {
    const res = await organizeApi.initWorkspace()
    message.success(res.message)
    await loadMissingWs()
  } catch (e) {
    toastError(e, '创建失败')
  } finally {
    creatingWs.value = false
  }
}

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

/** 启用 OpenAPI 但没填 AppID 时后端会当作未启用，静默回落 Cookie——先在这里拦住 */
function validate(): boolean {
  if (form.value.openapi_enabled && !form.value.app_id.trim()) {
    message.error('启用 OPENAPI 必须填写开放平台 AppID；没有 AppID 请选择「禁用」，Cookie 通道功能完整')
    return false
  }
  return true
}

async function save(): Promise<boolean> {
  if (!validate()) return false
  saving.value = true
  try {
    await storageApi.save({ name: '115主号', type: '115', ...form.value })
    message.success('保存成功')
    return true
  } catch (e) {
    toastError(e, '保存失败')
    return false
  } finally {
    saving.value = false
  }
}

async function saveAndScan() {
  if (await save()) qrShow.value = true
}

async function saveMedia() {
  const cid = (await cidInput.value?.ensureCid()) ?? ''
  if (!cid || cid === '0') {
    message.error('目录路径无法识别：请点「选择目录」重新选择，或输入纯数字 cid')
    return
  }
  if (!media.model.value.local_path.trim()) {
    message.error('请填写本地媒体库根目录')
    return
  }
  media.model.value.cid = cid
  let readablePath = cidValue.value.path.trim()
  if (!readablePath || /^\d+$/.test(readablePath)) {
    try {
      readablePath = (await storageApi.path115(cid)).path
    } catch {
      readablePath = ''
    }
  }
  media.model.value.cid_path = readablePath
  await media.save()
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

onMounted(() => {
  load()
  loadMissingWs()
})
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
        tip="需先在 115 开放平台（open.115.com）申请应用拿到 AppID。没有 AppID 请保持「禁用」——Cookie 通道功能完整，且全量同步可用「快速模式」。"
      >
        <NRadioGroup v-model:value="form.openapi_enabled">
          <NRadioButton :value="true">启用</NRadioButton>
          <NRadioButton :value="false">禁用（默认）</NRadioButton>
        </NRadioGroup>
      </FieldRow>

      <FieldRow
        v-if="form.openapi_enabled"
        label="开放平台 AppID"
        required
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

    <SectionCard title="媒体库位置" hint="115 源目录 → 本地 STRM 根目录">
      <NAlert class="media-note" type="info" :bordered="false">
        此处是全量同步、增量同步、自动整理、影视刮削和 Emby 路径映射共同使用的统一位置配置。
      </NAlert>

      <FieldRow
        label="115 媒体库目录"
        required
        tip="全量同步、增量同步、整理入库与洗版判定共同锚定此目录。"
      >
        <Cid115Input ref="cidInput" v-model="cidValue" />
      </FieldRow>

      <NAlert
        v-if="media.saved.value.cid && missingWs.length"
        class="ws-note"
        type="warning"
        :bordered="false"
      >
        <div class="ws-row">
          <span>
            还没有配置{{ missingWs.join('、') }}目录。它们与媒体库目录必须互不包含，
            可一键在网盘根目录下创建 /StrmStation 统一存放。
          </span>
          <NButton size="small" type="primary" ghost :loading="creatingWs" @click="createWs">
            <template #icon><FolderPlus :size="15" /></template>
            一键创建
          </NButton>
        </div>
      </NAlert>

      <FieldRow
        label="本地媒体库根目录"
        required
        tip="STRM、字幕、NFO 与图片的统一本地保存根目录，也是影视刮削使用的根目录。"
      >
        <LocalPathInput v-model="media.model.value.local_path" />
      </FieldRow>

      <FormActions>
        <NButton type="primary" :loading="media.saving.value" @click="saveMedia">保存媒体库位置</NButton>
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
.media-note {
  margin-bottom: 12px;
}
.ws-note {
  margin-bottom: 12px;
}
.ws-row {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.ws-row > span {
  flex: 1;
  min-width: 200px;
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
