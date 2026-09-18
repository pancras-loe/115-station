<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { NSkeleton, NTag } from 'naive-ui'
import {
  Clapperboard,
  Cpu,
  FileVideo,
  MemoryStick,
  RefreshCw,
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
import { bytes, num, percent } from '@/utils/format'
import { embyImageUrl, posterUrl } from '@/utils/media'
import { toastError } from '@/composables/useFeedback'

const data = ref<Dashboard | null>(null)
const loading = ref(true)

async function load(manual = false) {
  try {
    data.value = await dashboardApi.get()
  } catch (e) {
    // 轮询失败静默：30 秒一次的后台刷新不该把错误提示刷满屏
    if (manual) toastError(e, '仪表盘加载失败')
  } finally {
    loading.value = false
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

/** Emby 接上了就以 Emby 媒体库为准，否则退回本地整理台账的分类 */
const categories = computed(() => {
  const emby = data.value?.emby
  if (emby?.libraries?.length) {
    return emby.libraries.map((l) => ({
      name: l.name,
      count: l.count,
      posters: (l.collage ?? []).map((p) => embyImageUrl(p)),
    }))
  }
  return (data.value?.categories ?? []).map((c) => ({
    name: c.name,
    count: c.count,
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
</script>

<template>
  <div class="dash">
    <!-- ==== 指标行 ==== -->
    <div class="stats">
      <template v-if="loading">
        <NSkeleton v-for="i in 4" :key="i" height="74px" :sharp="false" />
      </template>
      <template v-else>
        <StatCard
          label="电影"
          :value="num(data?.media.movies)"
          :sub="'本月新增 ' + (data?.media.movies_month ?? 0)"
          :icon="Clapperboard"
          tone="primary"
        />
        <StatCard
          label="剧集"
          :value="num(data?.media.tvs)"
          :sub="'本月新增 ' + (data?.media.tvs_month ?? 0)"
          :icon="Tv"
          tone="success"
        />
        <StatCard
          label="STRM 文件"
          :value="num(data?.strm.total)"
          :sub="'失效 ' + (data?.strm.invalid ?? 0) + ' · 同步台账 ' + num(data?.synced_files)"
          :icon="FileVideo"
          :tone="(data?.strm.invalid ?? 0) > 0 ? 'warning' : 'primary'"
        />
        <StatCard
          label="已整理入库"
          :value="num(data?.organized)"
          :sub="'待处理事件 ' + (data?.pending_events ?? 0)"
          :icon="RefreshCw"
          tone="primary"
        />
      </template>
    </div>

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

      <SectionCard title="近 7 天入库" :hint="'共 ' + (data?.week_total ?? 0) + ' 部'">
        <WeeklyChart :data="data?.weekly ?? []" />
      </SectionCard>
    </div>

    <!-- ==== 我的媒体库 ==== -->
    <SectionCard title="我的媒体库">
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
            <NTag size="small" :bordered="false">{{ num(c.count) }}</NTag>
          </div>
        </div>
      </div>
      <EmptyState v-else text="暂无入库记录 · 整理或同步后这里会显示分类卡片" />
    </SectionCard>

    <!-- ==== 海报墙 + 最近整理 ==== -->
    <div class="grid-2">
      <SectionCard title="最新入库">
        <div v-if="wall.length" class="wall">
          <div v-for="m in wall" :key="m.key" class="wall-item" :title="m.title + ' ' + (m.sub || '')">
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
  </div>
</template>

<style scoped>
.dash {
  display: flex;
  flex-direction: column;
  gap: 16px;
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
