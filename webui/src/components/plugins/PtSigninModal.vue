<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { MoreHorizontal, Play, Plus } from '@lucide/vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HDropdown, { type MenuOption } from '@/components/hero/HDropdown.vue'
import HModal from '@/components/hero/HModal.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HTabs from '@/components/hero/HTabs.vue'
import { heroTone } from '@/components/hero/tone'
import EmptyState from '@/components/ui/EmptyState.vue'
import StatusStrip, { type StripItem } from '@/components/ui/StatusStrip.vue'
import PtSiteDialog from './ptsignin/PtSiteDialog.vue'
import PtRecords from './ptsignin/PtRecords.vue'
import PtSettings from './ptsignin/PtSettings.vue'
import { RESULT_LABEL, failLabel, handlerLabel, logoHue } from './ptsignin/pt'
import { pluginsApi } from '@/api'
import type { PtConfig, PtPreset, PtSite } from '@/api/plugins'
import { toastError, useFeedback } from '@/composables/useFeedback'

/**
 * PT 站签到（ptsignin.go，设计见 PT-SIGNIN-PLAN.md）：站点卡片墙 / 记录 / 设置。
 * 状态条只看不做；执行按钮在顶上与卡片菜单里。
 */
const show = defineModel<boolean>('show', { default: false })
const { message, dialog } = useFeedback()

type Tab = 'sites' | 'records' | 'settings'
const tab = ref<Tab>('sites')
const sites = ref<PtSite[]>([])
const presets = ref<PtPreset[]>([])
const cfg = ref<PtConfig | null>(null)
const running = ref(false)
const busyId = ref<number | null>(null)
const loading = ref(false)
const filter = ref<'all' | 'fix' | 'ok' | 'login' | 'off'>('all')
const dlgShow = ref(false)
const editing = ref<PtSite | null>(null)
let poll: ReturnType<typeof setTimeout> | null = null

async function load() {
  loading.value = true
  try {
    const [s, c] = await Promise.all([pluginsApi.ptSites(), pluginsApi.ptConfig()])
    sites.value = s.data ?? []
    running.value = s.running
    cfg.value = c
    if (!presets.value.length) presets.value = (await pluginsApi.ptPresets()).data ?? []
  } catch (e) {
    toastError(e, '读站点失败')
  } finally {
    loading.value = false
  }
  if (running.value) schedulePoll()
}

function schedulePoll() {
  if (poll) clearTimeout(poll)
  poll = setTimeout(async () => {
    poll = null
    if (!show.value) return
    try {
      const s = await pluginsApi.ptSites()
      sites.value = s.data ?? []
      const was = running.value
      running.value = s.running
      if (s.running) schedulePoll()
      else if (was) {
        message.success('这一轮跑完了')
        cfg.value = await pluginsApi.ptConfig()
      }
    } catch {
      schedulePoll()
    }
  }, 3000)
}

watch(show, (v) => {
  if (v) {
    tab.value = 'sites'
    load()
  } else if (poll) {
    clearTimeout(poll)
    poll = null
  }
})
onBeforeUnmount(() => poll && clearTimeout(poll))

// ---- 状态 ----
const active = computed(() => sites.value.filter((s) => s.enabled))
const count = (r: string) => active.value.filter((s) => s.today?.result === r).length
const needFix = computed(() => active.value.filter((s) => s.need_fix || s.today?.result === 'fail').length)

const strip = computed<StripItem[]>(() => {
  const n = active.value.length
  const ok = count('ok')
  return [
    { key: 'ok', title: '今天签到成功', tone: n === 0 ? 'off' : ok === n ? 'ok' : 'warn', text: `${ok} / ${n} 个站` },
    { key: 'login', title: '只确认登录', tone: count('login') ? 'warn' : 'off', text: `${count('login')} 个`, tip: '保号有效，签到没确认' },
    { key: 'fix', title: '需要处理', tone: needFix.value ? 'danger' : 'ok', text: needFix.value ? `${needFix.value} 个` : '没有' },
    {
      key: 'next',
      title: '下次执行',
      tone: cfg.value?.enabled ? 'ok' : 'off',
      text: running.value ? '正在跑' : cfg.value?.enabled ? cfg.value.next_run || '—' : '定时没开',
      action: cfg.value?.enabled ? undefined : '去开',
    },
  ]
})

