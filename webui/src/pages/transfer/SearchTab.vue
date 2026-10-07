<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HInput from '@/components/hero/HInput.vue'
import { ArrowLeft, BellPlus, BellRing, Search } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import PosterImage from '@/components/PosterImage.vue'
import CandidateGrid from '@/components/transfer/CandidateGrid.vue'
import ResourcePanel from '@/components/transfer/ResourcePanel.vue'
import SubscribeDialog from '@/components/subscribe/SubscribeDialog.vue'
import type { SubTarget } from '@/components/subscribe/SubscribeDialog.vue'
import { subscribeApi } from '@/api'
import type { Subscription } from '@/api/subscribe'
import { resourcesApi, transferApi } from '@/api'
import type { TmdbCandidate } from '@/api/resources'
import type { OwnedInfo, ResourceQuery } from '@/api/transfer'
import { useTransferSources } from '@/composables/transferSources'
import { parseLinks, pendingLinkText } from '@/composables/transferLinks'
import { useFeedback } from '@/composables/useFeedback'

const router = useRouter()
const { message } = useFeedback()
const { state: srcState } = useTransferSources()

// ---- 输入框：片名 / TMDB ID。贴了链接就带去「链接转存」页签 ----
const input = ref('')
const busy = ref(false)

const looksLikeLink = computed(() => parseLinks(input.value).length > 0)

function go() {
  const raw = input.value.trim()
  if (!raw) {
    message.warning('输入片名或 TMDB ID')
    return
  }
  if (looksLikeLink.value) {
    toLinkTab()
    return
  }
  void searchTmdb(raw)
}

function toLinkTab() {
  pendingLinkText.value = input.value
  input.value = ''
  router.push({ query: { tab: 'link' } })
}

// ---- TMDB 选片 ----
const stage = ref<'idle' | 'pick' | 'resources'>('idle')
const cands = ref<TmdbCandidate[]>([])
const candHint = ref('')
const owned = ref<Record<string, OwnedInfo>>({})
const selected = ref<{ query: ResourceQuery; cand?: TmdbCandidate } | null>(null)

async function searchTmdb(q: string) {
  busy.value = true
  stage.value = 'pick'
  cands.value = []
  candHint.value = ''
  try {
    const d = await resourcesApi.tmdbSearch(q)
    cands.value = d.data ?? []
    if (!cands.value.length) candHint.value = d.hint || `TMDB 里没有找到「${q}」`
    // 单条命中（直接填了 TMDB ID）就不用再点一下
    if (cands.value.length === 1) pick(cands.value[0])
    if (cands.value.length) {
      transferApi
        .owned(cands.value.map((c) => `${c.media_type}:${c.id}`))
        .then((r) => (owned.value = r.data ?? {}))
        .catch(() => {})
    }
  } catch (e) {
    candHint.value = e instanceof Error ? e.message : 'TMDB 搜索失败'
  } finally {
    busy.value = false
  }
}

function pick(c: TmdbCandidate) {
  selected.value = {
    cand: c,
    query: { tmdb_id: c.id, type: c.media_type, title: c.title, original_title: c.original_title, year: c.year },
  }
  stage.value = 'resources'
  void loadSub(c)
}

// ---- 订阅：选定影片后可以直接订阅，订阅过的显示进度、点了去订阅页 ----
const sub = ref<Subscription | null>(null)
const subOpen = ref(false)
const subTarget = computed<SubTarget | null>(() => {
  const c = selected.value?.cand
  return c ? { tmdb_id: c.id, media_type: c.media_type, title: c.title, year: c.year, poster: c.poster } : null
})
async function loadSub(c: TmdbCandidate) {
  sub.value = null
  try {
    sub.value = (await subscribeApi.of(c.id, c.media_type)).data
  } catch {
    // 查不到不影响找资源
  }
}
function subLabel(s: Subscription) {
  if (s.state === 'done') return '已订阅 · 已完成'
  if (s.media_type === 'movie') return '已订阅'
  return s.missing ? `已订阅 · 缺 ${s.missing} 集` : '已订阅'
}

function skipTmdb() {
  const kw = input.value.trim()
  if (!kw) return
  selected.value = { query: { title: kw } }
  stage.value = 'resources'
}

function back() {
  stage.value = cands.value.length ? 'pick' : 'idle'
}

const sources = computed(() => srcState.value?.sources ?? [])
const folderPath = computed(() => srcState.value?.folder_path || srcState.value?.folder || '')
const selOwned = computed(() => {
  const c = selected.value?.cand
  return c ? owned.value[`${c.media_type}:${c.id}`] : undefined
})
</script>

