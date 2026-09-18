<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { NAlert, NButton, NInput, NInputGroup, NRadioButton, NRadioGroup } from 'naive-ui'
import SectionCard from '@/components/ui/SectionCard.vue'
import SecretInput from '@/components/ui/SecretInput.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import TestBanner, { type BannerState } from '@/components/ui/TestBanner.vue'
import CopyBox from '@/components/ui/CopyBox.vue'
import { configApi } from '@/api'
import { plainProps } from '@/utils/autofill'
import { useSetting } from '@/composables/useSetting'
import { toastError, useFeedback } from '@/composables/useFeedback'

const { message } = useFeedback()

const { model, saving, save, reset } = useSetting('emby', {
  server_url: '',
  api_key: '',
  path_mapping: '',
  style: 'unix',
  refresh_enabled: true,
})

const banner = ref<BannerState | null>(null)
const testing = ref(false)
const pathProbe = ref('')

async function testConnection() {
  const url = model.value.server_url.trim()
  if (!url) {
    banner.value = { status: 'err', title: '请先填写 Emby 服务器地址' }
    return
  }
  testing.value = true
  banner.value = { status: 'pending', title: '正在连接 Emby 服务器…' }
  try {
    const d = await configApi.testEmby({ server_url: url, api_key: model.value.api_key.trim() })
    banner.value = d.ok
      ? {
          status: 'ok',
          title: 'Emby 连接成功',
          detail: `${d.server_name || 'Emby'}${d.version ? ` · v${d.version}` : ''} · ${d.library_count ?? 0} 个媒体库`,
        }
      : { status: 'err', title: 'Emby 连接失败', detail: d.error || '未知错误' }
  } catch (e) {
    banner.value = { status: 'err', title: 'Emby 连接失败', detail: e instanceof Error ? e.message : '' }
  } finally {
    testing.value = false
  }
}

