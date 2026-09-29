<script setup lang="ts">
import { ref, watch, type Component } from 'vue'
import { Clapperboard } from '@lucide/vue'

/**
 * 界面上所有海报 / 封面统一走这个组件：占位图始终垫在底下，图片加载完淡入盖上去，
 * 加载失败就留着占位 —— 海报 404 很常见（刮削未完成、TMDB 不通、Emby 图片代理失败），
 * 浏览器的裂图图标比没图更难看。
 *
 * 尺寸由调用方的 class 决定（scoped 样式能落到本组件根元素上），默认 2:3 竖版。
 */
const props = withDefaults(
  defineProps<{
    src?: string | null
    alt?: string
    /** 占位图标，默认场记板；目录 / 剧集等场景可以换 */
    icon?: Component
    iconSize?: number
    /** 外站直链（资源站封面）：不带 Referer，很多图床按 Referer 防盗链 */
    noReferrer?: boolean
  }>(),
  { src: null, alt: '', icon: undefined, iconSize: 20, noReferrer: false },
)

const state = ref<'loading' | 'loaded' | 'failed'>('loading')
// 列表复用同一个组件实例时 src 会换（翻页、筛选），状态要跟着重置
watch(
  () => props.src,
  () => (state.value = 'loading'),
)
</script>

<template>
  <div class="pimg" :class="src ? `is-${state}` : 'is-failed'">
    <div class="pimg-ph" aria-hidden="true">
      <slot name="placeholder">
        <component :is="icon ?? Clapperboard" :size="iconSize" :stroke-width="1.5" />
      </slot>
    </div>
    <img
      v-if="src && state !== 'failed'"
      :src="src"
      :alt="alt"
      loading="lazy"
      decoding="async"
      :referrerpolicy="noReferrer ? 'no-referrer' : undefined"
      @load="state = 'loaded'"
      @error="state = 'failed'"
    />
    <slot />
  </div>
</template>

<style scoped>
.pimg {
  position: relative;
  flex: none;
  aspect-ratio: 2 / 3;
  border-radius: var(--r-sm);
  overflow: hidden;
  background: var(--surface-secondary, var(--default));
}
.pimg-ph {
  position: absolute;
  inset: 0;
  display: grid;
  place-items: center;
  color: var(--muted);
  background: linear-gradient(
    160deg,
    color-mix(in oklab, var(--muted) 10%, transparent),
    color-mix(in oklab, var(--muted) 4%, transparent)
  );
}
.pimg-ph :deep(svg) {
  opacity: 0.55;
}
/* 加载中：占位上扫一道微光，告诉用户图在路上，不是没有图 */
.is-loading .pimg-ph::after {
  content: '';
  position: absolute;
  inset: 0;
  background: linear-gradient(
    100deg,
    transparent 30%,
    color-mix(in oklab, var(--foreground) 6%, transparent) 50%,
    transparent 70%
  );
  background-size: 200% 100%;
  animation: pimg-shimmer 1.4s ease-in-out infinite;
}
@keyframes pimg-shimmer {
  from {
    background-position: 150% 0;
  }
  to {
    background-position: -50% 0;
  }
}
@media (prefers-reduced-motion: reduce) {
  .is-loading .pimg-ph::after {
    animation: none;
  }
}
.pimg img {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
  opacity: 0;
  transition: opacity 0.2s ease-out;
}
.is-loaded img {
  opacity: 1;
}
</style>
