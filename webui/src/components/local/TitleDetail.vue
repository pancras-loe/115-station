<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import {
  Clapperboard,
  ExternalLink,
  FileText,
  Image as ImageIcon,
  Images,
  Radar,
  Search,
  Sparkles,
  Tv,
  X,
} from '@lucide/vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HDrawer from '@/components/hero/HDrawer.vue'
import HSkeleton from '@/components/hero/HSkeleton.vue'
import HTabs from '@/components/hero/HTabs.vue'
import DetailFiles from './DetailFiles.vue'
import DetailEmby from './DetailEmby.vue'
import { localApi } from '@/api'
import type { LocalTitle, LocalTitleDetail, TitleEmby } from '@/api/local'
import { toastError } from '@/composables/useFeedback'
import { useQueueStore } from '@/stores/queue'
import { relTime, fullTime } from '@/utils/time'
import { STATUS_TEXT, STATUS_TONE, statusTitle } from '@/utils/localStatus'

/**
 * 本地文件页的片目详情（右侧抽屉，手机上全屏）。卡片墙只放要紧的，细节都在这里：
 * 顶部是海报、片名与操作；下面两个页签 —— 刮削文件（本地，零 115 请求）、媒体信息（Emby）。
 * 打开时两边一起加载：Emby 那边只是一两次查询，顶部的概况也要用到它的数字。
 */
const props = defineProps<{ titleKey: string | null }>()
const show = defineModel<boolean>('show', { required: true })
const emit = defineEmits<{ scrape: [t: LocalTitle, preset: 'auto' | 'pick'] }>()

const queue = useQueueStore()

const detail = ref<LocalTitleDetail | null>(null)
const loading = ref(false)
const error = ref('')
const emby = ref<TitleEmby | null>(null)
const embyLoading = ref(false)
const tab = ref<'files' | 'emby'>('files')

let seq = 0
async function loadDetail() {
  const key = props.titleKey
  if (!key) return
  const my = ++seq
  loading.value = true
  error.value = ''
  try {
    const d = await localApi.titleDetail(key)
    if (my === seq) detail.value = d
  } catch (e) {
    if (my === seq) error.value = (e as Error).message || '读取片目详情失败'
  } finally {
    if (my === seq) loading.value = false
  }
}

let embySeq = 0
async function loadEmby() {
  const key = props.titleKey
  if (!key) return
  const my = ++embySeq
  embyLoading.value = true
  try {
    const d = await localApi.titleEmby(key)
    if (my === embySeq) emby.value = d
  } catch (e) {
    if (my === embySeq) toastError(e, '读取 Emby 媒体信息失败')
  } finally {
    if (my === embySeq) embyLoading.value = false
  }
}

watch(
  () => [show.value, props.titleKey] as const,
  ([open, key], prev) => {
    if (!open || !key) return
    if (prev && prev[1] === key && prev[0] && detail.value) return
    detail.value = null
    emby.value = null
    tab.value = 'files'
    void loadDetail()
    void loadEmby()
  },
  { immediate: true },
)

// 刮削跑完：本地产物变了，重读；探测结果可能也更新了
const offFinished = queue.onFinished((j) => {
  if ((j.kind === 'scrape' || j.kind === 'probe') && show.value) {
    void loadDetail()
    void loadEmby()
  }
})
onBeforeUnmount(offFinished)

const d = computed(() => detail.value)
const tv = computed(() => d.value?.media_type === 'tv')
// 头部海报 108px 宽（手机 84px），按两倍要图就够，不必拿卡片墙最大档
const posterSrc = computed(() => (d.value?.poster ? localApi.posterUrl(d.value, 108 * Math.max(2, window.devicePixelRatio || 1)) : ''))
const fanartSrc = computed(() => (d.value ? localApi.fanartUrl(d.value) : ''))
const posterBroken = ref(false)
watch(posterSrc, () => (posterBroken.value = false))

const seasonCount = computed(() => d.value?.seasons?.length ?? 0)
const metaParts = computed(() => {
  const x = d.value
  if (!x) return []
  const parts = [x.media_type === 'tv' ? '剧集' : '电影']
  if (x.year) parts.push(x.year)
  if (x.category) parts.push(x.category)
  if (x.media_type === 'tv') parts.push(seasonCount.value > 1 ? `${seasonCount.value} 季 ${x.videos} 集` : `${x.videos} 集`)
  else if (x.videos > 1) parts.push(`${x.videos} 个视频`)
  return parts
})

// 抽屉里地方够：缺什么全写出来（卡片上只写第一样）
const statusText = computed(() => {
  const x = d.value
  if (!x) return ''
  return x.status === 'partial' && x.lack?.length ? `缺${x.lack.join('、')}` : STATUS_TEXT[x.status]
})

const tmdbUrl = computed(() =>
  d.value?.tmdb_id ? `https://www.themoviedb.org/${d.value.media_type === 'tv' ? 'tv' : 'movie'}/${d.value.tmdb_id}` : '',
)

// ---- 概况 ----

