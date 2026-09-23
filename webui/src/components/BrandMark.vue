<script setup lang="ts">
import { useId } from 'vue'

// 品牌标：播放三角 + 左侧流动线，寓意「STRM 直链 302 推流」。
// 刻意不用「115」字样与 115 的配色/图形：那是 115 网盘的商标，放进自己的 logo 有侵权风险。
// 内联 SVG 而不是 <img>：<img> 里的 SVG 读不到主题令牌，暗色模式下颜色会对不上。
withDefaults(defineProps<{ size?: number }>(), { size: 32 })

// 同一页面可能出现多个实例，渐变 id 必须唯一，否则后渲染的会引用到前一个
const gid = `brand-grad-${useId()}`
const hid = `brand-hi-${useId()}`
</script>

<template>
  <svg
    class="brand-mark-svg"
    :width="size"
    :height="size"
    viewBox="0 0 32 32"
    aria-hidden="true"
  >
    <defs>
      <linearGradient :id="gid" x1="0" y1="0" x2="1" y2="1">
        <stop offset="0" stop-color="var(--c-primary-hover)" />
        <stop offset="1" stop-color="var(--c-primary-active)" />
      </linearGradient>
      <linearGradient :id="hid" x1="0" y1="0" x2="0" y2="1">
        <stop offset="0" stop-color="#fff" stop-opacity=".22" />
        <stop offset=".55" stop-color="#fff" stop-opacity="0" />
      </linearGradient>
    </defs>
    <rect width="32" height="32" rx="8.5" :fill="`url(#${gid})`" />
    <rect width="32" height="32" rx="8.5" :fill="`url(#${hid})`" />
    <g stroke="#fff" stroke-linecap="round" fill="none" stroke-width="2.2">
      <path d="M6.5 11.5h4" opacity=".55" />
      <path d="M4.8 16h6.2" opacity=".85" />
      <path d="M6.5 20.5h4" opacity=".55" />
    </g>
    <path
      d="M14.2 9.9c0-1.2 1.3-1.9 2.3-1.3l8.9 5.7a2 2 0 0 1 0 3.4l-8.9 5.7c-1 .6-2.3-.1-2.3-1.3z"
      fill="#fff"
    />
  </svg>
</template>

<style scoped>
.brand-mark-svg {
  display: block;
  flex-shrink: 0;
}
</style>
