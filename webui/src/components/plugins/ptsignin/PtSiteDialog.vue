<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import HModal from '@/components/hero/HModal.vue'
import HSearchField from '@/components/hero/HSearchField.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import SecretInput from '@/components/ui/SecretInput.vue'
import TestBanner, { type BannerState } from '@/components/ui/TestBanner.vue'
import { pluginsApi } from '@/api'
import type { PtPreset, PtSite, PtSiteReq } from '@/api/plugins'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { logoHue } from './pt'

/**
 * 添加 / 修改一个站。添加分两步：先选预置站（或填地址加其他站），再填凭据。
 * Cookie / Token 只进不出：修改时留空 = 不改。
 */
const show = defineModel<boolean>('show', { default: false })
const props = defineProps<{ presets: PtPreset[]; sites: PtSite[]; edit: PtSite | null }>()
const emit = defineEmits<{ saved: [] }>()
const { message } = useFeedback()

const step = ref<'pick' | 'form'>('pick')
const q = ref('')
const preset = ref<PtPreset | null>(null) // null + 表单 = 自定义站
const form = ref({ name: '', base_url: '', cookie: '', token: '', ua: '', proxy: false, mode: 'signin' as 'signin' | 'keep' })
const saving = ref(false)
const testing = ref(false)
const test = ref<BannerState | null>(null)

const MODE_OPTS = [
  { label: '签到', value: 'signin' as const },
  { label: '只保号（访问首页）', value: 'keep' as const },
]

watch(show, (v) => {
  if (!v) return
  test.value = null
  q.value = ''
  const e = props.edit
  if (e) {
    preset.value = props.presets.find((p) => p.key === e.preset) ?? null
    form.value = { name: e.name, base_url: e.base_url, cookie: '', token: '', ua: e.ua, proxy: e.proxy, mode: e.mode }
    step.value = 'form'
  } else {
    preset.value = null
    step.value = 'pick'
  }
})

const added = computed(() => new Set(props.sites.map((s) => s.preset).filter(Boolean)))

const GROUPS: { key: string; title: string; match: (p: PtPreset) => boolean }[] = [
  { key: 'nexus', title: '通用签到', match: (p) => p.handler === 'nexus' },
  { key: 'special', title: '专用流程', match: (p) => !p.unavailable && p.handler !== 'nexus' },
  { key: 'off', title: '暂不支持', match: (p) => !!p.unavailable },
]
const groups = computed(() => {
  const kw = q.value.trim().toLowerCase()
  const hit = (p: PtPreset) => !kw || p.name.toLowerCase().includes(kw) || p.domains.some((d) => d.includes(kw))
  return GROUPS.map((g) => ({ ...g, items: props.presets.filter((p) => g.match(p) && hit(p)) })).filter((g) => g.items.length)
})

const title = computed(() => {
  if (props.edit) return `修改 ${props.edit.name}`
  if (step.value === 'pick') return '添加站点'
  return preset.value ? `添加 ${preset.value.name}` : '添加其他站'
})
const isToken = computed(() => (preset.value?.cred ?? props.edit?.cred) === 'token')

function pick(p: PtPreset | null) {
  preset.value = p
  form.value = { name: p?.name ?? '', base_url: p ? `https://${p.domains[0]}/` : '', cookie: '', token: '', ua: '', proxy: false, mode: 'signin' }
  test.value = null
  step.value = 'form'
}

function body(): PtSiteReq {
  const f = form.value
  const b: PtSiteReq = { name: f.name.trim(), base_url: f.base_url.trim(), ua: f.ua.trim(), proxy: f.proxy, mode: f.mode }
  if (!props.edit) b.preset = preset.value?.key ?? ''
  // 修改时留空 = 不改
  if (!props.edit || f.cookie.trim()) b.cookie = f.cookie
  if (!props.edit || f.token.trim()) b.token = f.token
  return b
}

async function runTest() {
  testing.value = true
  test.value = { status: 'pending', title: '正在访问首页，只检查登录，不签到…' }
  try {
    const d = await pluginsApi.ptTestSite({ ...body(), id: props.edit?.id })
    const r = d.data
    test.value = r.result === 'fail'
      ? { status: 'err', title: '没登录上', detail: r.message }
      : { status: 'ok', title: r.message, trailing: `${(d.ms / 1000).toFixed(1)} 秒` }
  } catch (e) {
    test.value = { status: 'err', title: '测试失败', detail: e instanceof Error ? e.message : '' }
  } finally {
    testing.value = false
  }
}

