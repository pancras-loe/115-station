<script setup lang="ts">
import MediaTypeChip from '@/components/MediaTypeChip.vue'
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import HButton from '@/components/hero/HButton.vue'
import { ArrowLeft, BellPlus, BellRing, Check, SlidersHorizontal, Star } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import PosterImage from '@/components/PosterImage.vue'
import CandidateGrid from '@/components/transfer/CandidateGrid.vue'
import DiscoverHome from '@/components/transfer/DiscoverHome.vue'
import DiscoverPanel from '@/components/transfer/DiscoverPanel.vue'
import SearchBar from '@/components/transfer/SearchBar.vue'
import ResourcePanel from '@/components/transfer/ResourcePanel.vue'
import SubscribeDialog from '@/components/subscribe/SubscribeDialog.vue'
import type { SubTarget } from '@/components/subscribe/SubscribeDialog.vue'
import type { Subscription } from '@/api/subscribe'
import { useSubscriptions } from '@/composables/subscriptions'
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
// 从哪儿点进来的：「换一部」回到榜单还是搜索结果
const from = ref<'pick' | 'idle'>('pick')
// 发现首页点了「查看全部」/「按条件筛选」：换成那个榜单的整页海报墙（null = 首页）
const browse = ref<string | null>(null)

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

function pick(c: TmdbCandidate, origin: 'pick' | 'idle' = 'pick') {
  from.value = origin
  selected.value = {
    cand: c,
    query: { tmdb_id: c.id, type: c.media_type, title: c.title, original_title: c.original_title, year: c.year },
  }
  stage.value = 'resources'
}

// ---- 订阅：选定影片后可以直接订阅，订阅过的显示进度、点了在原地打开详情抽屉 ----
// 订阅过没有直接看页面上那份订阅列表：抽屉里取消订阅、改状态都会刷新它，这里跟着变
const subs = useSubscriptions()
const sub = computed(() => {
  const c = selected.value?.cand
  return c ? subs.findSub(c.id, c.media_type) : null
})
const subOpen = ref(false)
const subTarget = computed<SubTarget | null>(() => {
  const c = selected.value?.cand
  return c ? { tmdb_id: c.id, media_type: c.media_type, title: c.title, year: c.year, poster: c.poster } : null
})
function subLabel(s: Subscription) {
  if (s.state === 'done') return '已订阅 · 已完成'
  if (s.media_type === 'movie') return '已订阅'
  return s.missing ? `已订阅 · 缺 ${s.missing} 集` : '已订阅'
}

function skipTmdb() {
  const kw = input.value.trim()
  if (!kw) return
  selected.value = { query: { title: kw } }
  from.value = 'pick'
  stage.value = 'resources'
}

function back() {
  stage.value = from.value === 'pick' && cands.value.length ? 'pick' : 'idle'
}

// 从搜索结果回到榜单
function toDiscover() {
  cands.value = []
  candHint.value = ''
  stage.value = 'idle'
}
function toFolder() {
  router.push({ name: 'accounts', query: { tab: 'library' } })
}
const img = (p: string | undefined, size: string) => (p ? resourcesApi.tmdbImageUrl(p, size) : null)

const sources = computed(() => srcState.value?.sources ?? [])
const folderPath = computed(() => srcState.value?.folder_path || srcState.value?.folder || '')
const selOwned = computed(() => {
  const c = selected.value?.cand
  return c ? owned.value[`${c.media_type}:${c.id}`] : undefined
})
</script>

