<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NInput, NInputGroup, NModal, NSpin, NTag } from 'naive-ui'
import { Search } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import SecretInput from '@/components/ui/SecretInput.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import LoginBadge from '@/components/transfer/LoginBadge.vue'
import { resourcesApi } from '@/api'
import { plainProps } from '@/utils/autofill'
import type { Re0Item, Re0Resource } from '@/api/resources'
import { toastError, useFeedback } from '@/composables/useFeedback'

const { message } = useFeedback()

const form = ref({ base_url: 'https://re0.me', client_id: '', client_secret: '' })
const authorized = ref(false)
const saving = ref(false)
const checking = ref(false)

const query = ref('')
const listShow = ref(false)
const items = ref<Re0Item[]>([])
const hint = ref('')
const loading = ref(false)
const error = ref('')
/** 每条资源的解锁状态，key 是 `${tmdbId}-${slug}` */
const unlockState = ref<Record<string, { tone: 'busy' | 'ok' | 'err'; text: string }>>({})

const MEDIA_LABEL: Record<string, string> = { movie: '电影', tv: '剧集' }

async function load() {
  try {
    const d = await resourcesApi.re0Config()
    form.value = {
      base_url: d.base_url || 'https://re0.me',
      client_id: d.client_id ?? '',
      client_secret: d.client_secret ?? '',
    }
    authorized.value = !!d.authorized
  } catch {
    // 首次使用尚无配置
  }
}

