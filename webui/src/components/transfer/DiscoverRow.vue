<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { ChevronLeft, ChevronRight } from '@lucide/vue'
import PosterCard from '@/components/transfer/PosterCard.vue'
import { resourcesApi, transferApi } from '@/api'
import type { DiscoverList, TmdbCandidate } from '@/api/resources'
import type { OwnedInfo } from '@/api/transfer'

/**
 * 发现页的一行榜单：横向滑动的海报带（Jellyseerr / MoviePilot 首页同款），只读第一页。
 * 滚到眼前才请求（IntersectionObserver）：九个榜单一进页面全拉，最下面几行多数人根本看不到。
 * 「查看全部」交给上层换成整页海报墙，那边能翻页、能按条件筛选。
 */
const props = defineProps<{
  list: DiscoverList
  /** 趋势类榜单给前十名标名次 */
  ranked?: boolean
}>()
const emit = defineEmits<{ pick: [TmdbCandidate]; more: [string] }>()

const items = ref<TmdbCandidate[]>([])
const owned = ref<Record<string, OwnedInfo>>({})
const state = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const err = ref('')

async function load() {
  state.value = 'loading'
  try {
    const d = await resourcesApi.tmdbDiscover(props.list.key, 1)
    items.value = d.data ?? []
    state.value = 'ready'
    if (items.value.length) {
      transferApi
        .owned(items.value.map((c) => `${c.media_type}:${c.id}`))
        .then((r) => (owned.value = r.data ?? {}))
        .catch(() => {})
    }
  } catch (e) {
    err.value = e instanceof Error ? e.message : '榜单读取失败'
    state.value = 'error'
  }
}

const root = ref<HTMLElement | null>(null)
const track = ref<HTMLElement | null>(null)
let io: IntersectionObserver | undefined
onMounted(() => {
  if (!('IntersectionObserver' in window)) {
    void load()
    return
  }
  io = new IntersectionObserver(
    (es) => {
      if (es.some((e) => e.isIntersecting) && state.value === 'idle') {
        io?.disconnect()
        void load()
      }
    },
    { rootMargin: '300px 0px' },
  )
  if (root.value) io.observe(root.value)
})
onBeforeUnmount(() => io?.disconnect())

// ---- 左右翻：一次翻大半屏，箭头到头了就灰掉 ----
const atStart = ref(true)
const atEnd = ref(false)
function onScroll() {
  const el = track.value
  if (!el) return
  atStart.value = el.scrollLeft < 8
  atEnd.value = el.scrollLeft + el.clientWidth >= el.scrollWidth - 8
}
function page(dir: 1 | -1) {
  const el = track.value
  if (!el) return
  el.scrollBy({ left: dir * el.clientWidth * 0.85, behavior: 'smooth' })
}
</script>

<template>
  <section ref="root" class="row">
    <header class="row-head">
      <h3 class="row-title">{{ list.label }}</h3>
      <button type="button" class="row-more" @click="emit('more', list.key)">
        查看全部<ChevronRight :size="14" />
      </button>
      <div v-if="state === 'ready' && items.length" class="row-arrows">
        <button type="button" class="arrow" :disabled="atStart" aria-label="往前翻" @click="page(-1)">
          <ChevronLeft :size="16" />
        </button>
        <button type="button" class="arrow" :disabled="atEnd" aria-label="往后翻" @click="page(1)">
          <ChevronRight :size="16" />
        </button>
      </div>
    </header>
    <p v-if="state === 'error'" class="row-state">{{ err }}</p>
    <p v-else-if="state === 'ready' && !items.length" class="row-state">这个榜单暂时是空的</p>
    <div v-else class="row-viewport">
      <div ref="track" class="row-track" @scroll.passive="onScroll">
        <template v-if="state === 'ready'">
          <PosterCard
            v-for="(it, i) in items"
            :key="`${it.media_type}-${it.id}`"
            class="row-item"
            :item="it"
            :owned="owned[`${it.media_type}:${it.id}`]"
            :rank="ranked ? i + 1 : undefined"
            @pick="emit('pick', it)"
          />
        </template>
        <template v-else>
          <div v-for="i in 8" :key="i" class="row-item sk" />
        </template>
      </div>
    </div>
  </section>
</template>

<style scoped>
.row {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-width: 0;
}
.row-head {
  display: flex;
  align-items: center;
  gap: 12px;
}
.row-title {
  margin: 0;
  font-size: 17px;
  font-weight: 700;
  letter-spacing: -0.01em;
  color: var(--foreground);
}
/* 标题前一道强调色竖条：榜单一行行往下排时，眼睛能抓到每行的开头 */
.row-title::before {
  content: '';
  display: inline-block;
  width: 4px;
  height: 0.9em;
  margin-right: 9px;
  border-radius: 99px;
  vertical-align: -0.08em;
  background: linear-gradient(to bottom, var(--accent), color-mix(in oklab, var(--accent) 40%, transparent));
}
.row-more {
  all: unset;
  display: inline-flex;
  align-items: center;
  gap: 1px;
  font-size: 12.5px;
  color: var(--muted);
  cursor: pointer;
}
.row-more:hover {
  color: var(--accent);
}
.row-more:focus-visible {
  outline: 2px solid var(--focus);
}
.row-arrows {
  display: flex;
  gap: 6px;
  margin-left: auto;
}
.arrow {
  all: unset;
  display: grid;
  place-items: center;
  width: 28px;
  height: 28px;
  border-radius: 999px;
  background: var(--default);
  color: var(--foreground);
  cursor: pointer;
  transition:
    background-color 0.15s,
    opacity 0.15s;
}
.arrow:hover:not(:disabled) {
  background: var(--accent);
  color: var(--accent-foreground);
}
.arrow:disabled {
  opacity: 0.35;
  cursor: default;
}
.arrow:focus-visible {
  outline: 2px solid var(--focus);
}
.row-state {
  margin: 0;
  padding: 28px 0;
  text-align: center;
  font-size: 13px;
  color: var(--muted);
}
/* 左右各留一点出血给海报浮起时的光晕，不然悬停的阴影被裁成一刀切 */
.row-viewport {
  margin: -14px -10px -6px;
  min-width: 0;
}
.row-track {
  display: grid;
  grid-auto-flow: column;
  grid-auto-columns: 150px;
  gap: 16px;
  padding: 14px 10px 6px;
  overflow-x: auto;
  overscroll-behavior-x: contain;
  scroll-snap-type: x proximity;
  scroll-padding: 0 10px;
  scrollbar-width: none;
}
.row-track::-webkit-scrollbar {
  display: none;
}
.row-item {
  scroll-snap-align: start;
}
.sk {
  aspect-ratio: 2 / 3;
  border-radius: var(--r-lg);
  background: linear-gradient(
    100deg,
    color-mix(in oklab, var(--muted) 10%, transparent) 30%,
    color-mix(in oklab, var(--muted) 18%, transparent) 50%,
    color-mix(in oklab, var(--muted) 10%, transparent) 70%
  );
  background-size: 200% 100%;
  animation: sk 1.4s ease-in-out infinite;
}
@keyframes sk {
  from {
    background-position: 150% 0;
  }
  to {
    background-position: -50% 0;
  }
}
@media (prefers-reduced-motion: reduce) {
  .sk {
    animation: none;
  }
}
@media (max-width: 720px) {
  .row-track {
    grid-auto-columns: 112px;
    gap: 10px;
  }
  .row-arrows {
    display: none;
  }
  .row-title {
    font-size: 15.5px;
  }
}
</style>
