<script setup lang="ts">
import { computed } from 'vue'
import MediaTypeChip from '@/components/MediaTypeChip.vue'
import HSpinner from '@/components/hero/HSpinner.vue'
import { BellPlus, BellRing, Check, Search, Star } from '@lucide/vue'
import PosterImage from '@/components/PosterImage.vue'
import { resourcesApi } from '@/api'
import type { TmdbCandidate } from '@/api/resources'
import type { OwnedInfo } from '@/api/transfer'
import { useSubscriptions } from '@/composables/subscriptions'

/**
 * 发现页与搜索结果共用的海报卡片。悬停时海报浮起、压一层渐变露出简介（Jellyseerr / MoviePilot 同款），
 * 点海报 = 选定这部去各站找资源；右上角是一键订阅（订过的常亮，点了打开订阅详情）。
 */
const props = defineProps<{
  item: TmdbCandidate
  owned?: OwnedInfo
  /** 榜单行里的名次（1 起），只给前十名显示 */
  rank?: number
}>()
const emit = defineEmits<{ pick: [TmdbCandidate] }>()

const subs = useSubscriptions()
const sub = computed(() => subs.findSub(props.item.id, props.item.media_type))
const busy = computed(() => subs.quickBusy.value.has(`${props.item.media_type}:${props.item.id}`))
function onSub() {
  if (sub.value) subs.openDetail(sub.value.id)
  else void subs.quickSubscribe(props.item.id, props.item.media_type)
}
const SUB_TIP = {
  movie: '订阅：找到资源自动转存入库',
  tv: '订阅：按默认设置补缺集，在播的多季剧只订最新一季。要改范围到「订阅」里修改',
}
// 评分配色：高分偏绿、及格偏黄、低分灰，扫一眼就能分出好坏
const voteTone = computed(() => {
  const v = props.item.vote ?? 0
  return v >= 7.5 ? 'hi' : v >= 6 ? 'mid' : 'lo'
})
</script>

<template>
  <div class="pc">
    <button type="button" class="pc-main" :aria-label="`${item.title}，去找资源`" @click="emit('pick', item)">
      <div class="pc-frame">
        <PosterImage
          class="pc-poster"
          :src="item.poster ? resourcesApi.tmdbImageUrl(item.poster, 'w342') : null"
          :alt="item.title"
          :icon-size="24"
        />
        <div class="pc-shade" aria-hidden="true">
          <p v-if="item.overview" class="pc-overview">{{ item.overview }}</p>
          <span class="pc-go"><Search :size="13" />找资源</span>
        </div>
        <span v-if="rank && rank <= 10" class="pc-rank" aria-hidden="true">{{ rank }}</span>
        <span v-if="item.vote" class="pc-vote" :class="`is-${voteTone}`">
          <Star :size="10" :stroke-width="0" fill="currentColor" />{{ item.vote.toFixed(1) }}
        </span>
        <span v-if="owned" class="pc-owned"><Check :size="11" :stroke-width="3" />已入库</span>
      </div>
      <div class="pc-title">{{ item.title }}</div>
      <div class="pc-meta">
        <MediaTypeChip :type="item.media_type" />
        <span v-if="item.year">{{ item.year }}</span>
      </div>
    </button>
    <button
      type="button"
      class="pc-sub"
      :class="{ on: sub }"
      :disabled="busy"
      :title="sub ? '已订阅，点击查看' : SUB_TIP[item.media_type]"
      :aria-label="sub ? `已订阅 ${item.title}` : `订阅 ${item.title}`"
      @click="onSub"
    >
      <HSpinner v-if="busy" size="sm" />
      <BellRing v-else-if="sub" :size="14" />
      <BellPlus v-else :size="14" />
    </button>
  </div>
</template>

