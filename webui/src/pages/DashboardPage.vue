<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import {
  ChevronRight,
  Clapperboard,
  Cpu,
  Database,
  FileVideo,
  MemoryStick,
  RefreshCw,
  SlidersHorizontal,
  Tv,
} from '@lucide/vue'
import { dashboardApi } from '@/api'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HSkeleton from '@/components/hero/HSkeleton.vue'
import HTooltip from '@/components/hero/HTooltip.vue'
import type { Dashboard } from '@/types/dashboard'
import StatCard from '@/components/ui/StatCard.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import MeterBar from '@/components/ui/MeterBar.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import WeeklyChart from '@/components/ui/WeeklyChart.vue'
import PosterImage from '@/components/PosterImage.vue'
import CalibrateModal from '@/components/dashboard/CalibrateModal.vue'
import HeroBanner, { type HeroSlide } from '@/components/dashboard/HeroBanner.vue'
import { bytes, num, percent } from '@/utils/format'
import { embyImageUrl, posterUrl } from '@/utils/media'
import { toastError } from '@/composables/useFeedback'

const data = ref<Dashboard | null>(null)
const loading = ref(true)
const refreshing = ref(false)
const calibrating = ref(false)

async function load(manual = false, force = false) {
  if (force) refreshing.value = true
  try {
    data.value = await dashboardApi.get(force)
  } catch (e) {
    // 轮询失败静默：30 秒一次的后台刷新不该把错误提示刷满屏
    if (manual) toastError(e, '仪表盘加载失败')
  } finally {
    loading.value = false
    refreshing.value = false
  }
}

let timer: number | undefined
onMounted(() => {
  load(true)
  // 后端对 115 容量（5min）和 Emby（60s）各有缓存，30s 轮询不会打到上游
  timer = window.setInterval(() => load(), 30_000)
})
onUnmounted(() => clearInterval(timer))

const storage = computed(() => data.value?.storage ?? {})
const storagePct = computed(() => {
  const s = storage.value
  return s.total && s.total > 0 ? Math.min(100, ((s.used ?? 0) / s.total) * 100) : 0
})
const storageEnabled = computed(() => storage.value.enabled !== false && !!storage.value.total)

const sys = computed(() => data.value?.sys ?? {})
const media = computed(() => data.value?.media)
const strm = computed(() => data.value?.strm)

/** 数字是谁给的：Emby 接上了就以 Emby 为准，否则只能用本地整理台账 */
const fromEmby = computed(() => media.value?.source === 'emby')

/**
 * 台账「虚高」：本地整理台账比 Emby 实际条目多出一截。
 * 手工删片、解除媒体库目录关联都只影响 Emby 和本地 STRM，台账不会自己缩回去，
 * 这时候面板上任何按台账算的数字都偏大 —— 提示用户校准，而不是让他自己猜。
 */
const localTotal = computed(() => (media.value?.local_movies ?? 0) + (media.value?.local_tvs ?? 0))
const drift = computed(() => {
  if (!fromEmby.value || !media.value) return 0
  return localTotal.value - media.value.total
})
const driftNotable = computed(() => drift.value >= 10)

/** 「3 小时前」：Emby 给的是 ISO 时间；本地台账已经是「09-30 12:00」这种短格式，原样用 */
function ago(iso?: string): string {
  if (!iso) return ''
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return ''
  const m = Math.max(0, Math.round((Date.now() - t) / 60000))
  if (m < 1) return '刚刚'
  if (m < 60) return `${m} 分钟前`
  const h = Math.round(m / 60)
  if (h < 24) return `${h} 小时前`
  const d = Math.round(h / 24)
  return d < 30 ? `${d} 天前` : new Date(t).toLocaleDateString()
}

const kindLabel = (t?: string) =>
  ({ Movie: '电影', Series: '剧集', movie: '电影', tv: '剧集' })[t ?? ''] ?? ''

