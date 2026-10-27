<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HNumberInput from '@/components/hero/HNumberInput.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import { playbackApi } from '@/api'
import type { PlaybackConfig, PlaybackEmbyUsers, PlayMode, ProxyStatus } from '@/api/playback'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { bytes } from '@/utils/format'
import { relTime } from '@/utils/time'

/**
 * 播放方式：302 直连 / 本机中转（后端 playmode.go / playproxy.go，设计见 PROXY-PLAY-PLAN.md）。
 * 单独一个 setting「playback」，只有这张卡片写它（整份提交不会冲掉别的页面）。
 * 按 Emby 用户分只对经本站 Emby 反代（6086/emby）的客户端有效：后端只有那条路上核对过用户身份。
 */
const props = defineProps<{ domain?: string }>()
const { message, dialog } = useFeedback()

const DEFAULT: PlaybackConfig = { mode: 'redirect', users: {}, max_conns: 3, transcode: false }
const saved = ref<PlaybackConfig>({ ...DEFAULT })
const model = ref<PlaybackConfig>({ ...DEFAULT, users: {} })
const loading = ref(true)
const saving = ref(false)
const emby = ref<PlaybackEmbyUsers | null>(null)
const embyLoading = ref(false)
const status = ref<ProxyStatus | null>(null)
const liveOpen = ref(true)

const clone = (c: PlaybackConfig): PlaybackConfig => ({ ...c, users: { ...c.users } })
const dirty = computed(() => JSON.stringify(model.value) !== JSON.stringify(saved.value))
const anyProxy = computed(
  () => model.value.mode === 'proxy' || Object.values(model.value.users).includes('proxy'),
)

async function load() {
  loading.value = true
  try {
    const c = await playbackApi.config()
    c.users ??= {}
    saved.value = clone(c)
    model.value = clone(c)
  } catch (e) {
    toastError(e, '读取播放方式失败')
  } finally {
    loading.value = false
  }
}

async function loadUsers() {
  embyLoading.value = true
  try {
    emby.value = await playbackApi.embyUsers()
  } catch (e) {
    emby.value = { configured: true, error: e instanceof Error ? e.message : String(e), users: [] }
  } finally {
    embyLoading.value = false
  }
}

async function loadStatus() {
  try {
    status.value = await playbackApi.proxyStatus()
  } catch {
    // 状态读不到维持上一次的
  }
}

let timer: number | undefined
onMounted(() => {
  void load()
  void loadUsers()
  void loadStatus()
  timer = window.setInterval(() => {
    if (!document.hidden) void loadStatus()
  }, 5000)
})
onUnmounted(() => clearInterval(timer))

// ---- 按 Emby 用户 ----
type Pick = '' | PlayMode
const userPick = (id: string): Pick => model.value.users[id] ?? ''
function setUser(id: string, v: Pick) {
  const u = { ...model.value.users }
  if (v) u[id] = v
  else delete u[id]
  model.value.users = u
}
const effective = (id: string): PlayMode => model.value.users[id] ?? model.value.mode
const userOptions = computed(() => [
  { label: model.value.mode === 'proxy' ? '跟随默认（中转）' : '跟随默认（302）', value: '' as Pick },
  { label: '302', value: 'redirect' as Pick },
  { label: '中转', value: 'proxy' as Pick },
])
// 设置里有、Emby 里已经没有的用户（删了或换了 Emby）
const goneUsers = computed(() => {
  if (!emby.value?.configured || emby.value.error) return []
  const ids = new Set(emby.value.users.map((u) => u.id))
  return Object.keys(model.value.users).filter((id) => !ids.has(id))
})
const proxyUrl = computed(() => {
  const d = (props.domain || '').trim().replace(/\/+$/, '')
  return d ? `${d}/emby` : 'http://本机:6086/emby'
})

