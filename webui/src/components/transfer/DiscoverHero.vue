<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { BellPlus, BellRing, Check, ChevronLeft, ChevronRight, Search, Star } from '@lucide/vue'
import HButton from '@/components/hero/HButton.vue'
import MediaTypeChip from '@/components/MediaTypeChip.vue'
import PosterImage from '@/components/PosterImage.vue'
import { resourcesApi } from '@/api'
import type { TmdbCandidate } from '@/api/resources'
import type { OwnedInfo } from '@/api/transfer'
import { useSubscriptions } from '@/composables/subscriptions'

/**
 * 发现页顶部的聚光灯横幅：轮播今日趋势里有背景图的前几部，搜索框也放在这里（Jellyseerr 首页同款），
 * 一进页面先看到的是「现在大家在看什么」，而不是一个空输入框。
 * 外观与总览的影院横幅同一套（暗底、浅色字 .on-dark、背景缓慢推近），但不出血到顶栏：这里在页签里。
 */
const props = defineProps<{
  items: TmdbCandidate[]
  owned: Record<string, OwnedInfo>
  loading?: boolean
}>()
const emit = defineEmits<{ pick: [TmdbCandidate] }>()

const idx = ref(0)
const current = computed(() => props.items[idx.value])
watch(
  () => props.items.length,
  (n) => {
    if (idx.value >= n) idx.value = 0
  },
)
// 背景图只给轮到过的和下一张挂 <img>：1280px 的背景图一张几百 KB，一次全拉白费
const seen = ref(new Set<number>([0, 1]))
watch(idx, (i) => {
  seen.value.add(i)
  seen.value.add((i + 1) % Math.max(1, props.items.length))
})

const INTERVAL = 8000
const hovering = ref(false)
const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches
let timer: number | undefined
function restart() {
  clearInterval(timer)
  if (reduced) return
  timer = window.setInterval(() => {
    if (hovering.value || document.hidden || props.items.length < 2) return
    idx.value = (idx.value + 1) % props.items.length
  }, INTERVAL)
}
onMounted(restart)
onUnmounted(() => clearInterval(timer))
function go(d: number) {
  const n = props.items.length
  if (n < 2) return
  idx.value = (idx.value + d + n) % n
  restart()
}
function pickDot(i: number) {
  idx.value = i
  restart()
}
let touchX = 0
function onTouchStart(e: TouchEvent) {
  touchX = e.touches[0].clientX
}
function onTouchEnd(e: TouchEvent) {
  const dx = e.changedTouches[0].clientX - touchX
  if (Math.abs(dx) > 48) go(dx < 0 ? 1 : -1)
}

const subs = useSubscriptions()
const sub = computed(() => (current.value ? subs.findSub(current.value.id, current.value.media_type) : null))
const subBusy = computed(() =>
  current.value ? subs.quickBusy.value.has(`${current.value.media_type}:${current.value.id}`) : false,
)
function onSub() {
  const c = current.value
  if (!c) return
  if (sub.value) subs.openDetail(sub.value.id)
  else void subs.quickSubscribe(c.id, c.media_type)
}
const isOwned = computed(() => (current.value ? !!props.owned[`${current.value.media_type}:${current.value.id}`] : false))
const img = (p: string | undefined, size: string) => (p ? resourcesApi.tmdbImageUrl(p, size) : null)
</script>