/** 横幅：Emby 的最新入库优先（有背景图、Logo），退回本地最近整理（只有 TMDB 海报） */
const heroSlides = computed<HeroSlide[]>(() => {
  const emby = data.value?.emby
  if (emby?.recent?.length) {
    return emby.recent.slice(0, 6).map((m) => ({
      key: m.id,
      title: m.name,
      year: m.year,
      kind: kindLabel(m.type),
      overview: m.overview,
      rating: m.rating || undefined,
      genres: m.genres ?? undefined,
      badge: m.official_rating || undefined,
      when: ago(m.created),
      backdrop: m.has_backdrop ? embyImageUrl(`Items/${m.id}/Images/Backdrop`, 1600) : null,
      poster: embyImageUrl(`Items/${m.id}/Images/Primary`, 400),
      logo: m.has_logo ? embyImageUrl(`Items/${m.id}/Images/Logo`, 600) : null,
    }))
  }
  return (data.value?.recent_media ?? []).slice(0, 6).map((m, i) => ({
    key: `${m.title}-${i}`,
    title: m.title,
    year: m.year,
    kind: kindLabel(m.type) || m.category,
    overview: m.overview,
    rating: m.rating || undefined,
    when: m.at,
    poster: posterUrl(m.poster, 'w342'),
  }))
})

/** Emby 接上了就以 Emby 媒体库为准，否则退回本地整理台账的分类 */
const categories = computed(() => {
  const emby = data.value?.emby
  if (emby?.libraries?.length) {
    return emby.libraries.map((l) => ({
      name: l.name,
      count: l.count,
      label: l.type_label ?? '',
      posters: (l.collage ?? []).map((p) => embyImageUrl(p, 200)),
    }))
  }
  return (data.value?.categories ?? []).map((c) => ({
    name: c.name,
    count: c.count,
    label: '',
    posters: (c.posters ?? []).map((p) => posterUrl(p, 'w185') as string),
  }))
})

/** 海报行同理：Emby 的最新入库优先，退回本地最近整理 */
const wall = computed(() => {
  const emby = data.value?.emby
  if (emby?.recent?.length) {
    return emby.recent.map((m) => ({
      key: m.id,
      title: m.name,
      sub: [m.year, kindLabel(m.type)].filter(Boolean).join(' · '),
      src: embyImageUrl('Items/' + m.id + '/Images/Primary', 320),
    }))
  }
  return (data.value?.recent_media ?? []).map((m, i) => ({
    key: m.title + '-' + i,
    title: m.title,
    sub: [m.year, kindLabel(m.type)].filter(Boolean).join(' · '),
    src: posterUrl(m.poster, 'w342'),
  }))
})

const recent = computed(() => data.value?.recent_media ?? [])

/** STRM 卡片的副标题：两种「坏掉」的口径分开说，别混成一个「失效 N」 */
const strmSub = computed(() => {
  const s = strm.value
  if (!s) return ''
  const parts = [`失效 ${num(s.orphan)}`]
  if (s.missing > 0) parts.push(`本地缺失 ${num(s.missing)}/${num(s.missing_sampled)}`)
  parts.push(`台账 ${num(data.value?.synced_files)}`)
  return parts.join(' · ')
})
</script>

