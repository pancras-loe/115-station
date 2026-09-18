<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NInput, NInputGroup, NModal, NSpin } from 'naive-ui'
import { ArrowLeft, Search } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import SecretInput from '@/components/ui/SecretInput.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import LoginBadge from '@/components/transfer/LoginBadge.vue'
import TmdbPicker from '@/components/transfer/TmdbPicker.vue'
import ResourceRow from '@/components/transfer/ResourceRow.vue'
import { resourcesApi, transferApi } from '@/api'
import { plainProps } from '@/utils/autofill'
import type { MkResource, MkVideo } from '@/api/resources'
import { toastError, useFeedback } from '@/composables/useFeedback'

const { message } = useFeedback()

const baseUrl = ref('')
const token = ref('')
const hasToken = ref(false)
const tokenAt = ref('')
const savingBase = ref(false)

const login = ref({ username: '', password: '', code: '' })
const captcha = ref({ img: '', key: '' })
const logging = ref(false)

const query = ref('')
const pickerShow = ref(false)
const listShow = ref(false)
const listTitle = ref('')
/** 弹窗里有两级：影片列表 → 该影片的资源列表 */
const stage = ref<'videos' | 'resources'>('videos')
const videos = ref<MkVideo[]>([])
const resources = ref<MkResource[]>([])
const curTitle = ref('')
const loading = ref(false)
const error = ref('')

async function load() {
  try {
    const d = await resourcesApi.mkConfig()
    baseUrl.value = d.base_url ?? ''
    login.value.username = d.username ?? ''
    hasToken.value = !!d.has_token
    tokenAt.value = d.token_at ?? ''
  } catch {
    // 首次使用尚无配置
  }
}

async function saveBase() {
  savingBase.value = true
  try {
    await resourcesApi.mkSaveConfig({ base_url: baseUrl.value.trim() })
    message.success('保存成功')
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    savingBase.value = false
  }
}

async function saveToken() {
  try {
    await resourcesApi.mkSaveConfig({ token: token.value.trim() })
    token.value = ''
    message.success(token.value ? 'token 已保存' : 'token 已清除')
    await load()
  } catch (e) {
    toastError(e, 'token 保存失败')
  }
}

async function refreshCaptcha() {
  try {
    const d = await resourcesApi.mkCaptcha()
    captcha.value = { img: d.img, key: d.key || '' }
  } catch (e) {
    toastError(e, '验证码获取失败')
  }
}

async function doLogin() {
  if (!login.value.username || !login.value.password) {
    message.warning('请填写账号和密码')
    return
  }
  if (!captcha.value.key || !login.value.code) {
    message.warning('请先获取并输入图形验证码')
    return
  }
  logging.value = true
  try {
    await resourcesApi.mkLogin({ ...login.value, key: captcha.value.key })
    message.success('登录成功，token 已保存')
    login.value.code = ''
    captcha.value = { img: '', key: '' }
    await load()
  } catch (e) {
    toastError(e, '登录失败')
    refreshCaptcha() // 验证码一次性，失败后必须换新的
  } finally {
    logging.value = false
  }
}

function startSearch() {
  if (!query.value.trim()) {
    message.warning('请输入影视名称或 TMDB ID')
    return
  }
  pickerShow.value = true
}

async function searchSite(kw: string) {
  curTitle.value = kw
  stage.value = 'videos'
  listShow.value = true
  listTitle.value = `不太灵影视 · ${kw}`
  loading.value = true
  error.value = ''
  videos.value = []
  try {
    videos.value = (await resourcesApi.mkSearch(kw)).data ?? []
    if (!videos.value.length) error.value = `没有找到「${kw}」相关影片`
  } catch (e) {
    error.value = e instanceof Error ? e.message : '搜索失败'
  } finally {
    loading.value = false
  }
}

async function openResources(v: MkVideo) {
  curTitle.value = v.title
  stage.value = 'resources'
  listTitle.value = `不太灵影视 · ${v.title}`
  loading.value = true
  error.value = ''
  resources.value = []
  try {
    resources.value = (await resourcesApi.mkResources(v.id)).data ?? []
    if (!resources.value.length) {
      error.value = '没有读到资源：站方仅对 VIP 开放资源列表，请确认已粘贴有效 token 或验证码登录成功。'
    }
  } catch (e) {
    error.value = e instanceof Error ? e.message : '读取失败'
  } finally {
    loading.value = false
  }
}

function runOf(it: MkResource) {
  return async () => {
    if (it.action === 'open') {
      window.open(it.link, '_blank', 'noopener')
      return ''
    }
    if (it.action === 'transfer') {
      const r = await transferApi.shareReceive({
        url: it.link,
        code: it.code || '',
        target_cid: '',
        organize: true,
      })
      return r.message || '已转存，完成后自动整理入库'
    }
    await transferApi.offlineAdd({ url: it.link, code: '', target_cid: '', organize: true })
    return '已提交离线下载，完成后自动整理入库'
  }
}

onMounted(load)
</script>