// ---- 保存 ----
async function save() {
  if (model.value.mode === 'proxy' && saved.value.mode !== 'proxy') {
    const ok = await dialog.confirm<boolean>({
      title: '默认改成本机中转？',
      content:
        '知道 STRM 地址的任何播放器（直连 Emby 8096、Infuse 直接读 STRM 等）都会用你家的上行带宽。只想让某几个人中转的话，默认保持 302，按 Emby 用户单独设。',
      tone: 'warning',
      actions: [
        { label: '取消', value: false, variant: 'tertiary' },
        { label: '改成中转', value: true, variant: 'primary' },
      ],
    })
    if (!ok) return
  }
  saving.value = true
  try {
    const c = await playbackApi.saveConfig(model.value)
    c.users ??= {}
    saved.value = clone(c)
    model.value = clone(c)
    message.success('已保存，下一次起播生效')
    void loadStatus()
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}
function reset() {
  model.value = clone(saved.value)
}

// ---- 正在中转 ----
const rate = (bps: number) => `${((bps * 8) / 1e6).toFixed(1)} Mbps`
const hhmm = (s: string) => {
  const d = new Date(s)
  return Number.isNaN(d.getTime()) ? '' : d.toTimeString().slice(0, 5)
}
const MODE_OPTIONS: { value: PlayMode; title: string; text: string }[] = [
  {
    value: 'redirect',
    title: '302 直连',
    text: '播放器直接连 115 下载，不经过这台机器、不占家里上行。播放器要能连上 115。',
  },
  {
    value: 'proxy',
    title: '本机中转',
    text: '这台机器从 115 拉，再转给播放器，走家里的上行带宽。115 的流量都从家里一个 IP 出去。',
  },
]
</script>

<template>
  <SectionCard
    id="strm-playback"
    title="播放方式"
    hint="播放本站 STRM 时，视频从 115 直接到播放器，还是先到这台机器再转给播放器。只影响播放，不用重新生成 STRM。"
  >
    <FieldRow label="默认播放方式" tip="没在下面单独设置的人都按这个。直接读 STRM 的播放器、直连 Emby 8096 的客户端也按这个。">
      <div class="modes" role="radiogroup" aria-label="默认播放方式">
        <button
          v-for="o in MODE_OPTIONS"
          :key="o.value"
          type="button"
          role="radio"
          class="mode"
          :aria-checked="model.mode === o.value"
          :disabled="loading"
          @click="model.mode = o.value"
        >
          <span class="mode-title"><span class="dot" />{{ o.title }}</span>
          <span class="mode-text">{{ o.text }}</span>
        </button>
      </div>
      <HAlert v-if="model.mode === 'proxy'" status="warning" class="gap">
        默认设成中转后，知道 STRM 地址的任何播放器（包括直连 Emby 8096、Infuse 直接读 STRM）都会用你的上行带宽。
        只想让某几个人中转的话，默认保持 302，在下面按 Emby 用户单独设。
      </HAlert>
    </FieldRow>

    <FieldRow label="按 Emby 用户" tip="覆盖上面的默认方式。">
      <div class="users-wrap">
        <template v-if="emby && !emby.configured">
          <HAlert>
            先在 <RouterLink to="/settings?tab=emby">系统配置 → EMBY 管理</RouterLink> 里填好 Emby 地址和 API Key，这里才能列出用户。没配 Emby 时只按上面的默认方式。
          </HAlert>
        </template>
        <template v-else>
          <p class="hint">
            只对通过本站 Emby 反代 <code>{{ proxyUrl }}</code> 连接的客户端生效。直连 Emby（8096）、或直接读 STRM 的播放器，本站认不出是谁，一律按默认方式。
          </p>
          <HAlert v-if="emby?.error" status="danger">读 Emby 用户失败：{{ emby.error }}</HAlert>
          <div class="users-head">
            <span>{{ emby ? `${emby.users.length} 个 Emby 用户` : '正在读 Emby 用户…' }}</span>
            <HButton variant="ghost" size="sm" :loading="embyLoading" @click="loadUsers">刷新用户列表</HButton>
          </div>
          <div v-if="emby?.users.length || goneUsers.length" class="users">
            <div v-for="u in emby?.users ?? []" :key="u.id" class="user" :class="{ off: u.disabled }">
              <div class="av" aria-hidden="true">{{ (u.name || '?').slice(0, 1).toUpperCase() }}</div>
              <div class="who">
                <div class="who-line">
                  <b>{{ u.name || u.id }}</b>
                  <span v-if="u.admin" class="pill">管理员</span>
                  <span v-if="u.disabled" class="pill">已禁用</span>
                  <span class="pill" :class="effective(u.id) === 'proxy' ? 'pill-proxy' : ''">
                    {{ effective(u.id) === 'proxy' ? '中转' : '302' }}
                  </span>
                </div>
                <small v-if="u.via_proxy">最近经本站反代播放：{{ relTime(u.via_proxy) }}</small>
                <small v-else class="warn">本站启动以来没见过它经本站反代播放；它要是直连 Emby，这里的设置对它不起作用</small>
              </div>
              <HSegmented
                size="sm"
                :options="userOptions"
                :model-value="userPick(u.id)"
                :aria-label="`${u.name} 的播放方式`"
                @update:model-value="(v) => setUser(u.id, v as Pick)"
              />
            </div>
            <div v-for="id in goneUsers" :key="id" class="user gone">
              <div class="av" aria-hidden="true">?</div>
              <div class="who">
                <div class="who-line">
                  <b class="mono">{{ id.slice(0, 8) }}…</b>
                  <span class="pill" :class="model.users[id] === 'proxy' ? 'pill-proxy' : ''">
                    {{ model.users[id] === 'proxy' ? '中转' : '302' }}
                  </span>
                </div>
                <small>Emby 里已经没有这个用户</small>
              </div>
              <HButton variant="ghost" size="sm" @click="setUser(id, '')">删除这行设置</HButton>
            </div>
          </div>
        </template>
      </div>
    </FieldRow>

    <FieldRow
      label="同时连接上限"
      hint="每个 115 账号。只算中转的连接，302 的不算。一路播放通常占 1–2 个，默认 3 够两个人同时看。满了新的播放会等最多 10 秒，等不到就提示「连接已满」；被 115 限流时这个账号会自动暂停一会儿。"
    >
      <div class="num">
        <HNumberInput v-model="model.max_conns" :min="1" :max="16" aria-label="同时连接上限" />
      </div>
    </FieldRow>

    <FieldRow label="允许 Emby 转码">
      <div class="col">
        <div class="switch-line">
          <HSwitch v-model="model.transcode" :disabled="!anyProxy" aria-label="允许 Emby 转码">
            {{ model.transcode ? '开' : '关' }}
          </HSwitch>
          <span class="pill pill-exp">实验</span>
        </div>
        <p class="hint">
          只对中转的播放。家里上行推不动原画时（比如上行 40 Mbps、4K 原盘 80 Mbps），让 Emby 按客户端或用户设的码率转码。码率上限在 Emby 里设：用户的「远程连接最大码率」或服务器的「互联网串流码率上限」。
        </p>
        <p class="hint">
          要求片子提前探测过媒体信息（Emby 不知道码率就不会转码），Emby 这台机器 CPU / 显卡吃得消。参考项目都没这样做过，先找一部片试一试。302 直连的播放不受影响，照旧不转码。
        </p>
        <p v-if="!anyProxy" class="hint">现在没有人用中转，这项不起作用。</p>
      </div>
    </FieldRow>

    <FieldRow label="正在中转">
      <p v-if="!status || !status.plays.length" class="hint">
        {{ status?.any_proxy ? '现在没有中转在传。' : '没有人用中转。' }}今天中转了 {{ bytes(status?.today_bytes ?? 0) }}。
      </p>
      <div v-else class="live">
        <button type="button" class="live-head" :aria-expanded="liveOpen" @click="liveOpen = !liveOpen">
          <span class="pulse" aria-hidden="true" />
          <span><b>{{ status.plays.length }}</b> 路在传</span>
          <span>合计 <b>{{ rate(status.rate) }}</b></span>
          <span>今天 <b>{{ bytes(status.today_bytes) }}</b></span>
          <span class="chev">{{ liveOpen ? '收起' : '展开' }}</span>
        </button>
        <div v-if="liveOpen" class="live-body">
          <div class="accts">
            <div v-for="a in status.accounts" :key="a.acct" class="acct" :class="{ cool: a.cool_left > 0 }">
              <div>
                <b>{{ a.name }}</b> ·
                <span class="st">{{ a.cool_left > 0 ? `被 115 限流，暂停 ${a.cool_left} 秒` : '正常' }}</span>
              </div>
              <div class="bar"><i :style="{ width: `${Math.min(100, (a.active / status.max_conns) * 100)}%` }" /></div>
              <div class="st">{{ a.active }} / {{ status.max_conns }} 个连接</div>
            </div>
          </div>
          <div class="tbl">
            <table>
              <thead>
                <tr>
                  <th>谁</th>
                  <th>在看</th>
                  <th>账号</th>
                  <th>播放器 · 来源</th>
                  <th class="r">速度</th>
                  <th class="r">已传</th>
                  <th>开始</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="p in status.plays" :key="p.who + p.name + p.start">
                  <td>{{ p.who }}</td>
                  <td><span class="fn" :title="p.name">{{ p.name }}</span></td>
                  <td>{{ p.acct }}</td>
                  <td><span class="fn" :title="p.ua">{{ p.ua || '—' }} · {{ p.ip }}</span></td>
                  <td class="r">{{ rate(p.rate) }}</td>
                  <td class="r">{{ bytes(p.bytes) }}</td>
                  <td>{{ hhmm(p.start) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <p class="hint">只看这台机器上的中转，重启后今天的流量从 0 算。</p>
        </div>
      </div>
    </FieldRow>

    <FormActions>
      <HButton variant="primary" :loading="saving" :disabled="!dirty || loading" @click="save">保存</HButton>
      <HButton variant="tertiary" :disabled="!dirty" @click="reset">重置</HButton>
      <span v-if="dirty" class="dirty">有未保存的修改</span>
    </FormActions>
  </SectionCard>
</template>

<style scoped>
.modes {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 10px;
  width: 100%;
}
.mode {
  display: flex;
  flex-direction: column;
  gap: 4px;
  text-align: left;
  padding: 12px 14px;
  border-radius: var(--r-lg);
  border: 1px solid var(--border);
  background: var(--surface-secondary);
  color: var(--foreground);
  cursor: pointer;
}
.mode[aria-checked='true'] {
  border-color: var(--accent);
  background: color-mix(in oklab, var(--accent) 12%, transparent);
}
.mode:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}
.mode-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-weight: 600;
}
.mode-text {
  font-size: 12.5px;
  color: var(--muted);
}
.dot {
  width: 14px;
  height: 14px;
  flex: none;
  border-radius: 50%;
  border: 2px solid var(--muted);
}
.mode[aria-checked='true'] .dot {
  border-color: var(--accent);
  background: radial-gradient(var(--accent) 45%, transparent 50%);
}
.gap {
  margin-top: 10px;
}
.hint {
  margin: 0;
  font-size: 12.5px;
  color: var(--muted);
}
.hint code,
.mono {
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 12px;
}
.col,
.users-wrap {
  display: flex;
  flex-direction: column;
  gap: 8px;
  width: 100%;
  min-width: 0;
}
.users-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
  font-size: 12.5px;
  color: var(--muted);
}
.users {
  border: 1px solid var(--border);
  border-radius: var(--r-lg);
  overflow: hidden;
}
.user {
  display: grid;
  grid-template-columns: 34px minmax(0, 1fr) auto;
  align-items: center;
  gap: 4px 12px;
  padding: 10px 12px;
  border-top: 1px solid var(--border);
}
.user:first-child {
  border-top: 0;
}
.user.off {
  opacity: 0.6;
}
.user.gone {
  background: color-mix(in oklab, var(--danger) 7%, transparent);
}
.av {
  width: 34px;
  height: 34px;
  border-radius: 50%;
  display: grid;
  place-items: center;
  font-weight: 650;
  font-size: 13px;
  background: var(--surface-tertiary);
}
.who {
  min-width: 0;
}
.who-line {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.who small {
  display: block;
  font-size: 12px;
  color: var(--muted);
}
.who small.warn {
  color: var(--warning);
}
.pill {
  font-size: 11.5px;
  padding: 1px 7px;
  border-radius: 999px;
  background: var(--surface-tertiary);
  color: var(--muted);
  white-space: nowrap;
}
.pill-proxy {
  background: color-mix(in oklab, var(--accent) 16%, transparent);
  color: var(--accent);
}
.pill-exp {
  background: color-mix(in oklab, var(--warning) 16%, transparent);
  color: var(--warning);
}
.switch-line {
  display: flex;
  align-items: center;
  gap: 10px;
}
.num {
  width: 160px;
}
.live {
  width: 100%;
  border: 1px solid var(--border);
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.live-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 16px;
  width: 100%;
  padding: 10px 14px;
  border: 0;
  background: transparent;
  color: var(--muted);
  font-size: 12.5px;
  text-align: left;
  cursor: pointer;
}
.live-head b {
  color: var(--foreground);
  font-variant-numeric: tabular-nums;
}
.chev {
  margin-left: auto;
}
.pulse {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--success);
  animation: pulse 2s infinite;
}
@keyframes pulse {
  0% {
    box-shadow: 0 0 0 0 color-mix(in oklab, var(--success) 60%, transparent);
  }
  100% {
    box-shadow: 0 0 0 8px transparent;
  }
}
@media (prefers-reduced-motion: reduce) {
  .pulse {
    animation: none;
  }
}
.live-body {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 12px 14px;
  border-top: 1px solid var(--border);
}
.accts {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
}
.acct {
  flex: 1 1 200px;
  padding: 8px 10px;
  border: 1px solid var(--border);
  border-radius: var(--r-sm);
  background: var(--surface);
  font-size: 12.5px;
}
.acct .st {
  color: var(--muted);
}
.acct.cool .st {
  color: var(--warning);
}
.bar {
  height: 6px;
  margin-top: 6px;
  border-radius: 999px;
  background: var(--surface-tertiary);
  overflow: hidden;
}
.bar i {
  display: block;
  height: 100%;
  border-radius: 999px;
  background: var(--accent);
}
.acct.cool .bar i {
  background: var(--warning);
}
.tbl {
  overflow-x: auto;
}
table {
  width: 100%;
  min-width: 560px;
  border-collapse: collapse;
  font-size: 12.5px;
}
th,
td {
  padding: 6px 8px;
  border-bottom: 1px solid var(--border);
  text-align: left;
  white-space: nowrap;
}
th {
  color: var(--muted);
  font-weight: 500;
}
.r {
  text-align: right;
  font-variant-numeric: tabular-nums;
}
.fn {
  display: inline-block;
  max-width: 260px;
  overflow: hidden;
  text-overflow: ellipsis;
  vertical-align: bottom;
}
.dirty {
  font-size: 12.5px;
  color: var(--warning);
}
@media (max-width: 720px) {
  .modes {
    grid-template-columns: 1fr;
  }
  .user {
    grid-template-columns: 34px minmax(0, 1fr);
  }
  .user > :last-child {
    grid-column: 1 / -1;
  }
}
</style>
