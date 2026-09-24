<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import {
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
import HChip from '@/components/hero/HChip.vue'
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

/** Emby 接上了就以 Emby 媒体库为准，否则退回本地整理台账的分类 */
const categories = computed(() => {
  const emby = data.value?.emby
  if (emby?.libraries?.length) {
    return emby.libraries.map((l) => ({
      name: l.name,
      count: l.count,
      label: l.type_label ?? '',
      posters: (l.collage ?? []).map((p) => embyImageUrl(p)),
    }))
  }
  return (data.value?.categories ?? []).map((c) => ({
    name: c.name,
    count: c.count,
    label: '',
    posters: (c.posters ?? []).map((p) => posterUrl(p) as string),
  }))
})

/** 海报墙同理：Emby 的最新入库优先，退回本地最近整理 */
const wall = computed(() => {
  const emby = data.value?.emby
  if (emby?.recent?.length) {
    return emby.recent.map((m) => ({
      key: m.id,
      title: m.name,
      sub: m.year,
      src: embyImageUrl('Items/' + m.id + '/Images/Primary'),
    }))
  }
  return (data.value?.recent_media ?? []).map((m, i) => ({
    key: m.title + '-' + i,
    title: m.title,
    sub: m.year,
    src: posterUrl(m.poster),
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
    <!-- ==== 数据来源与动作 ==== -->
    <div class="bar">
      <HTooltip
        :content="
          fromEmby
            ? 'Emby 已接入，电影/剧集数量与媒体库卡片都直接读 Emby，和 Emby 界面上的一致'
            : '未配置 Emby 或暂时不可达，只能按本地整理台账统计'
        "
        side="bottom"
      >
        <span class="bar-src" tabindex="0">
          <span class="bar-dot" :class="fromEmby ? 'is-emby' : 'is-local'" />
          <span class="bar-label">数量来自</span>
          <span class="bar-value">{{ fromEmby ? 'Emby 媒体库' : '本地整理台账' }}</span>
        </span>
      </HTooltip>
      <div class="bar-actions">
        <HButton size="sm" variant="tertiary" :loading="refreshing" aria-label="刷新" @click="load(true, true)">
          <template #icon><RefreshCw /></template>
          <span class="bar-btn-text">刷新</span>
        </HButton>
        <HButton size="sm" variant="tertiary" aria-label="校准台账" @click="calibrating = true">
          <template #icon><SlidersHorizontal /></template>
          <span class="bar-btn-text">校准台账</span>
        </HButton>
      </div>
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

    <!-- ==== 指标行 ==== -->
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

    <!-- ==== 我的媒体库 ==== -->
    <SectionCard
      title="我的媒体库"
      :hint="fromEmby ? '直接读 Emby 媒体库，按库类型计数（一部影视算一条）' : '按本地整理台账的分类聚合'"
    >
      <!-- 手机上一行横滑，而不是折成两列一路往下排：媒体库多的时候能少滚好几屏 -->
      <div v-if="categories.length" class="cats rail">
        <div
          v-for="c in categories"
          :key="c.name"
          class="cat"
          :title="c.name + ' · ' + c.count + ' 部'"
        >
          <div class="collage">
            <PosterImage v-for="i in 4" :key="i" :src="c.posters[i - 1]" :alt="c.name" />
          </div>
          <div class="cat-foot">
            <div class="cat-text">
              <span class="cat-name">{{ c.name }}</span>
              <span v-if="c.label" class="cat-type">{{ c.label }}</span>
            </div>
            <HChip size="sm">{{ num(c.count) }}</HChip>
          </div>
        </div>
      </div>
      <EmptyState v-else text="暂无入库记录 · 整理或同步后这里会显示分类卡片" />
    </SectionCard>

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

    <!-- ==== 海报墙 + 最近整理 ==== -->
    <div class="grid-2">
      <SectionCard title="最新入库" :hint="fromEmby ? '来自 Emby' : '来自本地整理记录'">
        <div v-if="wall.length" class="wall rail">
          <div
            v-for="m in wall"
            :key="m.key"
            class="wall-item"
            :title="m.title + ' ' + (m.sub || '')"
          >
            <PosterImage :src="m.src" :alt="m.title" />
            <div class="wall-title">{{ m.title }}</div>
          </div>
        </div>
        <EmptyState v-else text="暂无入库" />
      </SectionCard>

      <SectionCard title="最近整理">
        <div v-if="recent.length" class="recent">
          <div v-for="(m, i) in recent" :key="m.title + '-' + i" class="recent-item">
            <PosterImage class="recent-poster" :src="posterUrl(m.poster)" :alt="m.title" />
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
    </div>

    <CalibrateModal v-model:show="calibrating" @done="load(true, true)" />
  </div>
</template>

<style scoped>
.dash {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

/* ---- 来源与动作 ---- */
.bar {
  display: flex;
  align-items: center;
  gap: 12px;
}
.bar-src {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  min-width: 0;
  height: 32px;
  padding: 0 12px;
  border-radius: 999px;
  background: var(--surface);
  box-shadow: var(--surface-shadow);
  font-size: 12.5px;
  outline: none;
}
.bar-src:focus-visible {
  box-shadow: 0 0 0 2px var(--focus);
}
.bar-dot {
  width: 7px;
  height: 7px;
  border-radius: 999px;
  flex-shrink: 0;
}
.bar-dot.is-emby {
  background: var(--success);
  box-shadow: 0 0 0 3px var(--success-soft);
}
.bar-dot.is-local {
  background: var(--muted);
}
.bar-label {
  color: var(--muted);
}
.bar-value {
  font-weight: 500;
  color: var(--foreground);
  white-space: nowrap;
}
.bar-actions {
  margin-left: auto;
  display: flex;
  gap: 6px;
}

/* ---- 栅格 ---- */
.stats {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 16px;
}
.stat-skel {
  gap: 10px;
  padding: 20px;
}
.grid-3 {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 16px;
}
.grid-2 {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
}

/* ---- 存储 / 负载 ---- */
.big-num {
  font-size: 30px;
  font-weight: 600;
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

/* ---- 媒体库卡片 ---- */
.cats {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: 16px;
}
.collage {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 2px;
  border-radius: 16px;
  overflow: hidden;
  background: var(--surface-secondary);
}
/* 拼贴里每张小海报自带圆角，拼起来缝隙处会露出一圈锯齿，统一抹平 */
.collage :deep(*) {
  border-radius: 0;
}
.cat-foot {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 10px;
}
.cat-text {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
}
.cat-name {
  font-size: 13.5px;
  font-weight: 500;
  color: var(--foreground);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.cat-type {
  font-size: 11.5px;
  color: var(--muted);
}

/* ---- 海报墙 ---- */
.wall {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(88px, 1fr));
  gap: 14px 12px;
}
.wall-item > :first-child {
  border-radius: 12px;
}
.wall-title {
  margin-top: 6px;
  font-size: 12px;
  color: var(--foreground);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* ---- 最近整理 ---- */
.recent {
  display: flex;
  flex-direction: column;
  margin: -6px -8px;
}
.recent-item {
  display: flex;
  align-items: center;
  gap: 12px;
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
  .grid-2 {
    grid-template-columns: 1fr;
  }
}
@media (max-width: 720px) {
  .dash {
    gap: 12px;
  }
  .stats {
    gap: 12px;
  }
  .grid-3 {
    grid-template-columns: 1fr;
    gap: 12px;
  }
  .grid-2 {
    gap: 12px;
  }
  /* 按钮只留图标，给来源胶囊让出宽度 */
  .bar-btn-text {
    display: none;
  }
  .bar-actions :deep(.button) {
    width: 36px;
    padding: 0;
  }
  .bar-label {
    display: none;
  }

  /* 横滑轨道：左右出血到卡片边缘，滑动时内容从边缘露出来，暗示还能再滑 */
  .rail {
    display: grid;
    grid-auto-flow: column;
    grid-template-columns: none;
    overflow-x: auto;
    overscroll-behavior-x: contain;
    scroll-snap-type: x mandatory;
    scroll-padding-inline: 16px;
    margin-inline: -16px;
    padding-inline: 16px;
    scrollbar-width: none;
  }
  .rail::-webkit-scrollbar {
    display: none;
  }
  .rail > * {
    scroll-snap-align: start;
  }
  .cats.rail {
    grid-auto-columns: 42%;
    gap: 12px;
  }
  .wall.rail {
    grid-auto-columns: 28%;
    gap: 10px;
  }
}
</style>