/** 本地路径 → Emby 路径。规则是「本地前缀#Emby前缀」，仅前缀匹配时替换 */
const pathResult = computed(() => {
  const input = pathProbe.value
  if (!input) return ''
  let out = input
  const rule = model.value.path_mapping
  if (rule.includes('#')) {
    const [src, dst] = rule.split('#')
    if (src && input.startsWith(src)) out = dst + input.slice(src.length)
  }
  return model.value.style === 'windows' ? out.replace(/\//g, '\\') : out
})

// ---- Emby Webhook 接收地址 ----
const notifyToken = ref('')

function genToken() {
  const buf = new Uint8Array(8)
  crypto.getRandomValues(buf)
  return [...buf].map((b) => b.toString(16).padStart(2, '0')).join('')
}

const webhookUrl = computed(() => `${location.origin}/api/emby/webhook?token=${notifyToken.value}`)

async function saveToken(t: string) {
  const token = t.trim()
  if (!token) {
    message.warning('token 不能为空')
    return
  }
  if (!/^[a-zA-Z0-9_-]{4,64}$/.test(token)) {
    message.warning('token 仅支持 4-64 位字母/数字/中横线/下划线')
    return
  }
  try {
    await configApi.saveSetting('emby-notify', { token })
    notifyToken.value = token
  } catch (e) {
    toastError(e, 'token 保存失败')
  }
}

function rotateToken() {
  const t = genToken()
  notifyToken.value = t
  saveToken(t)
  message.success('已生成新 token，请到 Emby 更新回调地址')
}

async function loadNotify() {
  const v = await configApi.getSetting<{ token: string; webhook?: string }>('emby-notify', {
    token: '',
    webhook: '',
  })
  let t = v.token
  // 兼容旧版：token 曾经只存在完整 webhook 地址里
  if (!t && v.webhook) t = v.webhook.match(/[?&]token=([^&]+)/)?.[1] ?? ''
  if (!t) {
    // 首次使用：自动生成并落库，用户不需要自己想一个
    t = genToken()
    await saveToken(t)
  }
  notifyToken.value = t
}

onMounted(loadNotify)

// 改了服务器地址就把上次的测试结果清掉，避免拿旧结论当新结论
watch(() => [model.value.server_url, model.value.api_key], () => (banner.value = null))
</script>

<template>
  <div class="tab">
    <SectionCard title="Emby 管理" hint="入库刷新与路径映射">
      <FieldRow
        label="Emby 服务器地址"
        required
        tip="Emby 访问地址（本容器内可达，如 http://172.17.0.1:8096）。6086 反代与此处配置联动。"
      >
        <NInput
          v-model:value="model.server_url"
          placeholder="如 http://192.168.1.100:8096"
          :input-props="plainProps('emby-server-url')"
        />
      </FieldRow>

      <FieldRow label="API 密钥" tip="Emby 后台「设置 → API 密钥」生成，用于按库刷新与连接测试。">
        <NInputGroup>
          <SecretInput
            v-model="model.api_key"
            name="emby-api-key"
            placeholder="Emby 设置 → API 密钥 中生成"
          />
          <NButton :loading="testing" @click="testConnection">测试连接</NButton>
        </NInputGroup>
        <TestBanner :state="banner" />
      </FieldRow>

      <FieldRow
        label="本地路径映射"
        tip="本地路径#Emby 路径（# 分隔）。入库刷新与建库插件共用此规则，把本地路径转换为 Emby 路径。"
      >
        <NInput v-model:value="model.path_mapping" placeholder="/media#/media" />
      </FieldRow>

      <FieldRow
        label="路径风格"
        tip="通知 Emby 的路径分隔符：Unix（/）或 Windows（\），与 Emby 挂载方式一致。"
      >
        <NRadioGroup v-model:value="model.style">
          <NRadioButton value="unix">Unix 风格</NRadioButton>
          <NRadioButton value="windows">Windows 风格</NRadioButton>
        </NRadioGroup>
      </FieldRow>

      <FieldRow
        label="路径转换测试"
        wide
        tip="输入一个本地路径，下方显示转换后的 Emby 路径，一致则说明映射正确。"
      >
        <NInput v-model:value="pathProbe" placeholder="如 /media/电影/钢铁侠.mkv" />
        <CopyBox v-if="pathResult" class="probe-out" :value="pathResult" tone="success" />
      </FieldRow>

      <FieldRow
        label="入库时刷新"
        tip="启用后 STRM 生成时自动通知 Emby 刷新对应媒体库（用上方服务器地址与 API 密钥）。"
      >
        <NRadioGroup v-model:value="model.refresh_enabled">
          <NRadioButton :value="true">启用</NRadioButton>
          <NRadioButton :value="false">禁用</NRadioButton>
        </NRadioGroup>
      </FieldRow>

      <FormActions>
        <NButton type="primary" :loading="saving" @click="save()">保存配置</NButton>
        <NButton @click="reset">重置配置</NButton>
      </FormActions>

      <NAlert class="note" type="info" :bordered="false">
        配置 Emby 连接后，STRM 生成时按库刷新对应媒体库，整理入库更快。非必须——开启 Emby
        实时监控也一样，此通知只是能更快入库。
      </NAlert>
    </SectionCard>

    <SectionCard title="Emby Webhook" hint="入库 / 删除 / 播放时的消息通知">
      <NAlert class="note-top" type="info" :bordered="false">
        把下方地址填到 Emby 的 Webhooks（请求方式 POST，内容类型 application/json）。
        推送哪些事件由 Emby 侧勾选决定；token 为本实例自动生成的鉴权密钥。
      </NAlert>

      <FieldRow
        label="接收地址"
        wide
        tip="填到 Emby 的 Webhooks（POST + application/json）。主机部分可换成 Emby 可达的任意地址，路径与 token 不变。"
        hint="主机部分按 Emby 的网络环境替换：同宿主机容器间用 172.17.0.1，局域网用内网 IP，跨网用公网 IP/域名 —— 路径与 token 保持不变即可。"
      >
        <CopyBox :value="webhookUrl" />
      </FieldRow>

      <FieldRow
        label="鉴权 token"
        tip="自动生成，可改成自己好记的（仅字母数字）；修改后立即生效，Emby 侧地址需同步更新。"
        hint="Emby 推送时必须携带相同 token，否则返回 401；「换一个」随机重新生成。"
      >
        <NInputGroup>
          <NInput
            v-model:value="notifyToken"
            placeholder="自动生成，可自定义"
            @blur="saveToken(notifyToken)"
          />
          <NButton @click="rotateToken">换一个</NButton>
        </NInputGroup>
      </FieldRow>
    </SectionCard>
  </div>
</template>

<style scoped>
.tab {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.probe-out {
  margin-top: 8px;
}
.note {
  margin-top: 16px;
}
.note-top {
  margin-bottom: 8px;
}
</style>