<template>
  <section
    class="spot on-dark"
    aria-roledescription="carousel"
    aria-label="今日趋势"
    @mouseenter="hovering = true"
    @mouseleave="hovering = false"
    @touchstart.passive="onTouchStart"
    @touchend="onTouchEnd"
  >
    <div class="spot-bg" aria-hidden="true">
      <div v-for="(it, i) in items" :key="`${it.media_type}-${it.id}`" class="bg" :class="{ on: i === idx }">
        <img v-if="seen.has(i) && it.backdrop" :src="img(it.backdrop, 'w1280')!" alt="" decoding="async" />
      </div>
      <div class="glow" />
    </div>
    <div class="scrim" aria-hidden="true" />

    <div class="spot-inner">
      <div class="spot-search"><slot name="search" /></div>

      <div class="spot-body">
        <div v-if="loading && !items.length" class="spot-text">
          <div class="sk sk-eyebrow" />
          <div class="sk sk-title" />
          <div class="sk sk-line" />
          <div class="sk sk-line short" />
        </div>
        <div v-else-if="!items.length" class="spot-text">
          <div class="eyebrow"><span class="dot-live" />发现</div>
          <h2 class="title">今天想看点什么？</h2>
          <p class="overview">搜一部片，或者往下逛逛榜单。选定之后各资源站一起搜，找到就转存入库。</p>
        </div>
        <Transition v-else name="spot-text" mode="out-in">
          <div :key="`${current?.media_type}-${current?.id}`" class="spot-text" aria-live="polite">
            <div class="eyebrow"><span class="dot-live" />今日趋势 · No.{{ idx + 1 }}</div>
            <h2 class="title">{{ current?.title }}</h2>
            <div class="meta">
              <MediaTypeChip v-if="current" :type="current.media_type" />
              <span v-if="current?.year">{{ current.year }}</span>
              <span v-if="current?.vote" class="rating">
                <Star :size="13" :stroke-width="0" fill="currentColor" />{{ current.vote.toFixed(1) }}
              </span>
              <span v-if="isOwned" class="owned"><Check :size="12" :stroke-width="3" />已入库</span>
            </div>
            <p v-if="current?.overview" class="overview">{{ current.overview }}</p>
            <div class="actions">
              <HButton variant="primary" @click="current && emit('pick', current)">
                <template #icon><Search :size="15" /></template>
                找资源
              </HButton>
              <HButton variant="tertiary" :loading="subBusy" @click="onSub">
                <template #icon>
                  <BellRing v-if="sub" :size="15" />
                  <BellPlus v-else :size="15" />
                </template>
                {{ sub ? '已订阅' : '订阅' }}
              </HButton>
            </div>
          </div>
        </Transition>

        <Transition name="spot-poster" mode="out-in">
          <button
            v-if="current"
            :key="`${current.media_type}-${current.id}`"
            type="button"
            class="poster-btn"
            :aria-label="`${current.title}，去找资源`"
            @click="emit('pick', current)"
          >
            <PosterImage class="poster" :src="img(current.poster, 'w342')" :alt="current.title" :icon-size="28" />
          </button>
        </Transition>
      </div>

      <div v-if="items.length > 1" class="spot-foot">
        <div class="dots" role="tablist">
          <button
            v-for="(it, i) in items"
            :key="`${it.media_type}-${it.id}`"
            type="button"
            role="tab"
            class="dot"
            :class="{ on: i === idx, paused: hovering }"
            :aria-selected="i === idx"
            :aria-label="it.title"
            @click="pickDot(i)"
          >
            <span class="dot-fill" :style="{ animationDuration: INTERVAL + 'ms' }" />
          </button>
        </div>
        <div class="arrows">
          <button type="button" class="arrow" aria-label="上一部" @click="go(-1)"><ChevronLeft :size="17" /></button>
          <button type="button" class="arrow" aria-label="下一部" @click="go(1)"><ChevronRight :size="17" /></button>
        </div>
      </div>
    </div>
  </section>
</template>

<style scoped>
.spot {
  position: relative;
  isolation: isolate;
  overflow: hidden;
  border-radius: var(--r-card);
  min-height: 420px;
  background: oklch(0.2 0.03 50);
  box-shadow: 0 30px 60px -36px rgb(0 0 0 / 0.7);
}
.spot-bg {
  position: absolute;
  inset: 0;
  z-index: -2;
}
.bg {
  position: absolute;
  inset: 0;
  opacity: 0;
  transition: opacity 900ms ease;
}
.bg.on {
  opacity: 1;
}
.bg img {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  object-fit: cover;
  object-position: center 25%;
}
.bg.on img {
  animation: zoom 14s ease-out both;
}
@keyframes zoom {
  from {
    transform: scale(1.08);
  }
  to {
    transform: scale(1);
  }
}
/* 放映机光束：图没到、或者榜单是空的时候，横幅也不是一块死色 */
.glow {
  position: absolute;
  inset: 0;
  background:
    radial-gradient(55% 80% at 85% 0%, color-mix(in oklab, var(--accent) 40%, transparent), transparent 70%),
    radial-gradient(50% 60% at 0% 100%, oklch(0.4 0.1 30 / 0.5), transparent 70%);
  mix-blend-mode: screen;
  opacity: 0.55;
}
.scrim {
  position: absolute;
  inset: 0;
  z-index: -1;
  pointer-events: none;
  background:
    linear-gradient(to bottom, rgb(0 0 0 / 0.55) 0%, rgb(0 0 0 / 0) 32%),
    linear-gradient(to right, rgb(0 0 0 / 0.85) 0%, rgb(0 0 0 / 0.5) 45%, rgb(0 0 0 / 0.08) 80%),
    linear-gradient(to top, rgb(0 0 0 / 0.65) 0%, rgb(0 0 0 / 0) 40%);
}