<template>
  <div class="dash">
    <!-- ==== 影院横幅 ==== -->
    <HeroBanner :slides="heroSlides" :loading="loading">
      <template #extra>
        <HTooltip
          :content="
            fromEmby
              ? 'Emby 已接入，电影/剧集数量与媒体库卡片都直接读 Emby，和 Emby 界面上的一致'
              : '未配置 Emby 或暂时不可达，只能按本地整理台账统计'
          "
          side="top"
        >
          <span class="src" tabindex="0">
            <span class="src-dot" :class="fromEmby ? 'is-emby' : 'is-local'" />
            <span class="src-label">数量来自</span>
            <span class="src-value">{{ fromEmby ? 'Emby 媒体库' : '本地整理台账' }}</span>
          </span>
        </HTooltip>
        <HButton size="sm" variant="tertiary" :loading="refreshing" aria-label="刷新" @click="load(true, true)">
          <template #icon><RefreshCw /></template>
          <span class="btn-text">刷新</span>
        </HButton>
        <HButton size="sm" variant="tertiary" aria-label="校准台账" @click="calibrating = true">
          <template #icon><SlidersHorizontal /></template>
          <span class="btn-text">校准台账</span>
        </HButton>
      </template>
    </HeroBanner>

    <!-- ==== 指标行：往上浮，压住横幅的下沿 ==== -->
    <div class="stats">
      <template v-if="loading">
        <div v-for="i in 4" :key="i" class="card card--default stat-skel">
          <HSkeleton width="40%" height="12px" radius="999px" />
          <HSkeleton width="55%" height="26px" radius="8px" />
          <HSkeleton width="70%" height="10px" radius="999px" />
        </div>
      </template>
      <template v-else>
        <StatCard
          label="电影"
          :value="num(media?.movies)"
          :sub="'本月整理 +' + (media?.movies_month ?? 0)"
          :icon="Clapperboard"
          tone="primary"
        />
        <StatCard
          label="剧集"
          :value="num(media?.tvs)"
          :sub="
            media?.episodes
              ? num(media.episodes) + ' 集 · 本月整理 +' + (media?.tvs_month ?? 0)
              : '本月整理 +' + (media?.tvs_month ?? 0)
          "
          :icon="Tv"
          tone="success"
        />
        <StatCard
          label="STRM 文件"
          :value="num(strm?.total)"
          :sub="strmSub"
          :icon="FileVideo"
          :tone="(strm?.orphan ?? 0) > 0 ? 'warning' : 'primary'"
        />
        <StatCard
          label="整理台账"
          :value="num(data?.organized)"
          :sub="'待处理事件 ' + (data?.pending_events ?? 0)"
          :icon="Database"
          :tone="driftNotable ? 'warning' : 'primary'"
        />
      </template>
    </div>

    <!-- ==== 台账虚高提示 ==== -->
    <HAlert v-if="driftNotable" status="warning" :title="`台账比 Emby 多出 ${num(drift)} 部`">
      本地整理台账记着 {{ num(localTotal) }} 部，Emby 实际只有 {{ num(media?.total) }} 部，
      多出的多半是手工删片、解除媒体库目录关联之后留下的幽灵记录。
      按本地 STRM 目录核对一遍即可（只删台账行，不动网盘与 Emby）。
      <template #actions>
        <HButton size="sm" variant="primary" @click="calibrating = true">校准台账</HButton>
      </template>
    </HAlert>

    <!-- ==== 最新入库：海报行 ==== -->
    <section class="row">
      <header class="row-head">
        <h2 class="row-title">最新入库</h2>
        <span class="row-hint">{{ fromEmby ? '来自 Emby' : '来自本地整理记录' }}</span>
        <RouterLink :to="{ name: 'local' }" class="row-more">
          本地文件<ChevronRight :size="15" />
        </RouterLink>
      </header>
      <div v-if="loading" class="shelf">
        <HSkeleton v-for="i in 8" :key="i" class="shelf-skel" radius="14px" />
      </div>
      <div v-else-if="wall.length" class="shelf">
        <div v-for="m in wall" :key="m.key" class="shelf-item" :title="m.title">
          <PosterImage class="shelf-poster" :src="m.src" :alt="m.title" :icon-size="24" />
          <div class="shelf-title">{{ m.title }}</div>
          <div class="shelf-sub">{{ m.sub }}</div>
        </div>
      </div>
      <EmptyState v-else text="暂无入库 · 整理或同步后这里会显示最新的影片" />
    </section>

    <!-- ==== 我的媒体库 ==== -->
    <section class="row">
      <header class="row-head">
        <h2 class="row-title">我的媒体库</h2>
        <span class="row-hint">
          {{ fromEmby ? '直接读 Emby 媒体库，按库类型计数' : '按本地整理台账的分类聚合' }}
        </span>
      </header>
      <div v-if="categories.length" class="libs">
        <div v-for="c in categories" :key="c.name" class="lib" :title="c.name + ' · ' + c.count + ' 部'">
          <div class="lib-strip">
            <PosterImage v-for="i in 4" :key="i" :src="c.posters[i - 1]" :alt="c.name" :icon-size="16" />
          </div>
          <div class="lib-shade on-dark">
            <div class="lib-text">
              <span class="lib-name">{{ c.name }}</span>
              <span v-if="c.label && c.label !== c.name" class="lib-type">{{ c.label }}</span>
            </div>
            <span class="lib-count">{{ num(c.count) }}</span>
          </div>
        </div>
      </div>
      <EmptyState v-else-if="!loading" text="暂无入库记录 · 整理或同步后这里会显示分类卡片" />
    </section>

    <!-- ==== 容量 / 系统 / 趋势 ==== -->
    <div class="grid-3">
      <SectionCard title="115 存储空间" :hint="storage.username || undefined">
        <template v-if="storageEnabled">
          <div class="big-num">{{ storage.used_h || bytes(storage.used) }}</div>
          <div class="big-sub">已使用 {{ percent(storagePct) }}</div>
          <MeterBar class="mt" :percent="storagePct" />
          <div class="meta">
            可用 {{ bytes((storage.total ?? 0) - (storage.used ?? 0)) }} · 总容量
            {{ storage.total_h || bytes(storage.total) }}
          </div>
        </template>
        <EmptyState v-else text="115 账号未配置或 Cookie 已失效" />
      </SectionCard>

      <SectionCard title="系统负载">
        <div class="load">
          <div>
            <div class="load-head">
              <MemoryStick :size="15" :stroke-width="1.8" />
              <span>内存</span>
              <span class="load-pct">
                {{ sys.mem_percent !== undefined ? percent(sys.mem_percent) : '—' }}
              </span>
            </div>
            <MeterBar :percent="sys.mem_percent ?? 0" />
            <div class="meta">
              <template v-if="sys.mem_total_mb">
                {{ bytes((sys.mem_used_mb ?? 0) * 1048576) }} /
                {{ bytes(sys.mem_total_mb * 1048576) }}
              </template>
              <template v-else>不可用</template>
            </div>
          </div>
          <div>
            <div class="load-head">
              <Cpu :size="15" :stroke-width="1.8" />
              <span>CPU</span>
              <span class="load-pct">
                {{ sys.cpu_percent !== undefined ? percent(sys.cpu_percent) : '—' }}
              </span>
            </div>
            <MeterBar :percent="sys.cpu_percent ?? 0" />
          </div>
        </div>
      </SectionCard>

      <SectionCard title="近 7 天整理入库" :hint="'共 ' + (data?.week_total ?? 0) + ' 部'">
        <WeeklyChart :data="data?.weekly ?? []" />
      </SectionCard>
    </div>

    <!-- ==== 最近整理 ==== -->
    <SectionCard title="最近整理" hint="本站整理流水线最近处理完的影片">
      <div v-if="recent.length" class="recent">
        <div v-for="(m, i) in recent" :key="m.title + '-' + i" class="recent-item">
          <PosterImage class="recent-poster" :src="posterUrl(m.poster, 'w92')" :alt="m.title" />
          <div class="recent-body">
            <div class="recent-title">
              {{ m.title }} <span class="recent-year">{{ m.year }}</span>
            </div>
            <div class="recent-cat">{{ m.category || m.type }}</div>
          </div>
          <div class="recent-at">{{ m.at }}</div>
        </div>
      </div>
      <EmptyState v-else text="暂无整理记录" />
    </SectionCard>

    <CalibrateModal v-model:show="calibrating" @done="load(true, true)" />
  </div>