<template>
  <div class="stack">
    <SectionCard>
      <div class="bar">
        <HInput
          v-model="input"
          class="bar-input"
          placeholder="片名（中英文均可）或 TMDB ID"
          :input-attrs="{ 'aria-label': '片名或 TMDB ID', autocomplete: 'off' }"
          @enter="go"
        />
        <HButton variant="primary" :loading="busy" @click="go">
          <template #icon><Search :size="15" /></template>
          搜索
        </HButton>
      </div>
      <div class="bar-sub">
        <template v-if="looksLikeLink">
          <span class="kind">这是下载链接，不是片名</span>
          <button type="button" class="link" @click="toLinkTab">带到「链接转存」提交</button>
        </template>
        <template v-else>
          <span v-if="folderPath">找到的资源转存到 <b class="mono">{{ folderPath }}</b>，完成后自动整理入库</span>
          <span v-else-if="srcState" class="warn">还没设置转存目录，分享链接转存不了</span>
          <button type="button" class="link" @click="router.push({ query: { tab: 'link' } })">
            {{ folderPath ? '更改' : '去设置' }}
          </button>
        </template>
      </div>
    </SectionCard>

    <SectionCard v-if="stage === 'pick'" title="选择影片" hint="先在 TMDB 定下是哪一部，再拿规范片名去各站搜">
      <CandidateGrid :items="cands" :owned="owned" :loading="busy" :hint="candHint" @pick="pick" @skip="skipTmdb" />
    </SectionCard>

    <SectionCard v-if="stage === 'resources' && selected">
      <div class="sel">
        <PosterImage
          v-if="selected.cand"
          class="sel-poster"
          :src="selected.cand.poster ? resourcesApi.tmdbImageUrl(selected.cand.poster, 'w154') : null"
          :alt="selected.cand.title"
        />
        <div class="sel-body">
          <div class="sel-head">
            <HButton variant="ghost" size="sm" icon-only aria-label="换一部" @click="back">
              <ArrowLeft :size="16" />
            </HButton>
            <h2 class="sel-title">{{ selected.query.title }}</h2>
            <span v-if="selected.query.year" class="sel-year">{{ selected.query.year }}</span>
            <HChip v-if="selected.cand" :color="selected.cand.media_type === 'tv' ? 'accent' : 'warning'">
              {{ selected.cand.media_type === 'tv' ? '剧集' : '电影' }}
            </HChip>
            <HChip v-if="selOwned" color="success">已入库{{ selOwned.category ? ` · ${selOwned.category}` : '' }}</HChip>
            <template v-if="selected.cand">
              <HButton
                v-if="sub"
                variant="secondary"
                size="sm"
                class="sub-btn"
                @click="router.push({ name: 'subscriptions', query: { sub: String(sub.id) } })"
              >
                <template #icon><BellRing :size="14" /></template>
                {{ subLabel(sub) }}
              </HButton>
              <HButton v-else variant="secondary" size="sm" class="sub-btn" @click="subOpen = true">
                <template #icon><BellPlus :size="14" /></template>
                订阅
              </HButton>
            </template>
          </div>
          <p v-if="selected.cand?.overview" class="sel-overview">{{ selected.cand.overview }}</p>
          <p v-else-if="!selected.cand" class="sel-overview">
            直接用关键词搜，没经过 TMDB：结果不做片名比对，RE0 不可用（它按 TMDB 条目查资源）。
          </p>
        </div>
      </div>
      <ResourcePanel :query="selected.query" :sources="sources" />
    </SectionCard>
    <SubscribeDialog v-model:show="subOpen" :target="subTarget" @saved="(s) => (sub = s)" />
  </div>
</template>

<style scoped>
.stack {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.bar {
  display: flex;
  gap: 8px;
  align-items: center;
}
.bar-input {
  flex: 1;
  min-width: 0;
}
.bar-sub {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px 12px;
  margin-top: 8px;
  font-size: 12.5px;
  color: var(--muted);
}
.bar-sub .kind {
  color: var(--accent);
}
.bar-sub .warn {
  color: var(--warning);
}
.mono {
  font-family: var(--font-mono);
  font-weight: 500;
  color: var(--foreground);
  word-break: break-all;
}
.link {
  all: unset;
  color: var(--accent);
  cursor: pointer;
}
.link:hover {
  text-decoration: underline;
}

.sel {
  display: flex;
  gap: 14px;
  margin-bottom: 14px;
}
.sel-poster {
  width: 72px;
  height: 108px;
  flex: none;
  border-radius: var(--r-sm);
}
.sel-body {
  min-width: 0;
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.sel-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 8px;
}
.sel-title {
  margin: 0;
  font-size: 17px;
  font-weight: 600;
  color: var(--foreground);
}
.sub-btn {
  margin-left: auto;
}
.sel-year {
  font-size: 13px;
  color: var(--muted);
}
.sel-overview {
  margin: 0;
  font-size: 12.5px;
  line-height: 1.65;
  color: color-mix(in oklab, var(--foreground) 68%, var(--muted));
  display: -webkit-box;
  -webkit-line-clamp: 3;
  line-clamp: 3;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
@media (max-width: 720px) {
  .bar {
    flex-wrap: wrap;
  }
  .bar-input {
    flex-basis: 100%;
  }
  .sel-poster {
    width: 56px;
    height: 84px;
  }
}
</style>
