<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { ChevronLeft, ChevronRight, Clapperboard, Star } from '@lucide/vue'
import PosterImage from '@/components/PosterImage.vue'

/**
 * 总览顶部的「影院横幅」：轮播最新入库的几部，背景是 Emby 的背景图（没有就拿海报放大虚化垫底）。
 *
 * 横幅在亮暗两种主题下都是暗的（和 Emby / Infuse 的首页一样），所以里面的字统一浅色（.on-dark），
 * 只有最底下一截渐变到页面底色，好让下面浮上来的指标卡片接得住。
 */
export interface HeroSlide {
  key: string
  title: string
  year?: string
  /** 电影 / 剧集 */
  kind?: string
  overview?: string
  rating?: number
  genres?: string[]
  /** 分级（PG-13 之类），可空 */
  badge?: string
  /** 「3 小时前入库」之类，已格式化好 */
  when?: string
  backdrop?: string | null
  poster?: string | null
  logo?: string | null
}

const props = defineProps<{ slides: HeroSlide[]; loading?: boolean }>()

const idx = ref(0)
const current = computed(() => props.slides[idx.value])
// 数据 30 秒刷新一次，条数可能变少：别让下标越界
watch(
  () => props.slides.length,
  (n) => {
    if (idx.value >= n) idx.value = 0
  },
)

// 背景图只在轮到过的那几张上挂 <img>：6 张 1600px 的背景图一次全拉是好几 MB，
// 大多数人看两眼就往下翻了
const seen = ref(new Set<number>([0]))
watch(idx, (i) => {
  seen.value.add(i)
  seen.value.add((i + 1) % Math.max(1, props.slides.length))
})

function go(delta: number) {
  const n = props.slides.length
  if (n < 2) return
  idx.value = (idx.value + delta + n) % n
  restart()
}
function pick(i: number) {
  idx.value = i
  restart()
}

// ---- 自动轮播：悬停 / 页面在后台 / 系统要求减少动效时都停 ----
const INTERVAL = 9000
const hovering = ref(false)
const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches
let timer: number | undefined
function restart() {
  clearInterval(timer)
  if (reduced) return
  timer = window.setInterval(() => {
    if (hovering.value || document.hidden || props.slides.length < 2) return
    idx.value = (idx.value + 1) % props.slides.length
  }, INTERVAL)
}
onMounted(restart)
onUnmounted(() => clearInterval(timer))

// 手机上左右滑动切换
let touchX = 0
function onTouchStart(e: TouchEvent) {
  touchX = e.touches[0].clientX
}
function onTouchEnd(e: TouchEvent) {
  const dx = e.changedTouches[0].clientX - touchX
  if (Math.abs(dx) > 48) go(dx < 0 ? 1 : -1)
}

const logoFailed = ref(new Set<string>())
</script>

