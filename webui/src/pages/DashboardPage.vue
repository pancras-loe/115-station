<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { NAlert, NButton, NSkeleton, NTag, NTooltip } from 'naive-ui'
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
      <div class="bar-src">
        <span class="bar-label">媒体数量来源</span>
        <NTooltip>
          <template #trigger>
            <NTag size="small" :type="fromEmby ? 'success' : 'default'" :bordered="false">
              {{ fromEmby ? 'Emby 媒体库' : '本地整理台账' }}
            </NTag>
          </template>
          {{
            fromEmby
              ? 'Emby 已接入，电影/剧集数量与媒体库卡片都直接读 Emby，和 Emby 界面上的一致'
              : '未配置 Emby 或暂时不可达，只能按本地整理台账统计'
          }}
        </NTooltip>
      </div>
      <div class="bar-actions">
        <NButton size="small" quaternary :loading="refreshing" @click="load(true, true)">
          <template #icon><RefreshCw :size="15" /></template>
          刷新
        </NButton>
        <NButton size="small" quaternary @click="calibrating = true">
          <template #icon><SlidersHorizontal :size="15" /></template>
          校准台账
        </NButton>
      </div>
    </div>

    <!-- ==== 台账虚高提示 ==== -->
    <NAlert v-if="driftNotable" type="warning" :bordered="false">
      本地整理台账记着 {{ num(localTotal) }} 部，Emby 实际只有 {{ num(media?.total) }} 部，
      多出的 {{ num(drift) }} 部多半是手工删片、解除媒体库目录关联之后留下的幽灵记录。
      点「校准台账」按本地 STRM 目录核对一遍即可（只删台账行，不动网盘与 Emby）。
    </NAlert>

    <!-- ==== 指标行 ==== -->
    <div class="stats">
      <template v-if="loading">
        <NSkeleton v-for="i in 4" :key="i" height="74px" :sharp="false" />
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
      <div v-if="categories.length" class="cats">
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
            <span class="cat-name">{{ c.name }}</span>
            <span v-if="c.label" class="cat-type">{{ c.label }}</span>
            <NTag size="small" :bordered="false">{{ num(c.count) }}</NTag>
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
        <div v-if="wall.length" class="wall">
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

.bar {
  display: flex;
  align-items: center;
  gap: 12px;
}
.bar-src {
  display: flex;
  align-items: center;
  gap: 7px;
  min-width: 0;
}
.bar-label {
  font-size: 12.5px;
  color: var(--c-text-3);
}
.bar-actions {
  margin-left: auto;
  display: flex;
  gap: 4px;
}

.stats {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 14px;
}
.grid-3 {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 14px;
}
.grid-2 {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 14px;
}

.big-num {
  font-size: 27px;
  font-weight: 600;
  letter-spacing: -0.02em;
  line-height: 1.1;
  color: var(--c-text-1);
  font-variant-numeric: tabular-nums;
}
.big-sub {
  margin-top: 2px;
  font-size: 12.5px;
  color: var(--c-text-3);
}
.mt {
  margin-top: 12px;
}
.meta {
  margin-top: 8px;
  font-size: 11.5px;
  color: var(--c-text-3);
}

.load {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.load-head {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 7px;
  font-size: 12.5px;
  color: var(--c-text-2);
}
.load-pct {
  margin-left: auto;
  font-weight: 600;
  color: var(--c-text-1);
  font-variant-numeric: tabular-nums;
}

.cats {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
  gap: 14px;
}
.cat {
  border-radius: var(--radius);
}
.collage {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 2px;
  border-radius: var(--radius);
  overflow: hidden;
}
.cat-foot {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 8px;
}
.cat-name {
  flex: 1;
  min-width: 0;
  font-size: 13px;
  font-weight: 500;
  color: var(--c-text-1);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.cat-type {
  flex-shrink: 0;
  font-size: 11px;
  color: var(--c-text-3);
}

.wall {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(84px, 1fr));
  gap: 12px;
}
.wall-title {
  margin-top: 6px;
  font-size: 11.5px;
  color: var(--c-text-2);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.recent {
  display: flex;
  flex-direction: column;
}
.recent-item {
  display: flex;
  align-items: center;
  gap: 11px;
  padding: 9px 0;
  border-bottom: 1px solid var(--c-border);
}
.recent-item:last-child {
  border-bottom: none;
}
.recent-poster {
  width: 34px;
  flex-shrink: 0;
}
.recent-body {
  flex: 1;
  min-width: 0;
}
.recent-title {
  font-size: 13px;
  color: var(--c-text-1);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.recent-year {
  color: var(--c-text-3);
}
.recent-cat {
  margin-top: 1px;
  font-size: 11.5px;
  color: var(--c-text-3);
}
.recent-at {
  flex-shrink: 0;
  font-size: 11.5px;
  color: var(--c-text-3);
  font-variant-numeric: tabular-nums;
}

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
  .grid-3 {
    grid-template-columns: 1fr;
  }
}
</style>