async function save() {
  saving.value = true
  try {
    if (props.edit) await pluginsApi.ptUpdateSite(props.edit.id, { ...body(), enabled: props.edit.enabled })
    else await pluginsApi.ptCreateSite(body())
    message.success('已保存')
    show.value = false
    emit('saved')
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <HModal v-model:show="show" :title="title" width="720px" persistent>
    <template v-if="step === 'pick'">
      <HSearchField v-model="q" placeholder="搜站名或域名，比如 织梦 / zmpt" />
      <div v-for="g in groups" :key="g.key" class="group">
        <div class="group-t">{{ g.title }}</div>
        <div class="presets">
          <button
            v-for="p in g.items"
            :key="p.key"
            type="button"
            class="preset"
            :disabled="p.unavailable || added.has(p.key)"
            :title="p.note || p.domains[0]"
            @click="pick(p)"
          >
            <span class="logo" :style="{ '--h': logoHue(p.name) }">{{ p.name.slice(0, 1) }}</span>
            <span class="pt">
              <span class="pn">{{ p.name }}</span>
              <span class="pd">{{ added.has(p.key) ? '已添加' : p.note || p.domains[0] }}</span>
            </span>
          </button>
        </div>
      </div>
      <p v-if="!groups.length" class="empty">目录里没有，点下面「填地址添加其他站」。</p>
    </template>

    <template v-else>
      <FieldRow v-if="!preset" label="名称" required>
        <HInput v-model="form.name" placeholder="比如 某某PT" />
      </FieldRow>
      <FieldRow
        label="站点地址"
        required
        wide
        :hint="preset ? (preset.handler === 'nexus' ? '通用流程：访问 /attendance.php。' : '这个站有专用签到流程。') + (preset.note ? ` ${preset.note}。` : '') : '按通用流程签到：访问 /attendance.php，认不出结果时退回确认登录。'"
      >
        <HInput v-model="form.base_url" mono placeholder="https://pt.example.com/" />
      </FieldRow>
      <FieldRow
        v-if="isToken"
        label="API Token"
        :required="!edit"
        wide
        :hint="edit?.has_token ? '已保存，留空 = 不改' : '站点「控制面板 → API」里生成'"
      >
        <SecretInput v-model="form.token" name="pt-site-token" placeholder="粘贴 API Token" />
      </FieldRow>
      <FieldRow
        v-else
        label="Cookie"
        :required="!edit"
        wide
        :hint="edit?.has_cookie ? `已保存（结尾 …${edit.cookie_end || ''}），留空 = 不改` : '浏览器登录这个站后，F12 → 网络 → 任意请求 → 请求头里的 Cookie 整段复制过来'"
      >
        <HInput v-model="form.cookie" mono :rows="3" placeholder="c_secure_uid=…; c_secure_pass=…" :input-attrs="{ autocomplete: 'off', name: 'pt-site-cookie' }" />
      </FieldRow>
      <FieldRow label="User-Agent" wide hint="留空用内置的 Chrome UA。站点开了 Cloudflare 时，填你复制 Cookie 那个浏览器的 UA（cf_clearance 绑定 UA）。">
        <HInput v-model="form.ua" mono placeholder="Mozilla/5.0 …" />
      </FieldRow>
      <FieldRow label="走全局代理" tip="用「系统配置 → 代理」里的代理访问这个站。">
        <HSwitch v-model="form.proxy" aria-label="走全局代理" />
      </FieldRow>
      <FieldRow label="每天做什么" tip="只保号：每天访问一次首页确认登录，不签到。有的站只要求定期登录。">
        <HSegmented v-model="form.mode" :options="MODE_OPTS" size="sm" />
      </FieldRow>
      <p class="note">Cookie 只存在本站数据库里，日志、通知里都不会出现。用内置浏览器访问（过 Cloudflare）第二期做。</p>
      <TestBanner :state="test" />
    </template>

    <template #footer>
      <div class="foot">
        <template v-if="step === 'pick'">
          <span class="muted">列表里没有？</span>
          <HButton variant="secondary" @click="pick(null)">填地址添加其他站</HButton>
        </template>
        <template v-else>
          <HButton variant="tertiary" :loading="testing" @click="runTest">测试登录</HButton>
          <div class="right">
            <HButton v-if="!edit" variant="tertiary" @click="step = 'pick'">返回</HButton>
            <HButton variant="primary" :loading="saving" @click="save">保存</HButton>
          </div>
        </template>
      </div>
    </template>
  </HModal>
</template>

<style scoped>
.group {
  margin-top: 14px;
}
.group-t {
  margin-bottom: 6px;
  font-size: 12px;
  color: var(--muted);
}
.presets {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: 8px;
}
.preset {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  padding: 8px;
  border: 1px solid var(--border);
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  color: var(--foreground);
  text-align: left;
  cursor: pointer;
}
.preset:hover:not(:disabled) {
  border-color: var(--accent);
}
.preset:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.logo {
  display: grid;
  flex: none;
  place-items: center;
  width: 28px;
  height: 28px;
  border-radius: 7px;
  background: oklch(0.6 0.13 calc(var(--h) * 1deg));
  color: #fff;
  font-size: 12px;
  font-weight: 700;
}
.pt {
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.pn {
  overflow: hidden;
  font-size: 13px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pd {
  overflow: hidden;
  font-size: 11px;
  color: var(--muted);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.empty,
.note {
  margin: 12px 0 0;
  font-size: 12px;
  color: var(--muted);
}
.note {
  margin-bottom: 10px;
}
.foot {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  width: 100%;
}
.right {
  display: flex;
  gap: 8px;
}
.muted {
  font-size: 12.5px;
  color: var(--muted);
}
</style>