<template>
  <section
    class="hero on-dark"
    :class="{ 'is-empty': !loading && !slides.length }"
    aria-roledescription="carousel"
    aria-label="最新入库"
    @mouseenter="hovering = true"
    @mouseleave="hovering = false"
    @touchstart.passive="onTouchStart"
    @touchend="onTouchEnd"
  >
    <!-- ==== 背景层：每张一层，叠在一起交叉淡入 ==== -->
    <div class="hero-bg" aria-hidden="true">
      <div
        v-for="(s, i) in slides"
        :key="s.key"
        class="bg-slide"
        :class="{ 'is-active': i === idx }"
      >
        <template v-if="seen.has(i)">
          <img v-if="s.poster" class="bg-blur" :src="s.poster" alt="" decoding="async" />
          <img
            v-if="s.backdrop"
            class="bg-img"
            :src="s.backdrop"
            alt=""
            decoding="async"
            @error="($event.target as HTMLImageElement).style.display = 'none'"
          />
        </template>
      </div>
      <div class="bg-glow" />
    </div>
    <div class="hero-scrim" aria-hidden="true" />

    <!-- ==== 前景 ==== -->
    <div class="hero-inner">
      <div v-if="loading && !slides.length" class="hero-text">
        <div class="sk sk-eyebrow" />
        <div class="sk sk-title" />
        <div class="sk sk-line" />
        <div class="sk sk-line short" />
      </div>

      <div v-else-if="!slides.length" class="hero-text">
        <div class="eyebrow"><span class="eyebrow-dot" />等待第一部影片</div>
        <h2 class="hero-title">你的私人影院已就位</h2>
        <p class="overview">
          配好 115 账号与媒体库，转存或整理一部影片，它就会出现在这里。
        </p>
      </div>

      <Transition v-else name="hero-text" mode="out-in">
        <div :key="current?.key" class="hero-text" aria-live="polite">
          <div class="eyebrow">
            <span class="eyebrow-dot" />
            最新入库<template v-if="current?.kind"> · {{ current.kind }}</template>
            <template v-if="current?.when"> · {{ current.when }}</template>
          </div>
          <img
            v-if="current?.logo && !logoFailed.has(current.key)"
            class="hero-logo"
            :src="current.logo"
            :alt="current.title"
            @error="logoFailed.add(current.key)"
          />
          <h2 v-else class="hero-title">{{ current?.title }}</h2>
          <div class="meta">
            <span v-if="current?.year">{{ current.year }}</span>
            <span v-if="current?.rating" class="meta-rating">
              <Star :size="13" :stroke-width="0" fill="currentColor" />{{ current.rating.toFixed(1) }}
            </span>
            <span v-if="current?.badge" class="meta-badge">{{ current.badge }}</span>
            <span v-if="current?.genres?.length">{{ current.genres.join(' / ') }}</span>
          </div>
          <p v-if="current?.overview" class="overview">{{ current.overview }}</p>
        </div>
      </Transition>

      <Transition name="hero-poster" mode="out-in">
        <PosterImage
          v-if="current"
          :key="current.key"
          class="hero-poster"
          :src="current.poster"
          :alt="current.title"
          :icon="Clapperboard"
          :icon-size="28"
        />
      </Transition>
    </div>

    <!-- ==== 控制 ==== -->
    <div class="hero-foot">
      <div v-if="slides.length > 1" class="dots" role="tablist">
        <button
          v-for="(s, i) in slides"
          :key="s.key"
          type="button"
          role="tab"
          class="dot"
          :class="{ 'is-active': i === idx, 'is-paused': hovering }"
          :aria-selected="i === idx"
          :aria-label="s.title"
          @click="pick(i)"
        >
          <span class="dot-fill" :style="{ animationDuration: INTERVAL + 'ms' }" />
        </button>
      </div>
      <div v-if="slides.length > 1" class="arrows">
        <button type="button" class="arrow" aria-label="上一部" @click="go(-1)">
          <ChevronLeft :size="18" />
        </button>
        <button type="button" class="arrow" aria-label="下一部" @click="go(1)">
          <ChevronRight :size="18" />
        </button>
      </div>
      <div class="extra"><slot name="extra" /></div>
    </div>
  </section>
</template>

<style scoped>
/* 横幅左右出血到内容区边缘，往上一直铺到顶栏底下（--content-px 与顶栏高度由 AppLayout 给） */
.hero {
  position: relative;
  isolation: isolate;
  margin: 0 calc(-1 * var(--content-px, 28px));
  min-height: 460px;
  overflow: hidden;
  /* 图没到之前的底色：暗琥珀，和强调色同一个色相，不会是一块死黑 */
  background: oklch(0.2 0.03 50);
}

/* ---- 背景 ---- */
.hero-bg {
  position: absolute;
  inset: 0;
  z-index: -2;
}
.bg-slide {
  position: absolute;
  inset: 0;
  opacity: 0;
  transition: opacity 900ms ease;
}
.bg-slide.is-active {
  opacity: 1;
}
.bg-blur,
.bg-img {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  object-fit: cover;
}
/* 没有背景图时的替身：海报放大、重度虚化，只取它的色调 */
.bg-blur {
  filter: blur(48px) saturate(1.4) brightness(0.7);
  transform: scale(1.3);
}
.bg-img {
  object-position: center 22%;
}
/* 缓慢推近（Ken Burns），让静态背景图有一点「在放映」的感觉 */
.bg-slide.is-active .bg-img {
  animation: hero-zoom 14s ease-out both;
}
@keyframes hero-zoom {
  from {
    transform: scale(1.08);
  }
  to {
    transform: scale(1);
  }
}
/* 空状态的放映机光束 */
.bg-glow {
  position: absolute;
  inset: 0;
  opacity: 0;
  background:
    radial-gradient(60% 80% at 80% 0%, color-mix(in oklab, var(--accent) 45%, transparent), transparent 70%),
    radial-gradient(50% 60% at 10% 100%, oklch(0.4 0.1 30 / 0.6), transparent 70%);
  transition: opacity 400ms ease;
}
.is-empty .bg-glow {
  opacity: 1;
}

/* 遮罩：上沿压暗给顶栏的字，左侧压暗给正文，底部渐变到页面底色给指标卡 */
.hero-scrim {
  position: absolute;
  inset: 0;
  z-index: -1;
  pointer-events: none;
  background:
    linear-gradient(to bottom, rgb(0 0 0 / 0.55) 0%, rgb(0 0 0 / 0) 26%),
    linear-gradient(to right, rgb(0 0 0 / 0.78) 0%, rgb(0 0 0 / 0.45) 42%, rgb(0 0 0 / 0.05) 75%),
    linear-gradient(to bottom, transparent 55%, rgb(0 0 0 / 0.4) 79%, var(--background) 100%);
}