<template>
  <div class="stack">
    <SectionCard title="不太灵影视" hint="站内资源需 VIP token">
      <FieldRow
        label="站点地址"
        tip="不太灵影视（bt0 系）镜像地址，默认 https://web5.mukaku.com，某域名失效时换 web1-web4。"
      >
        <NInputGroup>
          <NInput
            v-model:value="baseUrl"
            placeholder="不太灵影视站点地址"
            :input-props="plainProps('mk-base-url')"
          />
          <NButton type="primary" :loading="savingBase" @click="saveBase">保存</NButton>
        </NInputGroup>
      </FieldRow>

      <FieldRow
        label="VIP Token"
        tip="资源仅 VIP 可见。浏览器登录后 F12 → Application → Local Storage → 复制 token 粘贴到这里。也可用下方验证码登录自动获取。搜索功能无需 token。"
      >
        <div class="token-row">
          <NInputGroup>
            <NInput
              v-model:value="token"
              :placeholder="hasToken ? '已保存（粘贴新值可覆盖）' : '粘贴浏览器 localStorage 里的 token'"
              :input-props="plainProps('mk-vip-token')"
            />
            <NButton type="primary" @click="saveToken">保存</NButton>
          </NInputGroup>
          <LoginBadge :on="hasToken" :sub="hasToken ? tokenAt : '资源需 VIP'" />
        </div>
      </FieldRow>

      <FieldRow
        label="验证码登录"
        tip="填写站内账号密码，点「获取验证码」输入图中字符后登录，token 自动保存。验证码有时效，过期就点图片刷新。"
      >
        <div class="login-row">
          <NInput
            v-model:value="login.username"
            placeholder="用户名 / 邮箱"
            class="w150"
            :input-props="plainProps('mk-account')"
          />
          <div class="w130"><SecretInput v-model="login.password" name="mk-secret" placeholder="密码" /></div>
          <NInput
            v-model:value="login.code"
            placeholder="验证码"
            class="w90"
            :input-props="plainProps('mk-captcha-code')"
            @keyup.enter="doLogin"
          />
          <img
            v-if="captcha.img"
            :src="captcha.img"
            class="captcha"
            alt="验证码"
            title="点击刷新"
            @click="refreshCaptcha"
          />
          <NButton @click="refreshCaptcha">获取验证码</NButton>
          <NButton type="primary" :loading="logging" @click="doLogin">登录</NButton>
        </div>
      </FieldRow>

      <FieldRow
        label="搜索影视"
        tip="先经 TMDB 匹配条目，选定后用规范标题搜站内影片；再点影片查看资源列表。磁力 / ed2k 自动提交离线下载，115 分享自动转存，其他网盘打开原链。"
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
      skip-label="跳过 TMDB，直接用关键词搜站内"
      @pick="(title) => searchSite(title)"
      @skip="searchSite(query.trim())"
    />

    <NModal v-model:show="listShow" preset="card" :title="listTitle" style="width: 720px">
      <div class="list">
        <div v-if="loading" class="state"><NSpin size="small" /><span>读取中…</span></div>
        <p v-else-if="error" class="state err">{{ error }}</p>

        <template v-else-if="stage === 'videos'">
          <button v-for="v in videos" :key="v.id" class="video" @click="openResources(v)">
            <img v-if="v.image" :src="v.image" class="video-poster" loading="lazy" :alt="v.title" />
            <div v-else class="video-poster" />
            <div class="video-body">
              <div class="video-title">{{ v.title }}</div>
              <div class="video-meta">{{ [v.year, v.note].filter(Boolean).join(' · ') }}</div>
            </div>
          </button>
        </template>

        <template v-else>
          <div class="back">
            <NButton text type="primary" size="small" @click="searchSite(query.trim())">
              <template #icon><ArrowLeft :size="14" /></template>
              返回影片列表
            </NButton>
            <span class="count">共 {{ resources.length }} 条资源</span>
          </div>
          <ResourceRow
            v-for="(it, i) in resources"
            :key="i"
            :title="it.seed_name || it.link"
            :meta="it.code ? [`提取码 ${it.code}`] : []"
            :action="it.action"
            :run="runOf(it)"
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
.token-row,
.login-row {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}
.token-row {
  flex-direction: column;
  align-items: flex-start;
  gap: 7px;
}
.token-row > :first-child {
  width: 100%;
}
.w150 {
  width: 150px;
}
.w130 {
  width: 130px;
}
.w90 {
  width: 90px;
}
.captcha {
  height: 32px;
  border-radius: var(--radius-sm);
  cursor: pointer;
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
  text-align: center;
  line-height: 1.7;
}
.state.err {
  color: var(--c-danger);
}

.video {
  all: unset;
  box-sizing: border-box;
  display: flex;
  gap: 12px;
  width: 100%;
  padding: 9px;
  margin-bottom: 8px;
  border: 1px solid var(--c-border);
  border-radius: var(--radius);
  cursor: pointer;
  transition: background-color 0.15s, border-color 0.15s;
}
.video:hover {
  background: var(--c-bg-hover);
  border-color: var(--c-border-strong);
}
.video-poster {
  width: 52px;
  height: 78px;
  flex: none;
  object-fit: cover;
  border-radius: var(--radius-sm);
  background: var(--c-bg-hover);
}
.video-body {
  min-width: 0;
  flex: 1;
}
.video-title {
  font-size: 13.5px;
  font-weight: 500;
  color: var(--c-text-1);
}
.video-meta {
  margin-top: 3px;
  font-size: 12px;
  color: var(--c-text-3);
}

.back {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 8px;
}
.count {
  margin-left: auto;
  font-size: 12px;
  color: var(--c-text-3);
}
</style>