.spot-inner {
  display: flex;
  flex-direction: column;
  gap: 18px;
  min-height: 420px;
  box-sizing: border-box;
  padding: 22px 28px 20px;
}
.spot-search {
  max-width: 640px;
}
.spot-body {
  flex: 1;
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 32px;
}
.spot-text {
  flex: 1;
  min-width: 0;
  max-width: 620px;
}
.eyebrow {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 12.5px;
  font-weight: 600;
  letter-spacing: 0.08em;
  color: color-mix(in oklab, var(--accent) 70%, white);
}
.dot-live {
  width: 7px;
  height: 7px;
  border-radius: 99px;
  background: var(--accent);
  box-shadow: 0 0 0 4px color-mix(in oklab, var(--accent) 30%, transparent);
  animation: live 2.4s ease-in-out infinite;
}
@keyframes live {
  50% {
    box-shadow: 0 0 0 7px color-mix(in oklab, var(--accent) 0%, transparent);
  }
}
.title {
  margin: 10px 0 0;
  font-size: clamp(26px, 3.4vw, 42px);
  font-weight: 800;
  line-height: 1.12;
  letter-spacing: -0.03em;
  text-shadow: 0 2px 24px rgb(0 0 0 / 0.5);
  display: -webkit-box;
  -webkit-line-clamp: 2;
  line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 12px;
  margin-top: 12px;
  font-size: 13.5px;
  color: var(--muted);
}
.rating {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-weight: 700;
  color: oklch(0.86 0.15 85);
}
.owned {
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
.overview {
  margin: 12px 0 0;
  font-size: 13.5px;
  line-height: 1.7;
  color: rgb(255 255 255 / 0.78);
  display: -webkit-box;
  -webkit-line-clamp: 3;
  line-clamp: 3;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  margin-top: 18px;
}
.actions .button--tertiary {
  backdrop-filter: blur(10px);
  -webkit-backdrop-filter: blur(10px);
}
.poster-btn {
  all: unset;
  flex: none;
  cursor: pointer;
  border-radius: 14px;
}
.poster-btn:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 3px;
}
.poster {
  width: 168px;
  border-radius: 14px;
  box-shadow:
    0 30px 60px -20px rgb(0 0 0 / 0.85),
    0 0 0 1px rgb(255 255 255 / 0.12);
  transform: rotate(2deg);
  transition: transform 0.35s cubic-bezier(0.2, 0.8, 0.2, 1);
}
.poster-btn:hover .poster {
  transform: rotate(0deg) scale(1.03);
}

.spot-foot {
  display: flex;
  align-items: center;
  gap: 14px;
}
.dots {
  display: flex;
  gap: 6px;
}
.dot {
  position: relative;
  width: 20px;
  height: 4px;
  padding: 0;
  border: 0;
  border-radius: 99px;
  background: rgb(255 255 255 / 0.25);
  overflow: hidden;
  cursor: pointer;
  transition: width 300ms ease;
}
.dot::after {
  content: '';
  position: absolute;
  inset: -10px -2px;
}
.dot.on {
  width: 40px;
}
.dot-fill {
  position: absolute;
  inset: 0;
  transform-origin: left;
  transform: scaleX(0);
  background: var(--accent);
}
.dot.on .dot-fill {
  animation: progress linear forwards;
}
.dot.paused .dot-fill {
  animation-play-state: paused;
}
@keyframes progress {
  to {
    transform: scaleX(1);
  }
}
.arrows {
  display: flex;
  gap: 6px;
  margin-left: auto;
}
.arrow {
  width: 30px;
  height: 30px;
  display: grid;
  place-items: center;
  border: 1px solid rgb(255 255 255 / 0.18);
  border-radius: 99px;
  background: rgb(0 0 0 / 0.25);
  color: var(--foreground);
  cursor: pointer;
  backdrop-filter: blur(8px);
  transition: background-color 150ms ease;
}
.arrow:hover {
  background: rgb(255 255 255 / 0.16);
}

.sk {
  border-radius: 8px;
  background: linear-gradient(100deg, rgb(255 255 255 / 0.08) 30%, rgb(255 255 255 / 0.16) 50%, rgb(255 255 255 / 0.08) 70%);
  background-size: 200% 100%;
  animation: sk 1.4s ease-in-out infinite;
}
.sk-eyebrow {
  width: 140px;
  height: 12px;
}
.sk-title {
  width: 60%;
  height: 38px;
  margin-top: 14px;
}
.sk-line {
  width: 90%;
  height: 12px;
  margin-top: 16px;
}
.sk-line.short {
  width: 55%;
  margin-top: 10px;
}
@keyframes sk {
  from {
    background-position: 150% 0;
  }
  to {
    background-position: -50% 0;
  }
}

.spot-text-enter-active {
  transition:
    opacity 500ms ease 150ms,
    transform 500ms cubic-bezier(0.2, 0.8, 0.2, 1) 150ms;
}
.spot-text-leave-active {
  transition: opacity 200ms ease;
}
.spot-text-enter-from {
  opacity: 0;
  transform: translateY(12px);
}
.spot-text-leave-to {
  opacity: 0;
}
.spot-poster-enter-active {
  transition:
    opacity 500ms ease 200ms,
    transform 600ms cubic-bezier(0.2, 0.8, 0.2, 1) 200ms;
}
.spot-poster-leave-active {
  transition: opacity 200ms ease;
}
.spot-poster-enter-from {
  opacity: 0;
  transform: translateY(16px) scale(0.96);
}
.spot-poster-leave-to {
  opacity: 0;
}
@media (prefers-reduced-motion: reduce) {
  .bg.on img,
  .dot-live,
  .sk {
    animation: none;
  }
}
@media (max-width: 720px) {
  .spot,
  .spot-inner {
    min-height: 380px;
  }
  .spot {
    border-radius: var(--r-xl);
  }
  .spot-inner {
    padding: 14px 16px 16px;
  }
  .poster-btn,
  .arrows {
    display: none;
  }
  .overview {
    -webkit-line-clamp: 2;
    line-clamp: 2;
    font-size: 13px;
  }
}
</style>
