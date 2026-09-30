<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HInput from '@/components/hero/HInput.vue'
import { ArrowLeft, Search, Send } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import PosterImage from '@/components/PosterImage.vue'
import CandidateGrid from '@/components/transfer/CandidateGrid.vue'
import ResourcePanel from '@/components/transfer/ResourcePanel.vue'
import { resourcesApi, transferApi } from '@/api'
import type { TmdbCandidate } from '@/api/resources'
import type { OwnedInfo, ResourceQuery } from '@/api/transfer'
import { useTransferSources } from '@/composables/transferSources'
import { toastError, useFeedback } from '@/composables/useFeedback'

const router = useRouter()
const { message, dialog } = useFeedback()
const { state: srcState } = useTransferSources()

// ---- 输入框：贴链接就提交，否则当片名 / TMDB ID 搜 ----
const input = ref('')
const code = ref('')
const busy = ref(false)
const lastSubmit = ref<{ ok: boolean; text: string } | null>(null)

type LinkKind = 'share' | 'magnet' | 'ed2k' | 'http' | ''

/** 与后端 classifyLink 同口径；TMDB 的网页链接是在指定条目，不是下载链接 */
function linkKindOf(raw: string): LinkKind {
  const lower = raw.trim().toLowerCase()
  if (['115.com/s/', '115cdn.com/s/', 'anxia.com/s/'].some((d) => lower.includes(d))) return 'share'
  if (lower.startsWith('magnet:?')) return 'magnet'
  if (lower.startsWith('ed2k://')) return 'ed2k'
  if (/^(https?|ftp):\/\//.test(lower) && !lower.includes('themoviedb.org')) return 'http'
  return ''
}

const linkKind = computed(() => linkKindOf(input.value))
/** 分享链接里已经带了提取码（?password= / #xxxx / 「提取码 xxxx」）就不用再填 */
const shareHasCode = computed(() => /[?&]password=|#[a-z0-9]{4}|提取码|访问码|密码/i.test(input.value))

const LINK_LABEL: Record<Exclude<LinkKind, ''>, string> = {
  share: '115 分享链接，将转存',
  magnet: '磁力链接，将提交 115 离线下载',
  ed2k: 'ed2k 链接，将提交 115 离线下载',
  http: 'HTTP 链接，将提交 115 离线下载',
}

async function go() {
  const raw = input.value.trim()
  if (!raw) {
    message.warning('输入片名、TMDB ID，或粘贴链接')
    return
  }
  if (linkKind.value) await submitLink(raw)
  else await searchTmdb(raw)
}

async function submitLink(raw: string) {
  if (linkKind.value === 'http') {
    const ok = await dialog.confirm({
      title: '提交 HTTP 离线下载',
      content: '这是一个普通网页链接。115 会按链接下载文件；如果它是网页而不是文件，下载下来的就是那个网页。确定提交吗？',
      actions: [
        { label: '取消', value: false, variant: 'tertiary' },
        { label: '提交离线下载', value: true, variant: 'primary' },
      ],
    })
    if (!ok) return
  }
  busy.value = true
  lastSubmit.value = null
  try {
    const r = await transferApi.submit({ url: raw, code: code.value.trim() })
    lastSubmit.value = { ok: true, text: r.message }
    message.success(r.message)
    input.value = ''
    code.value = ''
  } catch (e) {
    const text = e instanceof Error ? e.message : '提交失败'
    lastSubmit.value = { ok: false, text }
    toastError(e, '提交失败')
  } finally {
    busy.value = false
  }
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
  lastSubmit.value = null
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
          placeholder="片名、TMDB ID，或直接粘贴磁力 / ed2k / 115 分享链接"
          :input-attrs="{ 'aria-label': '片名或链接', autocomplete: 'off' }"
          @enter="go"
        />
        <HInput
          v-if="linkKind === 'share' && !shareHasCode"
          v-model="code"
          class="bar-code"
          placeholder="提取码"
          :input-attrs="{ 'aria-label': '提取码', autocomplete: 'off' }"
          @enter="go"
        />
        <HButton variant="primary" :loading="busy" @click="go">
          <template #icon><Send v-if="linkKind" :size="15" /><Search v-else :size="15" /></template>
          {{ linkKind ? (linkKind === 'share' ? '转存' : '离线下载') : '搜索' }}
        </HButton>
      </div>
      <div class="bar-sub">
        <span v-if="linkKind" class="kind">{{ LINK_LABEL[linkKind] }}</span>
        <span v-if="folderPath">转存到 <b class="mono">{{ folderPath }}</b>，完成后自动整理入库</span>
        <span v-else-if="srcState" class="warn">还没设置转存目录，分享链接转存不了</span>
        <button type="button" class="link" @click="router.push({ query: { tab: 'sources' } })">
          {{ folderPath ? '更改' : '去设置' }}
        </button>
      </div>
      <p v-if="lastSubmit" class="result" :class="lastSubmit.ok ? 'ok' : 'err'">{{ lastSubmit.text }}</p>
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
          </div>
          <p v-if="selected.cand?.overview" class="sel-overview">{{ selected.cand.overview }}</p>
          <p v-else-if="!selected.cand" class="sel-overview">
            直接用关键词搜，没经过 TMDB：结果不做片名比对，RE0 不可用（它按 TMDB 条目查资源）。
          </p>
        </div>
      </div>
      <ResourcePanel :query="selected.query" :sources="sources" />
    </SectionCard>
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
.bar-code {
  width: 110px;
  flex: none;
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
.result {
  margin: 8px 0 0;
  font-size: 13px;
}
.result.ok {
  color: var(--success);
}
.result.err {
  color: var(--danger);
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
  .bar-code {
    flex: 1;
  }
  .sel-poster {
    width: 56px;
    height: 84px;
  }
}
</style>
