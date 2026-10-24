<script setup lang="ts">
import HTabs from '@/components/hero/HTabs.vue'
import ProviderCard from '@/components/scrape/ProviderCard.vue'
import ScrapeTab from './scrape/ScrapeTab.vue'
import MediaTab from './scrape/MediaTab.vue'
import PersonFillPanel from '@/components/scrape/PersonFillPanel.vue'
import { useTabQuery } from '@/composables/useTabQuery'

/**
 * 影视刮削（2026-10-04 起独立成页）：原来散在「自动整理 → 影视刮削」与「扩展功能」
 * （媒体信息补全、演职人员补全）里的元数据配置收到这里。
 * 自动整理那边保留一个只读页签显示当前配置，改要跳过来 —— 开关只在一处能改，
 * 免得两页各存一份互相覆盖（`incr.cron` 那种坑）。
 *
 * 顶部一行是 Emby 媒体库设置检查（只读）。刮不刮按库设，在「账号与媒体库 → 媒体库」的库卡片上，
 * 原来这里的刮削方式总开关（本站 / Emby）2026-10-15 起已拆到各库。
 */
const tab = useTabQuery('scrape')

const TABS = [
  { value: 'scrape', label: '刮削' },
  { value: 'media', label: '媒体信息' },
  { value: 'people', label: '演职人员' },
]
</script>

<template>
  <div class="h-tabs-page">
    <ProviderCard />
    <HTabs v-model="tab" :items="TABS" />
    <ScrapeTab v-if="tab === 'scrape'" />
    <MediaTab v-else-if="tab === 'media'" />
    <PersonFillPanel v-else-if="tab === 'people'" />
  </div>
</template>