const filters = computed(() => [
  { label: `全部 ${sites.value.length}`, value: 'all' as const },
  { label: `需要处理 ${needFix.value}`, value: 'fix' as const },
  { label: '签到成功', value: 'ok' as const },
  { label: '只确认登录', value: 'login' as const },
  { label: '只保号 / 停用', value: 'off' as const },
])
const shown = computed(() =>
  sites.value.filter((s) => {
    switch (filter.value) {
      case 'fix':
        return s.enabled && (s.need_fix || s.today?.result === 'fail')
      case 'ok':
      case 'login':
        return s.enabled && s.today?.result === filter.value
      case 'off':
        return !s.enabled || s.mode === 'keep'
    }
    return true
  }),
)

const tabs = computed(() => [
  { value: 'sites' as const, label: '站点', count: needFix.value, countTone: 'danger' as const },
  { value: 'records' as const, label: '记录' },
  { value: 'settings' as const, label: '设置' },
])

// ---- 卡片 ----
function pill(s: PtSite): { text: string; tone: ReturnType<typeof heroTone> } {
  if (busyId.value === s.id) return { text: '签到中', tone: heroTone('accent') }
  if (!s.enabled) return { text: '停用', tone: heroTone('default') }
  const t = s.today
  if (!t) return { text: running.value ? '排队中' : '今天还没跑', tone: heroTone('default') }
  if (t.result === 'fail') return { text: failLabel(t.reason), tone: heroTone('danger') }
  return { text: RESULT_LABEL[t.result], tone: heroTone(t.result === 'ok' ? 'success' : 'accent') }
}
function statusText(s: PtSite): string {
  if (s.today) return `${s.today.message} · ${s.today.at}`
  if (s.need_fix) return '上次凭据失效，修改后再试'
  return s.mode === 'keep' ? '只保号：每天访问一次首页' : ''
}
function menu(s: PtSite): MenuOption[] {
  return [
    { key: 'run', label: s.mode === 'keep' ? '立即访问首页' : '立即签到这个站' },
    { key: 'edit', label: '修改 Cookie / 设置' },
    { key: 'toggle', label: s.enabled ? '停用' : '启用' },
    { key: 'del', label: '移除', danger: true },
  ]
}

async function onMenu(s: PtSite, key: string) {
  if (key === 'run') return runOne(s)
  if (key === 'edit') {
    editing.value = s
    dlgShow.value = true
    return
  }
  if (key === 'toggle') {
    try {
      await pluginsApi.ptUpdateSite(s.id, { name: s.name, base_url: s.base_url, ua: s.ua, proxy: s.proxy, mode: s.mode, enabled: !s.enabled })
      await load()
    } catch (e) {
      toastError(e, '保存失败')
    }
    return
  }
  if (key === 'del') {
    const ok = await dialog.confirm({
      title: `移除 ${s.name}`,
      content: '连同这个站的签到记录一起删掉。Cookie 也会删掉，再加要重新贴。',
      tone: 'danger',
      actions: [
        { label: '取消', value: false, variant: 'tertiary' },
        { label: '移除', value: true, variant: 'danger' },
      ],
    })
    if (!ok) return
    try {
      await pluginsApi.ptDeleteSite(s.id)
      message.success(`已移除 ${s.name}`)
      await load()
    } catch (e) {
      toastError(e, '移除失败')
    }
  }
}

async function runOne(s: PtSite) {
  busyId.value = s.id
  try {
    const d = await pluginsApi.ptRunOne(s.id)
    const r = d.data
    if (r.result === 'fail') message.error(`${s.name}：${r.message}`)
    else message.success(`${s.name}：${r.message}`)
  } catch (e) {
    toastError(e, `${s.name} 签到失败`)
  } finally {
    busyId.value = null
    await load()
  }
}

async function runAll() {
  try {
    const d = await pluginsApi.ptRunAll()
    message.success(d.message || '开始签到')
    running.value = true
    schedulePoll()
  } catch (e) {
    toastError(e, '没能开始')
  }
}

function openAdd() {
  editing.value = null
  dlgShow.value = true
}
function onStrip(key: string) {
  if (key === 'next') tab.value = 'settings'
}
</script>

