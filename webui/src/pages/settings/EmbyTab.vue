<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import SecretInput from '@/components/ui/SecretInput.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import TestBanner, { type BannerState } from '@/components/ui/TestBanner.vue'
import CopyBox from '@/components/ui/CopyBox.vue'
import { configApi, localApi } from '@/api'
import { plainProps } from '@/utils/autofill'
import { useSetting } from '@/composables/useSetting'
import { useFullSetting } from '@/pages/strm/fullSetting'
import { toastError, useFeedback } from '@/composables/useFeedback'

const { message } = useFeedback()
const router = useRouter()
const route = useRoute()
const media = useFullSetting()

const { model, loading, saving, save, reset } = useSetting('emby', {
  server_url: '',
  api_key: '',
  path_mapping: '',
  style: 'unix',
  refresh_enabled: true,
})

const banner = ref<BannerState | null>(null)
const testing = ref(false)
const pathProbe = ref('')
const embyMediaRoot = ref('')
/** 映射本地这一侧：本地媒体库根下的子目录（Emby 直接从 115 库目录那一层挂进去时填库名），空 = 根本身 */
const localSub = ref('')

/** path_mapping 前半段只认相对路径（后端 embyLocalSub 同口径）；老配置写的是当时的本地根（绝对路径），当作空 */
function subOf(raw: string): string {
  const s = raw.trim().replace(/\\/g, '/')
  if (!s || s.startsWith('/') || /^[A-Za-z]:/.test(s)) return ''
  return s.replace(/^\.\//, '').replace(/\/+$/, '')
}

watch(
  () => model.value.path_mapping,
  (rule) => {
    const splitAt = rule.indexOf('#')
    embyMediaRoot.value = splitAt >= 0 ? rule.slice(splitAt + 1) : ''
    localSub.value = splitAt >= 0 ? subOf(rule.slice(0, splitAt)) : ''
  },
  { immediate: true },
)

/** 映射本地这一侧的完整路径 */
const localSide = computed(() => {
  const root = media.model.value.local_path.trim().replace(/\/+$/, '')
  const sub = subOf(localSub.value)
  return sub ? `${root}/${sub}` : root
})

// ---- Emby 媒体库目录：按 Emby 媒体库的路径推算（/emby/path-suggest），只填进表单，保存由用户点 ----
const detecting = ref(false)
const detectNote = ref('')

function applySuggest(root: string, sub: string, how: string) {
  const desc = sub ? `本地子目录「${sub}」、Emby 媒体库目录 ${root}` : root
  if (root === embyMediaRoot.value.trim() && sub === subOf(localSub.value)) {
    detectNote.value = `推算结果与当前一致：${desc}`
    return
  }
  embyMediaRoot.value = root
  localSub.value = sub
  detectNote.value = `${how}已填入 ${desc}，确认无误后点「保存」`
}

async function detectEmbyRoot() {
  detecting.value = true
  detectNote.value = ''
  try {
    const d = await localApi.embyPathSuggest()
    if (!d.configured) detectNote.value = '先填好 Emby 服务器地址与 API 密钥并保存'
    else if (d.error) detectNote.value = d.error
    else if (!d.suggest)
      detectNote.value = 'Emby 媒体库的目录在本地媒体库根下（含往下一层的库目录）找不到同名目录，推算不出来，请对照 Emby 媒体库路径手动填写'
    else {
      const ev = d.evidence?.[0]
      applySuggest(d.suggest, d.suggest_sub || '', ev ? `按 Emby「${ev.location}」⇄ 本地「${ev.local}」` : '')
    }
  } catch (e) {
    toastError(e, '检测失败')
  } finally {
    detecting.value = false
  }
}

// 从对账弹窗跳过来时带着推算值（?emby_root=）：等配置读完再填，否则会被读回来的旧值盖掉
watch(
  loading,
  (busy) => {
    const root = typeof route.query.emby_root === 'string' ? route.query.emby_root.trim() : ''
    if (busy || !root) return
    const sub = typeof route.query.emby_sub === 'string' ? route.query.emby_sub.trim() : ''
    applySuggest(root, sub, '对账推算的值')
    router.replace({ query: { ...route.query, emby_root: undefined, emby_sub: undefined } })
  },
  { immediate: true },
)

// 前半段存相对子目录而不是绝对路径：本地媒体库目录以后改了，映射跟着走，不用回来再改一遍
function currentPathMapping(): string {
  const embyRoot = embyMediaRoot.value.trim()
  return embyRoot ? `${subOf(localSub.value)}#${embyRoot}` : ''
}

async function saveEmby() {
  model.value.path_mapping = currentPathMapping()
  await save()
}

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
  const src = localSide.value
  const dst = embyMediaRoot.value.trim().replace(/\/+$/, '')
  if (src && dst && (input === src || input.startsWith(src + '/'))) out = dst + input.slice(src.length)
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
  // 后端对 token 回的是掩码，接收地址要拼真值，否则复制出去的是 token=••••
  if (t === configApi.SECRET_MASK) t = await configApi.revealSecret('emby-notify', 'token')
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
        <HInput v-model="model.server_url" placeholder="如 http://192.168.1.100:8096" :input-attrs="plainProps('emby-server-url')" />
      </FieldRow>

      <FieldRow label="API 密钥" tip="Emby 后台「设置 → API 密钥」生成，用于按库刷新与连接测试。">
        <div class="h-field-row">
          <SecretInput
            v-model="model.api_key"
            name="emby-api-key"
            :reveal="{ key: 'emby', field: 'api_key' }"
            placeholder="Emby 设置 → API 密钥 中生成"
          />
          <HButton variant="tertiary" :loading="testing" @click="testConnection">测试连接</HButton>
        </div>
        <TestBanner :state="banner" />
      </FieldRow>

      <FieldRow
        label="本地路径映射"
        tip="本地这一侧默认是本地媒体库目录。Emby 若直接从 115 库目录那一层挂进去（STRM 路径第一层是 115 媒体库目录名），在子目录里填这个名字。Emby 那一侧填对应目录在 Emby 里的路径。"
      >
        <div class="path-pair">
          <label>
            <span>本地媒体库目录 / 子目录（可选）</span>
            <div class="local-side">
              <HInput :model-value="media.model.value.local_path || '未配置'" readonly />
              <span class="sep">/</span>
              <HInput v-model="localSub" placeholder="如 资源库，一般留空" />
            </div>
          </label>
          <span class="path-arrow">→</span>
          <label>
            <span>Emby 媒体库目录</span>
            <HInput v-model="embyMediaRoot" placeholder="如 /media" />
          </label>
        </div>
        <div class="detect">
          <HButton size="sm" variant="tertiary" :loading="detecting" @click="detectEmbyRoot">自动检测</HButton>
          <span v-if="detectNote" class="detect-note">{{ detectNote }}</span>
        </div>
        <HButton variant="ghost" class="text-btn location-link" @click="router.push({ name: 'accounts' })">
          前往「账号与媒体库」修改本地目录
        </HButton>
      </FieldRow>

      <FieldRow
        label="路径风格"
        tip="通知 Emby 的路径分隔符：Unix（/）或 Windows（\），与 Emby 挂载方式一致。"
      >
        <HSegmented v-model="model.style" :options="[{ label: 'Unix 风格', value: 'unix' }, { label: 'Windows 风格', value: 'windows' }]" />
      </FieldRow>

      <FieldRow
        label="路径转换测试"
        wide
        tip="输入一个本地路径，下方显示转换后的 Emby 路径，一致则说明映射正确。"
      >
        <HInput v-model="pathProbe" placeholder="如 /media/电影/钢铁侠.mkv" />
        <CopyBox v-if="pathResult" class="probe-out" :value="pathResult" tone="success" />
      </FieldRow>

      <FieldRow
        label="入库时刷新"
        tip="启用后 STRM 生成时自动通知 Emby 刷新对应媒体库（用上方服务器地址与 API 密钥）。"
      >
        <HSegmented v-model="model.refresh_enabled" :options="[{ label: '启用', value: true }, { label: '禁用', value: false }]" />
      </FieldRow>

      <FormActions>
        <HButton variant="primary" :loading="saving" @click="saveEmby">保存配置</HButton>
        <HButton variant="tertiary" @click="reset">重置配置</HButton>
      </FormActions>

      <HAlert status="accent" class="note">
        配置 Emby 连接后，STRM 生成时按库刷新对应媒体库，整理入库更快。非必须——开启 Emby
        实时监控也一样，此通知只是能更快入库。
      </HAlert>
    </SectionCard>

    <SectionCard title="Emby Webhook" hint="入库 / 删除 / 播放时的消息通知">
      <HAlert status="accent" class="note-top">
        把下方地址填到 Emby 的 Webhooks（请求方式 POST，内容类型 application/json）。
        推送哪些事件由 Emby 侧勾选决定；token 为本实例自动生成的鉴权密钥。
      </HAlert>

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
        <div class="h-field-row">
          <HInput v-model="notifyToken" placeholder="自动生成，可自定义" @blur="saveToken(notifyToken)" />
          <HButton variant="tertiary" @click="rotateToken">换一个</HButton>
        </div>
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
.path-pair {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto minmax(0, 1fr);
  gap: 10px;
  align-items: end;
}
.path-pair label {
  min-width: 0;
}
.path-pair label > span {
  display: block;
  margin-bottom: 5px;
  font-size: 12px;
  color: var(--c-text-3);
}
.detect {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-top: 8px;
}
.detect-note {
  font-size: 12px;
  color: var(--muted);
  word-break: break-all;
}
.local-side {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto minmax(0, 1fr);
  gap: 6px;
  align-items: center;
}
.local-side .sep {
  color: var(--c-text-3);
}
.path-arrow {
  padding-bottom: 8px;
  color: var(--c-text-3);
}
.location-link {
  margin-top: 6px;
}
.note {
  margin-top: 16px;
}
.note-top {
  margin-bottom: 8px;
}
@media (max-width: 720px) {
  .path-pair {
    grid-template-columns: 1fr;
  }
  .path-arrow {
    display: none;
  }
}
</style>