</template>

<style scoped>
.dash {
  display: flex;
  flex-direction: column;
  gap: 28px;
}

/* ---- 横幅右下角：数据来源胶囊（毛玻璃，压在暗图上） ---- */
.src {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  min-width: 0;
  height: 32px;
  padding: 0 12px;
  border-radius: 999px;
  background: rgb(0 0 0 / 0.3);
  border: 1px solid rgb(255 255 255 / 0.14);
  backdrop-filter: blur(10px);
  -webkit-backdrop-filter: blur(10px);
  font-size: 12.5px;
  outline: none;
}
.src:focus-visible {
  box-shadow: 0 0 0 2px var(--focus);
}
.src-dot {
  width: 7px;
  height: 7px;
  border-radius: 999px;
  flex-shrink: 0;
}
.src-dot.is-emby {
  background: var(--success);
  box-shadow: 0 0 0 3px color-mix(in oklab, var(--success) 30%, transparent);
}
.src-dot.is-local {
  background: var(--muted);
}
.src-label {
  color: var(--muted);
}
.src-value {
  font-weight: 500;
  white-space: nowrap;
}
.dash :deep(.hero .button--tertiary) {
  backdrop-filter: blur(10px);
  -webkit-backdrop-filter: blur(10px);
}

/* ---- 指标行：上移压住横幅底部的渐隐区，卡片半透明 + 模糊 ---- */
.stats {
  position: relative;
  z-index: 1;
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 16px;
  margin-top: -84px;
}
.stats > * {
  background: color-mix(in oklab, var(--surface) 82%, transparent);
  backdrop-filter: blur(20px) saturate(160%);
  -webkit-backdrop-filter: blur(20px) saturate(160%);
  transition:
    transform 200ms ease,
    box-shadow 200ms ease;
}
@media (hover: hover) {
  .stats > :hover {
    transform: translateY(-2px);
  }
}
.stat-skel {
  gap: 10px;
  padding: 20px;
}