<template>
  <div class="stack">
    <!-- 发现首页时搜索框嵌在横幅里，其余时候单独一行；同一个 v-model，切过去输入不丢 -->
    <SearchBar
      v-if="stage !== 'idle' || browse"
      v-model="input"
      :busy="busy"
      :looks-like-link="looksLikeLink"
      :folder-path="folderPath"
      :ready="!!srcState"
      @go="go"
      @to-link="toLinkTab"
      @folder="toFolder"
    />

    <!-- 发现页用 v-show 保活：点进一部再「换一部」回来，滑到的位置、翻到的页数还在 -->
    <div v-show="stage === 'idle'">
      <DiscoverHome v-show="!browse" @pick="(c) => pick(c, 'idle')" @browse="(k) => (browse = k)">
        <template #search>
          <SearchBar
            v-if="stage === 'idle' && !browse"
            v-model="input"
            overlay
            :busy="busy"
            :looks-like-link="looksLikeLink"
            :folder-path="folderPath"
            :ready="!!srcState"
            @go="go"
            @to-link="toLinkTab"
            @folder="toFolder"
          />
        </template>
      </DiscoverHome>
      <SectionCard v-if="browse">
        <DiscoverPanel :key="browse" :initial="browse" @pick="(c) => pick(c, 'idle')" @back="browse = null" />
      </SectionCard>
    </div>

    <SectionCard v-if="stage === 'pick'" title="选择影片" hint="先在 TMDB 定下是哪一部，再拿规范片名去各站搜">
      <template #extra>
        <HButton variant="ghost" size="sm" @click="toDiscover">返回发现</HButton>
      </template>
      <CandidateGrid :items="cands" :owned="owned" :loading="busy" :hint="candHint" @pick="pick" @skip="skipTmdb" />
    </SectionCard>

    <template v-if="stage === 'resources' && selected">
      <!-- 选定影片的头图：背景图铺底（没有就拿海报放大虚化），海报 + 片名 + 简介 + 订阅 -->
      <section class="hero-sel on-dark">
        <div class="hs-bg" aria-hidden="true">
          <img v-if="selected.cand?.backdrop" class="hs-img" :src="img(selected.cand.backdrop, 'w1280')!" alt="" />
          <img v-else-if="selected.cand?.poster" class="hs-blur" :src="img(selected.cand.poster, 'w342')!" alt="" />
        </div>
        <div class="hs-scrim" aria-hidden="true" />
        <div class="hs-inner">
          <HButton variant="tertiary" size="sm" class="hs-back" @click="back">
            <template #icon><ArrowLeft :size="15" /></template>
            换一部
          </HButton>
          <div class="hs-body">
            <PosterImage
              v-if="selected.cand"
              class="hs-poster"
              :src="img(selected.cand.poster, 'w342')"
              :alt="selected.cand.title"
              :icon-size="26"
            />
            <div class="hs-text">
              <h2 class="hs-title">{{ selected.query.title }}</h2>
              <div class="hs-meta">
                <MediaTypeChip v-if="selected.cand" :type="selected.cand.media_type" />
                <span v-if="selected.query.year">{{ selected.query.year }}</span>
                <span v-if="selected.cand?.vote" class="hs-rating">
                  <Star :size="13" :stroke-width="0" fill="currentColor" />{{ selected.cand.vote.toFixed(1) }}
                </span>
                <span
                  v-if="selected.cand?.original_title && selected.cand.original_title !== selected.query.title"
                  class="hs-orig"
                >{{ selected.cand.original_title }}</span>
                <span v-if="selOwned" class="hs-owned">
                  <Check :size="12" :stroke-width="3" />已入库{{ selOwned.category ? ` · ${selOwned.category}` : '' }}
                </span>
              </div>
              <p v-if="selected.cand?.overview" class="hs-overview">{{ selected.cand.overview }}</p>
              <p v-else-if="!selected.cand" class="hs-overview">
                直接用关键词搜，没经过 TMDB：结果不做片名比对，RE0 与癫影不可用（它们按 TMDB 条目查资源）。
              </p>
              <div v-if="selected.cand" class="hs-actions">
                <HButton v-if="sub" variant="primary" @click="subs.openDetail(sub.id)">
                  <template #icon><BellRing :size="15" /></template>
                  {{ subLabel(sub) }}
                </HButton>
                <template v-else>
                  <!-- 一键订阅按默认值；要改范围 / 资源条件再点旁边的「自定义订阅」开弹窗 -->
                  <HButton
                    variant="primary"
                    :loading="subs.quickBusy.value.has(`${selected.cand.media_type}:${selected.cand.id}`)"
                    :title="selected.cand.media_type === 'tv' ? '按默认设置补缺集，在播的多季剧只订最新一季' : '找到资源自动转存入库'"
                    @click="subs.quickSubscribe(selected.cand.id, selected.cand.media_type)"
                  >
                    <template #icon><BellPlus :size="15" /></template>
                    订阅
                  </HButton>
                  <HButton variant="tertiary" title="自己选范围、资源条件后再订阅" @click="subOpen = true">
                    <template #icon><SlidersHorizontal :size="15" /></template>
                    自定义订阅
                  </HButton>
                </template>
              </div>
            </div>
          </div>
        </div>
      </section>
      <SectionCard title="各站资源">
        <ResourcePanel :query="selected.query" :sources="sources" />
      </SectionCard>
    </template>
    <SubscribeDialog v-model:show="subOpen" :target="subTarget" @saved="subs.load()" />
  </div>
