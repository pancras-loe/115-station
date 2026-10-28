<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import HButton from '@/components/hero/HButton.vue'
import HHelpTip from '@/components/hero/HHelpTip.vue'
import HInput from '@/components/hero/HInput.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import HAlert from '@/components/hero/HAlert.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import SecretInput from '@/components/ui/SecretInput.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import TestBanner, { type BannerState } from '@/components/ui/TestBanner.vue'
import CopyBox from '@/components/ui/CopyBox.vue'
import SaveBar from '@/pages/scrape/SaveBar.vue'
import { configApi, localApi } from '@/api'
import { plainProps } from '@/utils/autofill'
import { useSetting } from '@/composables/useSetting'
import { useFullSetting } from '@/pages/strm/fullSetting'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { useMediaServer } from '@/composables/mediaServer'

const { message } = useFeedback()
const router = useRouter()
const route = useRoute()
const media = useFullSetting()
const ms = useMediaServer()

const { model, loading, saving, dirty: modelDirty, saved, save } = useSetting('emby', {
  server_url: '',
  api_key: '',
  path_mapping: '',
  style: 'unix',
  refresh_enabled: true,
  /** 服务器类型：空 = 按 /System/Info/Public 自动识别（后端 mediaserver.go） */
  kind: '',
})

const KIND_OPTIONS = [
  { label: '自动识别', value: '' },
  { label: 'Emby', value: 'emby' },
  { label: 'Jellyfin', value: 'jellyfin' },
]
/** 测试连接认出来的类型，没测过就看后端当前判定 */
const testedKind = ref('')
const effectiveKind = computed(() => model.value.kind || testedKind.value || ms.kind.value)
const serverName = computed(() => (effectiveKind.value === 'jellyfin' ? 'Jellyfin' : 'Emby'))

const banner = ref<BannerState | null>(null)
const testing = ref(false)
const pathProbe = ref('')
const embyMediaRoot = ref('')
/** 映射本地这一侧，空 = 跟随本地媒体根（后端 embyPathRoots 同口径） */
const localPath = ref('')
/**
 * 本地媒体根固定（MEDIA_ROOT，默认 /media），映射本地一侧默认就是它、只填 Emby 那一侧。
 * Emby 只挂了媒体根下的某个子目录（Emby /Movies ⇄ 本地 /media/电影）时才要改本地一侧：收在「改本地一侧」里
 */
const localCustom = ref(false)

const norm = (p: string) => p.trim().replace(/\\/g, '/').replace(/(.)\/+$/, '$1')
const isAbs = (p: string) => p.startsWith('/') || /^[A-Za-z]:/.test(p)
const mediaRoot = computed(() => norm(media.model.value.local_path || ''))

/** 存的规则拆成表单两侧 */
function splitMapping(rule: string, root: string) {
  const splitAt = rule.indexOf('#')
  const l = splitAt >= 0 ? norm(rule.slice(0, splitAt)) : ''
  return {
    emby: splitAt >= 0 ? rule.slice(splitAt + 1) : '',
    // v26.10.10-6 存的是相对子目录：显示成完整路径，保存时按绝对路径写回
    local: !l || isAbs(l) ? l : root ? `${root}/${l.replace(/^\.\//, '')}` : '',
  }
}

// 本地一侧和本地媒体库目录一样时存空（跟随）：以后改了本地目录，映射跟着走
function joinMapping(local: string, emby: string, root: string): string {
  const embyRoot = emby.trim()
  const l = norm(local)
  return embyRoot ? `${l === root ? '' : l}#${embyRoot}` : ''
}

watch(
  [() => model.value.path_mapping, mediaRoot],
  ([rule, root]) => {
    const m = splitMapping(rule, root)
    embyMediaRoot.value = m.emby
    localPath.value = m.local
    if (m.local && m.local !== root) localCustom.value = true
  },
  { immediate: true },
)

/** 实际生效的本地这一侧 */
const localSide = computed(() => norm(localPath.value) || mediaRoot.value)

/** 填了却不在本地媒体库目录下：本站的 STRM 都在那下面，填到外面一定对不上 */
const localSideWarn = computed(() => {
  const l = norm(localPath.value)
  const root = mediaRoot.value
  if (!l) return ''
  if (!isAbs(l)) return '请填绝对路径（以 / 开头）'
  if (root && l !== root && !l.startsWith(root + '/') && !root.startsWith(l + '/'))
    return `不在本地媒体库目录 ${root} 下，本站生成的 STRM 都在那里面，这样填对不上`
  return ''
})

// ---- 映射两侧：按 Emby 媒体库的路径推算（/emby/path-suggest），只填进表单，保存由用户点 ----
const detecting = ref(false)
const detectNote = ref('')