<style scoped>
.pc {
  position: relative;
  min-width: 0;
}
.pc-main {
  all: unset;
  box-sizing: border-box;
  display: flex;
  flex-direction: column;
  gap: 7px;
  width: 100%;
  min-width: 0;
  cursor: pointer;
}
.pc-main:focus-visible .pc-frame {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}
.pc-frame {
  position: relative;
  border-radius: var(--r-lg);
  overflow: hidden;
  box-shadow:
    0 10px 24px -14px rgb(0 0 0 / 0.55),
    0 0 0 1px color-mix(in oklab, var(--foreground) 6%, transparent);
  transition:
    transform 0.28s cubic-bezier(0.2, 0.8, 0.2, 1),
    box-shadow 0.28s ease;
}
.pc-poster {
  width: 100%;
  aspect-ratio: 2 / 3;
  border-radius: 0;
}
.pc-poster :deep(img) {
  transition: transform 0.5s cubic-bezier(0.2, 0.8, 0.2, 1);
}
/* 悬停露出的那一层：下半截压暗，放简介与「找资源」 */
.pc-shade {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  justify-content: flex-end;
  gap: 8px;
  padding: 12px 10px 10px;
  color: #fff;
  background: linear-gradient(to top, rgb(0 0 0 / 0.92) 0%, rgb(0 0 0 / 0.6) 45%, rgb(0 0 0 / 0) 75%);
  opacity: 0;
  transition: opacity 0.25s ease;
}
.pc-overview {
  margin: 0;
  font-size: 11.5px;
  line-height: 1.55;
  color: rgb(255 255 255 / 0.82);
  display: -webkit-box;
  -webkit-line-clamp: 5;
  line-clamp: 5;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.pc-go {
  align-self: flex-start;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 3px 10px;
  border-radius: 999px;
  font-size: 11.5px;
  font-weight: 600;
  background: var(--accent);
  color: var(--accent-foreground);
}
@media (hover: hover) {
  .pc-main:hover .pc-frame {
    transform: translateY(-4px);
    box-shadow:
      0 22px 40px -18px rgb(0 0 0 / 0.7),
      0 0 0 2px color-mix(in oklab, var(--accent) 70%, transparent),
      0 0 32px -6px color-mix(in oklab, var(--accent) 45%, transparent);
  }
  .pc-main:hover .pc-poster :deep(img) {
    transform: scale(1.06);
  }
  .pc-main:hover .pc-shade {
    opacity: 1;
  }
}

/* 名次：描边大数字压在左下角，榜单行里一眼看出前十 */
.pc-rank {
  position: absolute;
  left: 6px;
  bottom: -6px;
  font-size: 54px;
  font-weight: 900;
  line-height: 1;
  letter-spacing: -0.06em;
  color: rgb(255 255 255 / 0.12);
  -webkit-text-stroke: 1.5px rgb(255 255 255 / 0.85);
  text-shadow: 0 4px 18px rgb(0 0 0 / 0.6);
  pointer-events: none;
  transition: opacity 0.2s;
}
@media (hover: hover) {
  .pc-main:hover .pc-rank {
    opacity: 0;
  }
}
.pc-vote {
  position: absolute;
  left: 7px;
  top: 7px;
  display: inline-flex;
  align-items: center;
  gap: 3px;
  padding: 0 7px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 700;
  line-height: 20px;
  background: rgb(0 0 0 / 0.55);
  backdrop-filter: blur(6px);
}
.pc-vote.is-hi {
  color: oklch(0.85 0.17 150);
}
.pc-vote.is-mid {
  color: oklch(0.87 0.15 85);
}
.pc-vote.is-lo {
  color: rgb(255 255 255 / 0.75);
}
.pc-owned {
  position: absolute;
  right: 7px;
  bottom: 7px;
  display: inline-flex;
  align-items: center;
  gap: 3px;
  padding: 0 8px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 600;
  line-height: 20px;
  background: var(--success);
  color: var(--success-foreground);
  box-shadow: 0 2px 10px rgb(0 0 0 / 0.35);
}
.pc-title {
  font-size: 13px;
  font-weight: 600;
  line-height: 1.35;
  color: var(--foreground);
  display: -webkit-box;
  -webkit-line-clamp: 1;
  line-clamp: 1;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.pc-meta {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: -2px;
  font-size: 12px;
  color: var(--muted);
}

/* 订阅按钮压在海报右上角：订过的常亮，没订的桌面端悬停才出来（手机没有悬停，一直显示） */
.pc-sub {
  all: unset;
  position: absolute;
  right: 7px;
  top: 7px;
  display: grid;
  place-items: center;
  width: 28px;
  height: 28px;
  border-radius: 999px;
  background: rgb(0 0 0 / 0.5);
  color: #fff;
  backdrop-filter: blur(6px);
  cursor: pointer;
  transition:
    opacity 0.15s,
    transform 0.28s cubic-bezier(0.2, 0.8, 0.2, 1),
    background-color 0.15s;
}
.pc-sub:hover,
.pc-sub.on {
  background: var(--accent);
  color: var(--accent-foreground);
}
.pc-sub:disabled {
  cursor: default;
}
.pc-sub:focus-visible {
  outline: 2px solid var(--accent);
}
@media (hover: hover) {
  .pc-sub:not(.on):not(:disabled) {
    opacity: 0;
  }
  .pc:hover .pc-sub,
  .pc-sub:focus-visible {
    opacity: 1;
  }
  /* 跟着海报一起浮起 */
  .pc:hover .pc-sub {
    transform: translateY(-4px);
  }
}
@media (prefers-reduced-motion: reduce) {
  .pc-frame,
  .pc-poster :deep(img),
  .pc-sub {
    transition: none;
  }
}
</style>