/* ---- 前景 ---- */
.hero-inner {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 40px;
  min-height: 460px;
  box-sizing: border-box;
  padding: 108px calc(var(--content-px, 28px) + 12px) 142px;
}
.hero-text {
  flex: 1;
  min-width: 0;
  max-width: 620px;
}
.eyebrow {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 12.5px;
  font-weight: 500;
  letter-spacing: 0.08em;
  color: color-mix(in oklab, var(--accent) 70%, white);
}
.eyebrow-dot {
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
.hero-title {
  margin: 12px 0 0;
  font-size: clamp(28px, 3.6vw, 46px);
  font-weight: 750;
  line-height: 1.12;
  letter-spacing: -0.03em;
  text-shadow: 0 2px 24px rgb(0 0 0 / 0.5);
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.hero-logo {
  display: block;
  margin-top: 14px;
  max-width: min(360px, 80%);
  max-height: 110px;
  object-fit: contain;
  object-position: left bottom;
  filter: drop-shadow(0 4px 18px rgb(0 0 0 / 0.55));
}
.meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 14px;
  margin-top: 14px;
  font-size: 13.5px;
  color: var(--muted);
}
.meta-rating {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-weight: 600;
  color: oklch(0.86 0.15 85);
}
.meta-badge {
  padding: 0 6px;
  border: 1px solid rgb(255 255 255 / 0.35);
  border-radius: 4px;
  font-size: 11.5px;
  line-height: 18px;
}
.overview {
  margin: 14px 0 0;
  font-size: 14px;
  line-height: 1.7;
  color: rgb(255 255 255 / 0.78);
  display: -webkit-box;
  -webkit-line-clamp: 3;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.hero-poster {
  width: 176px;
  flex-shrink: 0;
  border-radius: 14px;
  box-shadow:
    0 30px 60px -20px rgb(0 0 0 / 0.8),
    0 0 0 1px rgb(255 255 255 / 0.1);
  transform: rotate(1.5deg);
}

/* ---- 底部控制条：进度点 / 箭头 / 右侧附加（数据来源与按钮） ---- */
.hero-foot {
  position: absolute;
  left: calc(var(--content-px, 28px) + 12px);
  right: calc(var(--content-px, 28px) + 12px);
  bottom: 94px;
  display: flex;
  align-items: center;
  gap: 16px;
}
.dots {
  display: flex;
  gap: 6px;
}
.dot {
  position: relative;
  width: 22px;
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
  /* 放大点击区域，4px 高的条不好点 */
  content: '';
  position: absolute;
  inset: -10px -2px;
}
.dot.is-active {
  width: 44px;
}
.dot-fill {
  position: absolute;
  inset: 0;
  transform-origin: left;
  transform: scaleX(0);
  background: var(--accent);
}
.dot.is-active .dot-fill {
  animation: dot-progress linear forwards;
}
.dot.is-paused .dot-fill {
  animation-play-state: paused;
}
@keyframes dot-progress {
  to {
    transform: scaleX(1);
  }
}
.arrows {
  display: flex;
  gap: 6px;
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
.extra {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

/* ---- 骨架 ---- */
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
  height: 40px;
  margin-top: 16px;
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

/* ---- 切换动画 ---- */
.hero-text-enter-active {
  transition:
    opacity 500ms ease 150ms,
    transform 500ms cubic-bezier(0.2, 0.8, 0.2, 1) 150ms;
}
.hero-text-leave-active {
  transition: opacity 200ms ease;
}
.hero-text-enter-from {
  opacity: 0;
  transform: translateY(12px);
}
.hero-text-leave-to {
  opacity: 0;
}
.hero-poster-enter-active {
  transition:
    opacity 500ms ease 200ms,
    transform 600ms cubic-bezier(0.2, 0.8, 0.2, 1) 200ms;
}
.hero-poster-leave-active {
  transition: opacity 200ms ease;
}
.hero-poster-enter-from {
  opacity: 0;
  transform: rotate(4deg) translateY(16px) scale(0.96);
}
.hero-poster-leave-to {
  opacity: 0;
}

@media (prefers-reduced-motion: reduce) {
  .bg-slide.is-active .bg-img,
  .eyebrow-dot,
  .sk {
    animation: none;
  }
}

/* ---- 断点 ---- */
@media (max-width: 1080px) {
  .hero-poster {
    width: 140px;
  }
}
@media (max-width: 720px) {
  .hero,
  .hero-inner {
    min-height: 400px;
  }
  .hero-inner {
    padding: 80px calc(var(--content-px, 16px) + 4px) 132px;
  }
  .hero-poster {
    display: none;
  }
  .overview {
    -webkit-line-clamp: 2;
    font-size: 13px;
  }
  .hero-foot {
    left: calc(var(--content-px, 16px) + 4px);
    right: calc(var(--content-px, 16px) + 4px);
    bottom: 76px;
    flex-wrap: wrap;
  }
  .arrows {
    display: none;
  }
}
</style>
