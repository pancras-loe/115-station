<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NButton, NInput, NInputGroup, NModal, NSpin } from 'naive-ui'
import { Search } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import SecretInput from '@/components/ui/SecretInput.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import LoginBadge from '@/components/transfer/LoginBadge.vue'
import TmdbPicker from '@/components/transfer/TmdbPicker.vue'
import ResourceRow from '@/components/transfer/ResourceRow.vue'
import { resourcesApi } from '@/api'
import { plainProps } from '@/utils/autofill'
import type { GyTorrent } from '@/api/resources'
import { toastError, useFeedback } from '@/composables/useFeedback'

const { message } = useFeedback()

const form = ref({ base_url: '', username: '', password: '' })
const loggedIn = ref(false)
const saving = ref(false)
const authing = ref(false)
const query = ref('')

const pickerShow = ref(false)
const listShow = ref(false)
const listTitle = ref('')
const torrents = ref<GyTorrent[]>([])
const zyTabs = ref<Record<string, string>>({})
const curZy = ref('')
const curTitle = ref('')
const loading = ref(false)
const error = ref('')
const sort = ref<{ key: '' | 'size' | 'seeds' | 'time'; dir: 1 | -1 }>({ key: '', dir: -1 })

async function load() {
  try {
    const d = await resourcesApi.gyConfig()
    form.value = {
      base_url: d.base_url ?? '',
      username: d.username ?? '',
      password: d.password ?? '',
    }
    loggedIn.value = !!d.logged_in
    // 登录态异步校准，不阻塞表单回填
    resourcesApi.gyCheck().then((r) => (loggedIn.value = r.logged_in)).catch(() => {})
  } catch {
    // 首次使用尚无配置
  }
}