interface Stat {
  key: string
  label: string
  value: string
  sub: string
  tone: 'success' | 'warning' | 'danger' | 'default'
  icon: typeof FileText
}
function tone(have: number, total: number): Stat['tone'] {
  if (!total) return 'default'
  if (have === total) return 'success'
  return have === 0 ? 'danger' : 'warning'
}
const stats = computed<Stat[]>(() => {
  const x = d.value
  if (!x) return []
  const s = x.summary
  const out: Stat[] = [
    { key: 'nfo', label: 'NFO 元数据', value: `${s.nfo_have}/${s.nfo_total}`, sub: s.nfo_have === s.nfo_total ? '齐全' : `缺 ${s.nfo_total - s.nfo_have} 个`, tone: tone(s.nfo_have, s.nfo_total), icon: FileText },
    { key: 'img', label: '图片', value: `${s.img_have}/${s.img_total}`, sub: x.has_poster ? '海报已有' : '缺海报', tone: x.has_poster ? (s.img_have === s.img_total ? 'success' : 'default') : 'danger', icon: ImageIcon },
  ]
  if (tv.value) {
    out.push({ key: 'thumb', label: '集剧照', value: `${s.thumb_have}/${s.thumb_total}`, sub: s.thumb_have === s.thumb_total ? '齐全' : '可选', tone: s.thumb_have === s.thumb_total && s.thumb_total ? 'success' : 'default', icon: Images })
  }
  const e = emby.value
  let ev = '…'
  let es = embyLoading.value ? '查询中' : ''
  let et: Stat['tone'] = 'default'
  if (e && !e.configured) {
    ev = '—'
    es = '未配置 Emby'
  } else if (e && (e.error || !e.found)) {
    ev = '—'
    es = e.error ? '查询失败' : '尚未入库'
    et = 'warning'
  } else if (e) {
    const c = e.counts
    const done = c.done ?? 0
    const todo = (c.none ?? 0) + (c.retry ?? 0)
    const busy = (c.queued ?? 0) + (c.running ?? 0)
    const held = (c.wait ?? 0) + (c.exhausted ?? 0)
    ev = `${done}/${e.items.length}`
    if (done === e.items.length) es = '全部已探测'
    else if (busy) es = `${busy} 个探测中`
    else if (todo) es = `${todo} 个未探测`
    else if (held) es = `${held} 个暂不探测`
    else es = `${e.items.length - done} 个无法探测`
    et = tone(done, e.items.length)
  }
  out.push({ key: 'emby', label: '媒体信息', value: ev, sub: es, tone: et, icon: Radar })
  return out
})

const embyBadge = computed(() => {
  const e = emby.value
  if (!e?.found) return 0
  return e.items.length - (e.counts.done ?? 0)
})

const tabs = computed(() => [
  { value: 'files' as const, label: '刮削文件' },
  { value: 'emby' as const, label: '媒体信息', count: embyBadge.value, countTone: 'warning' as const },
])

function scrape(preset: 'auto' | 'pick') {
  if (d.value) emit('scrape', d.value, preset)
}
function onStat(key: string) {
  tab.value = key === 'emby' ? 'emby' : 'files'
}
</script>

<template>
  <HDrawer v-model:show="show" side="right" width="min(760px, 100vw)" :title="d?.title || '片目详情'">
    <div class="td">
      <!-- 头图：背景图模糊铺底，海报 + 片名 + 操作 -->
      <header class="hero">
        <div class="hero-bg" :style="fanartSrc ? { backgroundImage: `url('${fanartSrc}')` } : undefined" />
        <div class="hero-shade" />
        <HButton class="close" variant="ghost" size="sm" icon-only aria-label="关闭" @click="show = false">
          <X :size="18" />
        </HButton>
        <div class="hero-main">
          <div class="hero-poster">
            <Tv v-if="tv" :size="28" />
            <Clapperboard v-else :size="28" />
            <img v-if="posterSrc && !posterBroken" :src="posterSrc" :alt="d?.title" @error="posterBroken = true" />
          </div>
          <div class="hero-text">
            <template v-if="d">
              <h2 class="hero-title">{{ d.title }}</h2>
              <p class="hero-meta">{{ metaParts.join(' · ') }}</p>
              <div class="hero-chips">
                <HChip :color="STATUS_TONE[d.status]" variant="primary" size="sm" :title="statusTitle(d)">{{ statusText }}</HChip>
                <a v-if="tmdbUrl" :href="tmdbUrl" target="_blank" rel="noopener noreferrer" class="tmdb">
                  TMDB {{ d.tmdb_id }}<ExternalLink :size="11" />
                </a>
                <span v-else class="no-id" title="目录名里没有 TMDB 编号：刮削时按片名识别，识别不出来可以改指定">
                  <Sparkles :size="12" />无 TMDB 编号
                </span>
                <span class="hero-time" :title="fullTime(d.last_at)">入库 {{ relTime(d.last_at) }}</span>
              </div>
              <div class="hero-actions">
                <HButton variant="primary" size="sm" @click="scrape('auto')"><Images :size="14" />刮削</HButton>
                <HButton variant="secondary" size="sm" @click="scrape('pick')"><Search :size="14" />改指定 TMDB 条目</HButton>
              </div>
            </template>
            <template v-else-if="loading">
              <HSkeleton width="60%" height="20px" radius="999px" />
              <HSkeleton width="40%" height="12px" radius="999px" />
            </template>
          </div>
        </div>
      </header>

      <div class="td-body">
        <HAlert v-if="error" status="danger">
          {{ error }}
          <template #actions><HButton variant="tertiary" size="sm" @click="loadDetail">重试</HButton></template>
        </HAlert>

        <template v-if="d">
          <HAlert v-if="d.missing" status="warning">
            本地找不到这个片目的目录（被手工删掉，或挂载还没就绪）。台账里还有它，刮削会跳过没有 STRM 的片目。
          </HAlert>

          <div class="stats">
            <button v-for="s in stats" :key="s.key" type="button" class="stat" :class="`tone-${s.tone}`" @click="onStat(s.key)">
              <span class="stat-label"><component :is="s.icon" :size="13" />{{ s.label }}</span>
              <span class="stat-value">{{ s.value }}</span>
              <span class="stat-sub">{{ s.sub }}</span>
            </button>
          </div>

          <HTabs v-model="tab" :items="tabs" variant="secondary" />

          <DetailFiles v-if="tab === 'files'" :detail="d" />
          <DetailEmby
            v-else
            :data="emby"
            :loading="embyLoading"
            :title-key="d.key"
            :media-type="d.media_type"
            @reload="loadEmby"
          />
        </template>
        <div v-else-if="loading" class="sk">
          <HSkeleton v-for="i in 4" :key="i" width="100%" height="56px" radius="12px" />
        </div>
      </div>
    </div>
  </HDrawer>