</template>

<style scoped>
.stack {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

/* ---- 选定影片的头图（与发现页横幅同一套暗底，字是浅色 .on-dark） ---- */
.hero-sel {
  position: relative;
  isolation: isolate;
  overflow: hidden;
  border-radius: var(--r-card);
  background: oklch(0.2 0.03 50);
  box-shadow: 0 30px 60px -36px rgb(0 0 0 / 0.7);
}
.hs-bg {
  position: absolute;
  inset: 0;
  z-index: -2;
}
.hs-img,
.hs-blur {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  object-fit: cover;
}
.hs-img {
  object-position: center 25%;
  animation: hs-zoom 14s ease-out both;
}
/* 没有背景图时的替身：海报放大、重度虚化，只取它的色调 */
.hs-blur {
  filter: blur(48px) saturate(1.4) brightness(0.7);
  transform: scale(1.3);
}
@keyframes hs-zoom {
  from {
    transform: scale(1.06);
  }
  to {
    transform: scale(1);
  }
}
.hs-scrim {
  position: absolute;
  inset: 0;
  z-index: -1;
  background:
    linear-gradient(to right, rgb(0 0 0 / 0.88) 0%, rgb(0 0 0 / 0.6) 50%, rgb(0 0 0 / 0.25) 100%),
    linear-gradient(to top, rgb(0 0 0 / 0.6) 0%, rgb(0 0 0 / 0) 50%);
}
.hs-inner {
  display: flex;
  flex-direction: column;
  gap: 14px;
  padding: 16px 26px 24px;
}
.hs-back {
  align-self: flex-start;
  backdrop-filter: blur(10px);
  -webkit-backdrop-filter: blur(10px);
}
.hs-body {
  display: flex;
  align-items: flex-end;
  gap: 24px;
}
.hs-poster {
  width: 150px;
  flex: none;
  border-radius: 12px;
  box-shadow:
    0 24px 48px -18px rgb(0 0 0 / 0.85),
    0 0 0 1px rgb(255 255 255 / 0.12);
}
.hs-text {
  min-width: 0;
  flex: 1;
  max-width: 760px;
}
.hs-title {
  margin: 0;
  font-size: clamp(24px, 3vw, 36px);
  font-weight: 800;
  line-height: 1.15;
  letter-spacing: -0.025em;
  text-shadow: 0 2px 20px rgb(0 0 0 / 0.5);
}
.hs-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 12px;
  margin-top: 10px;
  font-size: 13.5px;
  color: var(--muted);
}
.hs-rating {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-weight: 700;
  color: oklch(0.86 0.15 85);
}
.hs-orig {
  font-style: italic;
}
.hs-owned {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  padding: 0 8px;
  border-radius: 999px;
  font-size: 11.5px;
  font-weight: 600;
  line-height: 20px;
  background: var(--success);
  color: var(--success-foreground);
}
.hs-overview {
  margin: 12px 0 0;
  font-size: 13.5px;
  line-height: 1.7;
  color: rgb(255 255 255 / 0.8);
  display: -webkit-box;
  -webkit-line-clamp: 4;
  line-clamp: 4;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.hs-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  margin-top: 16px;
}
.hs-actions .button--tertiary {
  backdrop-filter: blur(10px);
  -webkit-backdrop-filter: blur(10px);
}
@media (prefers-reduced-motion: reduce) {
  .hs-img {
    animation: none;
  }
}
@media (max-width: 720px) {
  .hero-sel {
    border-radius: var(--r-xl);
  }
  .hs-inner {
    padding: 12px 16px 18px;
  }
  .hs-body {
    gap: 14px;
    align-items: flex-start;
  }
  .hs-poster {
    width: 92px;
  }
  .hs-overview {
    -webkit-line-clamp: 3;
    line-clamp: 3;
    font-size: 13px;
  }
}
</style>