function applySuggest(root: string, local: string, how: string) {
  // 推算的本地一侧就是本地媒体库目录时留空（跟随），以后改了本地目录不用回来再改
  const l = norm(local) === mediaRoot.value ? '' : norm(local)
  const desc = l ? `本地 ${l} ⇄ Emby ${root}` : root
  if (root === embyMediaRoot.value.trim() && l === norm(localPath.value)) {
    detectNote.value = `推算结果与当前一致：${desc}`
    return
  }
  embyMediaRoot.value = root
  localPath.value = l
  if (l) localCustom.value = true
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
      applySuggest(d.suggest, d.suggest_local || '', ev ? `按 Emby「${ev.location}」⇄ 本地「${ev.local}」` : '')
    }
  } catch (e) {
    toastError(e, '检测失败')
  } finally {
    detecting.value = false
  }
}

// 从对账弹窗跳过来时带着推算值（?emby_root= &emby_local=）：等配置读完再填，否则会被读回来的旧值盖掉
watch(
  loading,
  (busy) => {
    const root = typeof route.query.emby_root === 'string' ? route.query.emby_root.trim() : ''
    if (busy || !root) return
    const local = typeof route.query.emby_local === 'string' ? route.query.emby_local.trim() : ''
    applySuggest(root, local, '对账推算的值')
    router.replace({ query: { ...route.query, emby_root: undefined, emby_local: undefined } })
  },
  { immediate: true },
)

function currentPathMapping(): string {
  return joinMapping(localPath.value, embyMediaRoot.value, mediaRoot.value)
}

/**
 * 映射两侧是拆开编辑的，保存时才拼回 path_mapping，所以拿已保存的规则走一遍拆 → 拼再比。
 * 不直接和 path_mapping 比：老格式（相对子目录、写着本地根）读进来就和拼回去的不一样，一打开就报未保存
 */
const dirty = computed(() => {
  const m = splitMapping(saved.value.path_mapping, mediaRoot.value)
  return modelDirty.value || currentPathMapping() !== joinMapping(m.local, m.emby, mediaRoot.value)
})

/**
 * 新用户最常卡在映射：Emby 连上了、映射还空着时，替他推算一次（只填表单，保存仍由用户点）。
 * 推算读的是**已保存**的 Emby 地址与密钥（/emby/path-suggest），所以只在表单这两项已保存时做；
 * 用户填过映射的一律不碰
 */
function autoDetectIfEmpty() {
  if (embyMediaRoot.value.trim() || detecting.value) return
  const s = saved.value
  if (s.server_url.trim() !== model.value.server_url.trim() || s.api_key.trim() !== model.value.api_key.trim()) return
  if (!s.server_url.trim() || !s.api_key.trim()) return
  detectEmbyRoot()
}

async function saveEmby() {
  model.value.path_mapping = currentPathMapping()
  if (await save()) {
    ms.reload()
    autoDetectIfEmpty()
  }
}