<template>
  <HModal v-model:show="show" title="PT 站签到" width="1040px">
    <div class="top">
      <StatusStrip :items="strip" @act="onStrip" />
      <div class="acts">
        <HButton variant="secondary" size="sm" @click="openAdd">
          <template #icon><Plus :size="14" /></template>
          添加站点
        </HButton>
        <HButton variant="primary" size="sm" :loading="running" :disabled="!active.length" @click="runAll">
          <template #icon><Play :size="14" /></template>
          {{ running ? '签到中' : '立即签到全部' }}
        </HButton>
      </div>
    </div>

    <HTabs v-model="tab" :items="tabs" variant="secondary" />

    <div v-if="tab === 'sites'" class="pane">
      <div class="bar">
        <HSegmented v-model="filter" :options="filters" size="sm" />
        <div class="legend">
          <span><i class="d-ok" />签到成功</span><span><i class="d-login" />只确认登录</span><span><i class="d-fail" />失败</span><span>· 近 7 天，最右是今天</span>
        </div>
      </div>
      <EmptyState v-if="!loading && !sites.length" text="还没有站点。点「添加站点」，从目录里选一个站、贴上 Cookie 就能用。" />
      <div v-else class="grid">
        <div v-for="s in shown" :key="s.id" class="site" :class="{ fix: s.enabled && (s.need_fix || s.today?.result === 'fail'), off: !s.enabled }">
          <div class="head">
            <span class="logo" :style="{ '--h': logoHue(s.name) }">{{ s.name.slice(0, 1) }}</span>
            <div class="nm">
              <div class="name">{{ s.name }}</div>
              <div class="host">{{ s.base_url.replace(/^https?:\/\//, '').replace(/\/$/, '') }}</div>
            </div>
            <HDropdown :options="menu(s)" align="end" @select="(k: string) => onMenu(s, k)">
              <HButton variant="ghost" size="sm" icon-only aria-label="更多操作" :disabled="busyId === s.id">
                <MoreHorizontal :size="16" />
              </HButton>
            </HDropdown>
          </div>
          <div class="status">
            <HChip size="sm" :color="pill(s).tone">{{ pill(s).text }}</HChip>
            <span class="msg">{{ statusText(s) }}</span>
          </div>
          <div class="foot">
            <div class="dots" title="近 7 天">
              <span v-for="(d, i) in s.week" :key="i" class="dot" :class="d ? `d-${d}` : ''" />
            </div>
            <span class="kind">{{ handlerLabel(s.handler) }}{{ s.mode === 'keep' ? ' · 只保号' : '' }}</span>
          </div>
        </div>
      </div>
      <p v-if="sites.length && !shown.length" class="none">这一类现在没有站点。</p>
    </div>

    <PtRecords v-else-if="tab === 'records'" class="pane" />
    <PtSettings v-else class="pane" @saved="load" />

    <PtSiteDialog v-model:show="dlgShow" :presets="presets" :sites="sites" :edit="editing" @saved="load" />
  </HModal>
</template>

<style scoped>
.top {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
  margin-bottom: 12px;
}
.top > :first-child {
  flex: 1 1 520px;
  min-width: 0;
}
.acts {
  display: flex;
  gap: 8px;
}
.pane {
  margin-top: 14px;
}
.bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  margin-bottom: 12px;
}
.legend {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  font-size: 11.5px;
  color: var(--muted);
}
.legend i {
  display: inline-block;
  width: 8px;
  height: 8px;
  margin-right: 4px;
  border-radius: 50%;
}
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
  gap: 10px;
}
.site {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.site.fix {
  border-color: color-mix(in oklch, var(--danger) 55%, var(--border));
}
.site.off {
  opacity: 0.6;
}
.head {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}
.logo {
  display: grid;
  flex: none;
  place-items: center;
  width: 34px;
  height: 34px;
  border-radius: 9px;
  background: oklch(0.6 0.13 calc(var(--h) * 1deg));
  color: #fff;
  font-size: 14px;
  font-weight: 700;
}
.nm {
  flex: 1;
  min-width: 0;
}
.name {
  overflow: hidden;
  font-size: 13.5px;
  font-weight: 600;
  color: var(--foreground);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.host {
  overflow: hidden;
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 11px;
  color: var(--muted);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.status {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  min-height: 36px;
}
.msg {
  min-width: 0;
  font-size: 12px;
  color: var(--muted);
  overflow-wrap: anywhere;
}
.foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding-top: 8px;
  border-top: 1px solid var(--border);
}
.dots {
  display: flex;
  gap: 4px;
}
.dot {
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: var(--border);
}
.d-ok {
  background: var(--success);
}
.d-login {
  background: var(--accent);
}
.d-fail {
  background: var(--danger);
}
.kind {
  font-size: 11px;
  color: var(--muted);
  white-space: nowrap;
}
.none {
  margin: 16px 0 0;
  font-size: 12.5px;
  color: var(--muted);
}
</style>