async function save() {
  saving.value = true
  try {
    await resourcesApi.re0SaveConfig(form.value)
    message.success('保存成功')
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}

async function check() {
  checking.value = true
  try {
    const d = await resourcesApi.re0Check()
    authorized.value = d.authorized
    message[d.authorized ? 'success' : 'warning'](d.message || (d.authorized ? '授权有效' : '尚未授权'))
  } catch (e) {
    toastError(e, '状态检查失败')
  } finally {
    checking.value = false
  }
}

async function authorize() {
  try {
    await resourcesApi.re0SaveConfig(form.value)
    const d = await resourcesApi.re0OAuthStart()
    if (!d.authorize_url) throw new Error('未取得授权地址，请先保存应用配置')
    // OAuth 要跳到 RE0 官方页面确认，回调再跳回来
    window.open(d.authorize_url, '_blank', 'noopener')
    message.info('已打开 RE0 授权页，完成后回来点「检查状态」')
  } catch (e) {
    toastError(e, '发起授权失败')
  }
}

async function search() {
  const q = query.value.trim()
  if (!q) {
    message.warning('请输入影视名称或 TMDB ID')
    return
  }
  listShow.value = true
  loading.value = true
  error.value = ''
  hint.value = ''
  items.value = []
  unlockState.value = {}
  try {
    const d = await resourcesApi.re0Search(q)
    items.value = d.data ?? []
    hint.value = d.hint ?? ''
    if (!items.value.length) error.value = 'TMDB 未匹配到影视条目'
  } catch (e) {
    error.value = e instanceof Error ? e.message : '搜索失败'
  } finally {
    loading.value = false
  }
}

function keyOf(it: Re0Item, r: Re0Resource) {
  return `${it.id}-${r.slug}`
}

async function unlock(it: Re0Item, r: Re0Resource) {
  const k = keyOf(it, r)
  if (unlockState.value[k]?.tone === 'busy' || unlockState.value[k]?.tone === 'ok') return
  unlockState.value[k] = { tone: 'busy', text: '处理中…' }
  try {
    // 只有 115 分享才让后端顺手转存，其他网盘拿到链接由用户自己处理
    const is115 = (r.pan_type || '') === '115'
    const d = await resourcesApi.re0Unlock({
      media_type: it.media_type,
      tmdb_id: it.id,
      slug: r.slug,
      transfer: is115,
    })
    if (d.transferred) {
      unlockState.value[k] = { tone: 'ok', text: `已转存${d.transfer_msg ? `（${d.transfer_msg}）` : ''}` }
      message.success('解锁成功，115 分享已转存，完成后自动整理入库')
    } else if (d.url) {
      unlockState.value[k] = { tone: 'ok', text: '已解锁' }
      window.open(d.url, '_blank', 'noopener')
      message.success('解锁成功，已打开分享链接')
    } else {
      unlockState.value[k] = { tone: 'err', text: '解锁成功但未返回链接' }
    }
  } catch (e) {
    unlockState.value[k] = { tone: 'err', text: e instanceof Error ? e.message : '解锁失败' }
  }
}

onMounted(load)
</script>

<template>
  <div class="stack">
    <SectionCard title="RE0" hint="官方 OpenAPI · 积分解锁">
      <FieldRow
        label="应用配置"
        wide
        tip="RE0 官方 OpenAPI。需先在 re0.me「个人面板 → OPENAPI → 我的应用」创建应用并等站方审核通过，再把 client_id 和应用 Secret 填到这里。"
      >
        <div class="app-row">
          <NInput
            v-model:value="form.base_url"
            placeholder="站点地址"
            class="w180"
            :input-props="plainProps('re0-base-url')"
          />
          <NInput
            v-model:value="form.client_id"
            placeholder="client_id（app_xxx）"
            class="w200"
            :input-props="plainProps('re0-client-id')"
          />
          <div class="w200">
            <SecretInput v-model="form.client_secret" name="re0-client-secret" placeholder="应用 Secret" />
          </div>
        </div>
      </FieldRow>

      <FieldRow
        label="账号授权"
        tip="保存应用信息后点「授权」，跳转 RE0 官方授权页确认一次。授权后以你的身份查询 / 解锁资源（消耗站内积分），Token 自动续期。解锁的 115 分享会自动转存到接收目录。"
      >
        <div class="auth-row">
          <NButton type="primary" @click="authorize">授权 RE0 账号</NButton>
          <NButton :loading="checking" @click="check">检查状态</NButton>
          <LoginBadge :on="authorized" />
        </div>
      </FieldRow>

      <FormActions>
        <NButton type="primary" :loading="saving" @click="save">保存配置</NButton>
      </FormActions>

      <FieldRow
        label="搜索站内资源"
        tip="先经 TMDB 匹配条目（也支持直接输入 TMDB ID），再查 RE0 站内资源：显示网盘类型、分辨率、大小与解锁积分。已解锁资源不重复扣积分。"
      >
        <NInputGroup>
          <NInput
            v-model:value="query"
            placeholder="影视名称（中英文均可）或 TMDB ID"
            @keyup.enter="search"
          />
          <NButton type="primary" @click="search">
            <template #icon><Search :size="15" /></template>
            搜索
          </NButton>
        </NInputGroup>
      </FieldRow>
    </SectionCard>

    <NModal v-model:show="listShow" preset="card" title="RE0 资源" style="width: 760px">
      <div class="list">
        <div v-if="loading" class="state">
          <NSpin size="small" /><span>TMDB 匹配并查询 RE0 站内资源…</span>
        </div>
        <p v-else-if="error" class="state err">{{ error }}</p>

        <template v-else>
          <p v-if="hint" class="hint">{{ hint }}</p>

          <section v-for="it in items" :key="`${it.media_type}-${it.id}`" class="group">
            <header class="group-head">
              <NTag size="small" :bordered="false">{{ MEDIA_LABEL[it.media_type] || it.media_type }}</NTag>
              <strong>{{ it.title }}</strong>
              <span v-if="it.year" class="dim">{{ it.year }}</span>
              <span v-if="it.vote" class="dim">★ {{ it.vote }}</span>
            </header>

            <p v-if="it.resources_err" class="err small">{{ it.resources_err }}</p>
            <p v-else-if="!it.resources?.length" class="dim small">站内暂无资源</p>

            <div v-for="r in it.resources || []" v-else :key="r.slug" class="res">
              <NTag v-if="r.pan_type" size="small" :bordered="false">{{ r.pan_type }}</NTag>
              <span class="res-title">{{ r.title || r.slug }}</span>
              <span v-if="r.video_resolution?.length" class="dim small">
                {{ r.video_resolution.join(' / ') }}
              </span>
              <span v-if="r.share_size" class="dim small">{{ r.share_size }}</span>
              <span class="points" :class="{ unlocked: r.is_unlocked }">
                {{ r.is_unlocked ? '已解锁' : r.unlock_points != null ? `${r.unlock_points} 积分` : '积分未知' }}
              </span>

              <span class="res-action">
                <span
                  v-if="unlockState[keyOf(it, r)]"
                  class="res-state"
                  :class="unlockState[keyOf(it, r)].tone"
                >
                  {{ unlockState[keyOf(it, r)].text }}
                </span>
                <NButton
                  v-else
                  size="tiny"
                  type="primary"
                  @click="unlock(it, r)"
                >
                  {{ r.is_unlocked ? '获取链接' : '解锁并转存' }}
                </NButton>
              </span>
            </div>
          </section>
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
.app-row,
.auth-row {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}
.w180 {
  width: 180px;
}
.w200 {
  width: 200px;
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
.hint {
  margin: 0 0 10px;
  color: var(--c-danger);
  font-size: 12.5px;
}

.group {
  margin-bottom: 14px;
  padding-bottom: 12px;
  border-bottom: 1px solid var(--c-border);
}
.group:last-child {
  border-bottom: none;
}
.group-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 7px;
  font-size: 13.5px;
  color: var(--c-text-1);
}

.res {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  padding: 6px 0;
}
.res-title {
  font-size: 13px;
  color: var(--c-text-1);
}
.res-action {
  margin-left: auto;
}
.res-state {
  font-size: 12px;
}
.res-state.busy {
  color: var(--c-text-3);
}
.res-state.ok {
  color: var(--c-success);
}
.res-state.err {
  color: var(--c-danger);
}

.points {
  font-size: 12px;
  color: var(--c-warning);
}
.points.unlocked {
  color: var(--c-success);
}
.dim {
  color: var(--c-text-3);
}
.small {
  font-size: 12px;
}
.err {
  color: var(--c-danger);
}
</style>