async function testConnection() {
  const url = model.value.server_url.trim()
  if (!url) {
    banner.value = { status: 'err', title: '请先填写服务器地址' }
    return
  }
  testing.value = true
  banner.value = { status: 'pending', title: `正在连接 ${serverName.value} 服务器…` }
  try {
    const d = await configApi.testEmby({ server_url: url, api_key: model.value.api_key.trim() })
    if (d.ok && d.kind) testedKind.value = d.kind
    const product = d.product || serverName.value
    const mismatch = d.ok && model.value.kind && d.kind && model.value.kind !== d.kind
    banner.value = d.ok
      ? {
          status: mismatch ? 'err' : 'ok',
          title: mismatch ? `连上了，但这是 ${product}，和上面选的类型对不上` : `${product} 连接成功`,
          detail: `${d.server_name || product}${d.version ? ` · v${d.version}` : ''} · ${d.library_count ?? 0} 个媒体库`,
        }
      : { status: 'err', title: '连接失败', detail: d.error || '未知错误' }
    if (d.ok) autoDetectIfEmpty()
  } catch (e) {
    banner.value = { status: 'err', title: '连接失败', detail: e instanceof Error ? e.message : '' }
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
watch(
  () => [model.value.server_url, model.value.api_key],
  () => {
    banner.value = null
    testedKind.value = ''
  },
)
</script>

<template>
  <!-- 一屏放下：连接与 Webhook 并排，路径映射与转换测试同一张网格上下对齐，保存条收在最后 -->
  <div class="tab">
    <div class="grid">
      <SectionCard title="媒体服务器连接" hint="入库刷新、对账、海报墙的媒体信息都用这里的地址与密钥">
        <FieldRow
          stacked
          label="服务器类型"
          tip="Emby 与 Jellyfin 同源，大部分功能两边都能用；少数几项 Jellyfin 做不了，会在对应页面灰掉并写明原因。自动识别读的是服务器的公开信息，一般不用改。"
        >
          <HSegmented v-model="model.kind" :options="KIND_OPTIONS" />
        </FieldRow>

        <FieldRow
          stacked
          label="服务器地址"
          required
          tip="Emby / Jellyfin 访问地址（本容器内可达，如 http://172.17.0.1:8096）。6086 反代与此处配置联动。"
        >
          <HInput v-model="model.server_url" placeholder="如 http://192.168.1.100:8096" :input-attrs="plainProps('emby-server-url')" />
        </FieldRow>

        <FieldRow stacked label="API 密钥" tip="Emby 后台「设置 → API 密钥」、Jellyfin 后台「控制台 → API 密钥」生成，用于按库刷新与连接测试。">
          <div class="h-field-row">
            <SecretInput
              v-model="model.api_key"
              name="emby-api-key"
              :reveal="{ key: 'emby', field: 'api_key' }"
              placeholder="在服务器后台的 API 密钥里生成"
            />
            <HButton variant="tertiary" :loading="testing" @click="testConnection">测试连接</HButton>
          </div>
          <TestBanner :state="banner" />
        </FieldRow>

        <FieldRow
          stacked
          label="入库时刷新"
          tip="非必须：Emby 开着实时监控也能入库，这里只是让入库更快。"
        >
          <HSwitch v-model="model.refresh_enabled">STRM 生成后通知 {{ serverName }} 刷新对应媒体库</HSwitch>
        </FieldRow>

        <HAlert v-if="effectiveKind === 'jellyfin'" status="warning" title="Jellyfin 与 Emby 的几处不同">
          <ul class="jf-notes">
            <li>客户端直连 Jellyfin 播放 STRM 时，由 Jellyfin 自己去拉流，流量经过它所在的机器；要 302 直连，客户端要连本站的反代端口</li>
            <li>Jellyfin 每次播放 STRM 都会重新探测一次，多取一次 115 直链；所以提前探测在 Jellyfin 下关闭</li>
            <li>Webhook 要装官方插件「Webhook」（设置步骤在右边）；它的删除事件不带文件路径，Emby 删条目联动删网盘（深度删除）用不了</li>
            <li>建媒体库时记得关掉「缩略进度图（Trickplay）」，它要对整部片抽帧</li>
          </ul>
        </HAlert>
      </SectionCard>

      <SectionCard :title="`${serverName} Webhook`" hint="入库 / 删除 / 播放时的消息通知，改动即时生效">
        <FieldRow
          stacked
          label="接收地址"
          :tip="
            effectiveKind === 'jellyfin'
              ? '在 Jellyfin 插件目录装「Webhook」，添加一个 Generic Destination 填这个地址；推送哪些事件在插件里勾选。'
              : '填到 Emby 的 Webhooks，请求方式 POST、内容类型 application/json；推送哪些事件在 Emby 里勾选。'
          "
          hint="主机部分换成 Emby 访问得到的地址：同宿主机容器用 172.17.0.1，局域网用内网 IP，跨网用公网域名；路径与 token 不变。"
        >
          <CopyBox :value="webhookUrl" />
        </FieldRow>

        <ol v-if="effectiveKind === 'jellyfin'" class="jf-steps">
          <li>Jellyfin「控制台 → 插件 → 目录」安装「Webhook」，装完重启 Jellyfin</li>
          <li>打开插件设置，点「Add Generic Destination」</li>
          <li>Webhook Url 填上面的接收地址</li>
          <li>Notification Type 勾「Item Added」「Playback Start」「Playback Stop」</li>
          <li>Item Type 勾「Movies」「Episodes」（剧集按整部报就勾「Series」）</li>
          <li>勾上「Send All Properties (ignores template)」，模板留空，保存</li>
          <li>插件没有测试按钮：播一个视频，深度删除页会显示收到过事件</li>
          <li class="muted">删除事件（Item Deleted）不带文件路径，分不清是不是本站自己删的，本站只记日志、不推通知</li>
        </ol>

        <FieldRow
          stacked
          label="鉴权 token"
          tip="Emby 推送时必须带相同 token，否则返回 401。可改成好记的（4-64 位字母数字），改完 Emby 侧地址要同步更新。"
        >
          <div class="h-field-row">
            <HInput v-model="notifyToken" placeholder="自动生成，可自定义" @blur="saveToken(notifyToken)" />
            <HButton variant="tertiary" @click="rotateToken">换一个</HButton>
          </div>
        </FieldRow>
      </SectionCard>
    </div>

    <SectionCard title="路径映射" hint="同一个目录在本站与 Emby 里各是什么路径；两个容器挂同一个目录时，本地一侧留空即可">
      <template #extra>
        <HButton size="sm" variant="tertiary" :loading="detecting" @click="detectEmbyRoot">自动检测</HButton>
      </template>

      <div class="map">
        <label v-if="localCustom" class="cell">
          <span class="cell-label">
            本地路径（留空 = 本地媒体根）
            <HButton variant="ghost" size="sm" class="text-btn" @click="((localPath = ''), (localCustom = false))">恢复默认</HButton>
          </span>
          <HInput v-model="localPath" :placeholder="mediaRoot || '/media'" />
        </label>
        <div v-else class="cell">
          <span class="cell-label">
            本地媒体根
            <HHelpTip text="本站的 STRM 都在这个目录下（由容器环境变量 MEDIA_ROOT 决定，默认 /media，界面上不能改）。Emby 只挂了它下面的某个子目录时，点「改本地一侧」。" />
          </span>
          <div class="root-ro">
            <code>{{ mediaRoot || '/media' }}</code>
            <HButton variant="ghost" size="sm" class="text-btn" @click="localCustom = true">改本地一侧</HButton>
          </div>
        </div>
        <span class="arrow">→</span>
        <label class="cell">
          <span class="cell-label">{{ serverName }} 媒体库目录</span>
          <HInput v-model="embyMediaRoot" placeholder="如 /media" />
        </label>
        <div class="cell">
          <span class="cell-label">
            路径风格
            <HHelpTip text="通知 Emby 的路径分隔符：Unix（/）或 Windows（\），与 Emby 挂载方式一致。" />
          </span>
          <HSegmented v-model="model.style" :options="[{ label: 'Unix', value: 'unix' }, { label: 'Windows', value: 'windows' }]" />
        </div>

        <label class="cell">
          <span class="cell-label">转换测试：输入一个本地路径</span>
          <HInput v-model="pathProbe" placeholder="如 /media/电影/钢铁侠.mkv" />
        </label>
        <span class="arrow">→</span>
        <div class="cell span-rest">
          <span class="cell-label">{{ serverName }} 里的路径</span>
          <CopyBox v-if="pathResult" :value="pathResult" tone="success" />
          <div v-else class="probe-empty">输入左边的路径后显示，与 Emby 里的一致即映射正确</div>
        </div>
      </div>

      <p v-if="localSideWarn" class="side-warn">{{ localSideWarn }}</p>
      <p v-if="detectNote" class="detect-note">{{ detectNote }}</p>
      <p class="map-foot">
        每个媒体库在 Emby 里该填的路径，见「账号与媒体库 → 媒体库」的库卡片
        <HButton variant="ghost" size="sm" class="text-btn" @click="router.push({ name: 'accounts', query: { tab: 'library' } })">
          去看看
        </HButton>
      </p>
    </SectionCard>

    <SaveBar :dirty="dirty" :saving="saving" note="连接、入库刷新与路径映射一起保存；Webhook 的 token 改完即生效" @save="saveEmby" />
  </div>
</template>

<style scoped>
.jf-steps {
  margin: 4px 0 0;
  padding-left: 20px;
  list-style: decimal;
  font-size: 12.5px;
  line-height: 1.8;
  color: var(--muted);
}
.jf-steps .muted {
  list-style: none;
  margin-left: -20px;
  margin-top: 4px;
}
.jf-notes {
  margin: 4px 0 0;
  padding-left: 18px;
  list-style: disc;
  font-size: 12.5px;
  line-height: 1.7;
}
.root-ro {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 36px;
}
.tab {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
  align-items: stretch;
}

/* 映射与转换测试共用一张网格：两行的输入框、箭头上下对齐，一眼看出「这边进、那边出」 */
.map {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto minmax(0, 1fr) auto;
  gap: 12px 10px;
  align-items: end;
}
.cell {
  min-width: 0;
}
.cell-label {
  display: flex;
  align-items: center;
  gap: 4px;
  margin-bottom: 5px;
  font-size: 12px;
  color: var(--c-text-3);
}
.span-rest {
  grid-column: 3 / -1;
}
.arrow {
  padding-bottom: 8px;
  color: var(--c-text-3);
}
.probe-empty {
  display: flex;
  align-items: center;
  min-height: 36px;
  padding: 0 12px;
  border-radius: var(--r-lg);
  box-shadow: inset 0 0 0 1px var(--border);
  font-size: 12px;
  color: var(--muted);
}
.side-warn {
  margin: 10px 0 0;
  font-size: 12px;
  color: var(--warning);
}
.detect-note {
  margin: 10px 0 0;
  font-size: 12px;
  color: var(--muted);
  word-break: break-all;
}
.map-foot {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px 8px;
  margin: 10px 0 0;
  font-size: 12px;
  color: var(--muted);
}
.map-foot code {
  word-break: break-all;
}

@media (max-width: 960px) {
  .grid {
    grid-template-columns: 1fr;
  }
}
@media (max-width: 720px) {
  .map {
    grid-template-columns: 1fr;
  }
  .arrow {
    display: none;
  }
  .span-rest {
    grid-column: auto;
  }
}
</style>