</template>

<style scoped>
.td {
  display: flex;
  flex-direction: column;
  flex: 1;
  min-height: 0;
}
.hero {
  position: relative;
  flex: none;
  overflow: hidden;
  padding: 20px 20px 18px;
  background: var(--surface-secondary);
}
.hero-bg {
  position: absolute;
  inset: -20px;
  background-position: center 30%;
  background-size: cover;
  filter: blur(18px) saturate(1.2);
  opacity: 0.55;
  transform: scale(1.05);
}
.hero-shade {
  position: absolute;
  inset: 0;
  background: linear-gradient(180deg, color-mix(in oklab, var(--background) 25%, transparent), var(--background));
}
.close {
  position: absolute;
  top: 10px;
  right: 10px;
  z-index: 1;
}
.hero-main {
  position: relative;
  display: flex;
  gap: 16px;
  padding-top: 8px;
}
.hero-poster {
  position: relative;
  display: grid;
  place-items: center;
  flex: none;
  width: 108px;
  aspect-ratio: 2 / 3;
  border-radius: var(--r-lg);
  background: var(--surface-tertiary, var(--surface));
  box-shadow: 0 8px 24px color-mix(in oklab, #000 25%, transparent);
  color: var(--muted);
  overflow: hidden;
}
.hero-poster img {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  object-fit: cover;
}
.hero-text {
  display: flex;
  flex-direction: column;
  justify-content: flex-end;
  gap: 8px;
  flex: 1;
  min-width: 0;
  padding-right: 28px;
}
.hero-title {
  margin: 0;
  font-size: 20px;
  font-weight: 700;
  line-height: 1.3;
  letter-spacing: -0.01em;
  word-break: break-word;
}
.hero-meta {
  margin: 0;
  font-size: 12.5px;
  color: var(--muted);
}
.hero-chips {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 10px;
  font-size: 12px;
}
.tmdb {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  color: var(--accent);
  text-decoration: none;
}
.tmdb:hover {
  text-decoration: underline;
}
.no-id {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  color: var(--warning);
}
.hero-time {
  color: var(--muted);
}
.hero-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.td-body {
  display: flex;
  flex-direction: column;
  gap: 16px;
  flex: 1;
  min-height: 0;
  padding: 4px 20px 28px;
  overflow-y: auto;
  overscroll-behavior: contain;
}
.sk {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.stats {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(130px, 1fr));
  gap: 8px;
}
.stat {
  all: unset;
  box-sizing: border-box;
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 10px 12px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  cursor: pointer;
  transition: background 0.15s;
}
.stat:hover {
  background: color-mix(in oklab, var(--surface-secondary) 80%, var(--foreground) 6%);
}
.stat:focus-visible {
  box-shadow: inset 0 0 0 2px var(--focus);
}
.stat-label {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 12px;
  color: var(--muted);
}
.stat-value {
  font-size: 18px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}
.stat-sub {
  font-size: 11.5px;
  color: var(--muted);
}
.stat.tone-success .stat-value {
  color: var(--success);
}
.stat.tone-warning .stat-value {
  color: var(--warning);
}
.stat.tone-danger .stat-value {
  color: var(--danger);
}

@media (max-width: 720px) {
  .hero {
    padding: 14px 16px;
  }
  .hero-poster {
    width: 84px;
  }
  .hero-title {
    font-size: 17px;
  }
  .td-body {
    padding: 4px 16px 24px;
  }
  .stats {
    grid-template-columns: 1fr 1fr;
  }
}
</style>