/* ---- 横向「货架」：标题行 ---- */
.row {
  display: flex;
  flex-direction: column;
  gap: 14px;
  min-width: 0;
}
.row-head {
  display: flex;
  align-items: baseline;
  gap: 12px;
  min-width: 0;
}
.row-title {
  position: relative;
  margin: 0;
  padding-left: 12px;
  font-size: 18px;
  font-weight: 650;
  letter-spacing: -0.02em;
  color: var(--foreground);
  white-space: nowrap;
}
.row-title::before {
  content: '';
  position: absolute;
  left: 0;
  top: 5px;
  bottom: 5px;
  width: 4px;
  border-radius: 2px;
  background: var(--accent);
}
.row-hint {
  min-width: 0;
  font-size: 12.5px;
  color: var(--muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.row-more {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
  gap: 2px;
  flex-shrink: 0;
  font-size: 13px;
  color: var(--muted);
  text-decoration: none;
  transition: color 150ms ease;
}
.row-more:hover {
  color: var(--accent);
}

/* ---- 海报货架：一行横滑，左右边缘渐隐提示还能滑 ---- */
.shelf {
  display: grid;
  grid-auto-flow: column;
  grid-auto-columns: 150px;
  gap: 18px;
  overflow-x: auto;
  overscroll-behavior-x: contain;
  scroll-snap-type: x proximity;
  margin: -12px calc(-1 * var(--content-px, 28px)) -8px;
  padding: 12px var(--content-px, 28px) 8px;
  scroll-padding-inline: var(--content-px, 28px);
  scrollbar-width: none;
  mask-image: linear-gradient(
    to right,
    transparent 0,
    #000 var(--content-px, 28px),
    #000 calc(100% - var(--content-px, 28px) * 2),
    transparent 100%
  );
}
.shelf::-webkit-scrollbar {
  display: none;
}
.shelf-item {
  min-width: 0;
  scroll-snap-align: start;
}
.shelf-poster {
  border-radius: 14px;
  box-shadow:
    0 10px 24px -14px rgb(0 0 0 / 0.6),
    0 0 0 1px color-mix(in oklab, var(--foreground) 6%, transparent);
  transition:
    transform 260ms cubic-bezier(0.2, 0.8, 0.2, 1),
    box-shadow 260ms ease;
}
@media (hover: hover) {
  .shelf-item:hover .shelf-poster {
    transform: translateY(-6px) scale(1.03);
    box-shadow:
      0 22px 40px -18px rgb(0 0 0 / 0.75),
      0 0 0 2px var(--accent);
  }
}
.shelf-skel {
  aspect-ratio: 2 / 3;
  height: auto !important;
}
.shelf-title {
  margin-top: 10px;
  font-size: 13.5px;
  font-weight: 500;
  color: var(--foreground);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.shelf-sub {
  margin-top: 1px;
  font-size: 12px;
  color: var(--muted);
}

/* ---- 媒体库：四张海报并排成一条「胶片」，底部压暗写库名 ---- */
.libs {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
  gap: 18px;
}
.lib {
  position: relative;
  border-radius: 16px;
  overflow: hidden;
  background: var(--surface-secondary);
  box-shadow: 0 0 0 1px color-mix(in oklab, var(--foreground) 6%, transparent);
  transition:
    transform 260ms cubic-bezier(0.2, 0.8, 0.2, 1),
    box-shadow 260ms ease;
}
/* 四张竖版海报并排本来是 8:3 的扁条，叠上库名就挤；固定成 16:9，海报裁掉下半截（片名那一截） */
.lib-strip {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 2px;
  aspect-ratio: 16 / 9;
}
.lib-strip :deep(.pimg) {
  aspect-ratio: auto;
  height: 100%;
}
.lib-strip :deep(img) {
  object-position: center top;
}
/* 拼贴里每张小海报自带圆角，拼起来缝隙处会露出一圈锯齿，统一抹平 */
.lib-strip :deep(.pimg) {
  border-radius: 0;
}
.lib-strip :deep(img) {
  transition: transform 500ms cubic-bezier(0.2, 0.8, 0.2, 1);
}
@media (hover: hover) {
  .lib:hover {
    transform: translateY(-3px);
    box-shadow:
      0 18px 36px -18px rgb(0 0 0 / 0.6),
      0 0 0 2px var(--accent);
  }
  .lib:hover .lib-strip :deep(img) {
    transform: scale(1.06);
  }
}
.lib-shade {
  position: absolute;
  inset: auto 0 0;
  display: flex;
  align-items: flex-end;
  gap: 10px;
  padding: 48px 16px 14px;
  background: linear-gradient(to bottom, transparent, rgb(0 0 0 / 0.7) 45%, rgb(0 0 0 / 0.92));
}
.lib-text {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
}
.lib-name {
  font-size: 15px;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.lib-type {
  font-size: 11.5px;
  color: var(--muted);
}
.lib-count {
  flex-shrink: 0;
  font-size: 22px;
  font-weight: 700;
  letter-spacing: -0.03em;
  line-height: 1;
  font-variant-numeric: tabular-nums;
}

/* ---- 下半部：卡片栅格 ---- */
.grid-3 {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 16px;
}
.big-num {
  font-size: 30px;
  font-weight: 700;
  letter-spacing: -0.03em;
  line-height: 1.1;
  color: var(--foreground);
  font-variant-numeric: tabular-nums;
}
.big-sub {
  margin-top: 4px;
  font-size: 12.5px;
  color: var(--muted);
}
.mt {
  margin-top: 14px;
}
.meta {
  margin-top: 8px;
  font-size: 12px;
  color: var(--muted);
}
.load {
  display: flex;
  flex-direction: column;
  gap: 18px;
}
.load-head {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 8px;
  font-size: 13px;
  color: var(--muted);
}
.load-pct {
  margin-left: auto;
  font-weight: 600;
  color: var(--foreground);
  font-variant-numeric: tabular-nums;
}

/* ---- 最近整理：宽屏两列 ---- */
.recent {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 16px;
  margin: -6px -8px;
}
.recent-item {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
  padding: 8px;
  border-radius: 14px;
  transition: background-color 150ms ease;
}
@media (hover: hover) {
  .recent-item:hover {
    background: var(--surface-secondary);
  }
}
.recent-poster {
  width: 36px;
  flex-shrink: 0;
}
.recent-body {
  flex: 1;
  min-width: 0;
}
.recent-title {
  font-size: 13.5px;
  font-weight: 500;
  color: var(--foreground);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.recent-year {
  font-weight: 400;
  color: var(--muted);
}
.recent-cat {
  margin-top: 1px;
  font-size: 12px;
  color: var(--muted);
}
.recent-at {
  flex-shrink: 0;
  font-size: 12px;
  color: var(--muted);
  font-variant-numeric: tabular-nums;
}

/* ---- 断点 ---- */
@media (max-width: 1280px) {
  .grid-3 {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
@media (max-width: 1080px) {
  .stats {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .recent {
    grid-template-columns: 1fr;
  }
}
@media (max-width: 720px) {
  .dash {
    gap: 20px;
  }
  .stats {
    gap: 10px;
    margin-top: -72px;
  }
  .grid-3 {
    grid-template-columns: 1fr;
    gap: 12px;
  }
  .row-title {
    font-size: 16px;
  }
  .shelf {
    grid-auto-columns: 30%;
    gap: 12px;
  }
  /* 媒体库在手机上也横滑，省得一路往下排好几屏 */
  .libs {
    grid-auto-flow: column;
    grid-template-columns: none;
    grid-auto-columns: 78%;
    gap: 12px;
    overflow-x: auto;
    scroll-snap-type: x mandatory;
    margin-inline: calc(-1 * var(--content-px, 16px));
    padding-inline: var(--content-px, 16px);
    scroll-padding-inline: var(--content-px, 16px);
    scrollbar-width: none;
  }
  .libs::-webkit-scrollbar {
    display: none;
  }
  .lib {
    scroll-snap-align: start;
  }
  /* 横幅上的按钮只留图标，给来源胶囊让出宽度 */
  .btn-text,
  .src-label {
    display: none;
  }
  .dash :deep(.hero .button) {
    width: 32px;
    padding: 0;
  }
}
</style>