async function save() {
  saving.value = true
  try {
    await resourcesApi.gySaveConfig(form.value)
    message.success('保存成功')
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}

async function auth() {
  authing.value = true
  try {
    if (loggedIn.value) {
      await resourcesApi.gyLogout()
      loggedIn.value = false
      message.success('已退出登录')
      return
    }
    await resourcesApi.gySaveConfig(form.value)
    const d = await resourcesApi.gyLogin()
    loggedIn.value = true
    message.success(d.message || '登录成功')
  } catch (e) {
    toastError(e, '登录失败')
    loggedIn.value = false
  } finally {
    authing.value = false
  }
}

function startSearch() {
  if (!query.value.trim()) {
    message.warning('请输入影视名称或 TMDB ID')
    return
  }
  pickerShow.value = true
}

async function searchSite(title: string, zy = '') {
  curTitle.value = title
  curZy.value = zy
  listShow.value = true
  listTitle.value = `观影种子 · ${title}`
  loading.value = true
  error.value = ''
  torrents.value = []
  sort.value = { key: '', dir: -1 }
  try {
    const d = await resourcesApi.gySearch(title, zy || undefined)
    torrents.value = d.data ?? []
    if (d.zy && Object.keys(d.zy).length) zyTabs.value = d.zy
    if (!torrents.value.length) error.value = `观影站内没有找到「${title}」的种子`
  } catch (e) {
    error.value = e instanceof Error ? e.message : '搜索失败'
  } finally {
    loading.value = false
  }
}

// ---- 排序：观影返回的是 "1.5 GB" / "3 小时前" 这类展示字符串，得先解析成可比较的数 ----
function sizeBytes(s?: string) {
  const m = String(s ?? '').match(/([\d.]+)\s*(TB|GB|MB|KB|B)/i)
  if (!m) return 0
  const mul: Record<string, number> = { B: 1, KB: 1024, MB: 1024 ** 2, GB: 1024 ** 3, TB: 1024 ** 4 }
  return parseFloat(m[1]) * (mul[m[2].toUpperCase()] ?? 1)
}
function seedsNum(v?: string | number) {
  const n = typeof v === 'number' ? v : parseInt(String(v ?? ''), 10)
  return Number.isFinite(n) ? n : 0
}
function timeSec(s?: string) {
  const m = String(s ?? '').match(/([\d.]+)\s*(秒|分钟|小时|天|个?月|年)/)
  if (!m) return 0
  const unit: Record<string, number> = { 秒: 1, 分钟: 60, 小时: 3600, 天: 86400, 月: 2592000, 个月: 2592000, 年: 31536000 }
  // 越久远数值越大，排序时取负让「最新」在 dir=-1 时排前面
  return -(parseFloat(m[1]) * (unit[m[2]] ?? 0))
}

const sorted = computed(() => {
  const list = torrents.value.slice()
  const { key, dir } = sort.value
  if (key === 'size') list.sort((a, b) => (sizeBytes(a.size) - sizeBytes(b.size)) * dir)
  if (key === 'seeds') list.sort((a, b) => (seedsNum(a.seeds) - seedsNum(b.seeds)) * dir)
  if (key === 'time') list.sort((a, b) => (timeSec(a.time) - timeSec(b.time)) * dir)
  return list
})

function sortBy(key: '' | 'size' | 'seeds' | 'time') {
  sort.value = sort.value.key === key ? { key, dir: (sort.value.dir * -1) as 1 | -1 } : { key, dir: -1 }
}

function metaOf(t: GyTorrent) {
  return [t.size, t.time, t.seeds !== undefined && t.seeds !== '' ? `做种 ${t.seeds}` : ''].filter(
    Boolean,
  ) as string[]
}

/** 点击种子行：先取磁力再提交 115 离线下载，一步完成 */
function runOf(t: GyTorrent) {
  return async () => {
    const d = await resourcesApi.gyResources(t.path)
    if (!d.magnet) throw new Error('该条目没有磁力链接')
    const r = await resourcesApi.gyOffline(d.magnet)
    return r.message || '已提交 115 离线下载'
  }
}

onMounted(load)
</script>

<template>
  <div class="stack">
    <SectionCard title="观影" hint="站内种子 → 115 离线下载">
      <FieldRow
        label="站点地址"
        tip="观影常更换域名。搜索失败时到站点首页看最新地址，改这里后保存并重新登录。"
      >
        <NInput
          v-model:value="form.base_url"
          placeholder="观影站点地址"
          :input-props="plainProps('gy-base-url')"
        />
      </FieldRow>

      <FieldRow
        label="账号"
        tip="观影站内账号（搜索 / 详情需登录）。登录即测试账号有效性；服务端自动通过站点反爬验证并保持登录态，会话失效后自动重登。"
      >
        <div class="auth-row">
          <NInputGroup>
            <NInput
              v-model:value="form.username"
              placeholder="用户名 / 邮箱"
              :input-props="plainProps('gy-account')"
            />
            <SecretInput v-model="form.password" name="gy-secret" placeholder="密码" />
            <NButton :type="loggedIn ? 'warning' : 'primary'" :loading="authing" @click="auth">
              {{ loggedIn ? '退出登录' : '登录' }}
            </NButton>
          </NInputGroup>
          <LoginBadge :on="loggedIn" :sub="loggedIn ? form.username : ''" />
        </div>
      </FieldRow>

      <FormActions>
        <NButton type="primary" :loading="saving" @click="save">保存配置</NButton>
      </FormActions>

      <FieldRow
        label="搜索影视"
        tip="先经 TMDB 匹配条目（支持直接填 TMDB ID），选定影片后到观影搜种子，磁力一键提交 115 离线下载。"
      >
        <NInputGroup>
          <NInput
            v-model:value="query"
            placeholder="影视名称（中英文均可）或 TMDB ID"
            @keyup.enter="startSearch"
          />
          <NButton type="primary" @click="startSearch">
            <template #icon><Search :size="15" /></template>
            搜索
          </NButton>
        </NInputGroup>
      </FieldRow>
    </SectionCard>

    <TmdbPicker
      v-model:show="pickerShow"
      :query="query"
      skip-label="跳过 TMDB，直接用关键词搜观影"
      @pick="(title) => searchSite(title)"
      @skip="searchSite(query.trim())"
    />

    <NModal v-model:show="listShow" preset="card" :title="listTitle" style="width: 760px">
      <div class="list">
        <div v-if="loading" class="state"><NSpin size="small" /><span>搜索观影种子中…</span></div>
        <p v-else-if="error" class="state err">{{ error }}</p>

        <template v-else>
          <div v-if="Object.keys(zyTabs).length" class="pills">
            <span class="pills-label">分类</span>
            <button
              v-for="(label, key) in zyTabs"
              :key="key"
              class="pill"
              :class="{ on: curZy === key }"
              @click="searchSite(curTitle, String(key))"
            >
              {{ label }}
            </button>
          </div>

          <div class="pills">
            <span class="pills-label">排序</span>
            <button class="pill" :class="{ on: !sort.key }" @click="sortBy('')">默认</button>
            <button
              v-for="k in (['size', 'seeds', 'time'] as const)"
              :key="k"
              class="pill"
              :class="{ on: sort.key === k }"
              @click="sortBy(k)"
            >
              {{ { size: '大小', seeds: '做种', time: '时间' }[k]
              }}{{ sort.key === k ? (sort.dir === -1 ? ' ↓' : ' ↑') : '' }}
            </button>
          </div>

          <ResourceRow
            v-for="(t, i) in sorted"
            :key="i"
            tag="种子"
            tag-type="success"
            :title="t.title"
            :meta="metaOf(t)"
            action="offline"
            :run="runOf(t)"
          />
        </template>
      </div>
    </NModal>
  </div>
</template>

<style scoped>
.stack {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.auth-row {
  display: flex;
  flex-direction: column;
  gap: 7px;
}
.list {
  max-height: 62vh;
  overflow-y: auto;
}
.state {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 30px 0;
  color: var(--c-text-3);
  font-size: 13px;
}
.state.err {
  color: var(--c-danger);
}

.pills {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  margin-bottom: 10px;
  font-size: 12px;
  color: var(--c-text-3);
}
.pills-label {
  margin-right: 2px;
}
.pill {
  all: unset;
  padding: 3px 12px;
  border-radius: 999px;
  cursor: pointer;
  font-size: 12.5px;
  line-height: 20px;
  background: var(--c-bg-hover);
  color: var(--c-text-2);
  transition: background-color 0.15s, color 0.15s;
}
.pill.on {
  background: var(--c-primary);
  color: #fff;
}
</style>
