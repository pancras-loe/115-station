<script setup lang="ts">
import { NTabPane, NTabs } from 'naive-ui'
import StrmTab from './settings/StrmTab.vue'
import TmdbTab from './settings/TmdbTab.vue'
import ProxyTab from './settings/ProxyTab.vue'
import EmbyTab from './settings/EmbyTab.vue'
import { useTabQuery } from '@/composables/useTabQuery'

const tab = useTabQuery('strm')
</script>

<template>
  <NTabs v-model:value="tab" type="line" animated>
    <NTabPane name="strm" tab="STRM 配置"><StrmTab /></NTabPane>
    <NTabPane name="tmdb" tab="TMDB 配置"><TmdbTab /></NTabPane>
    <NTabPane name="proxy" tab="代理配置"><ProxyTab /></NTabPane>
    <NTabPane name="emby" tab="EMBY 管理"><EmbyTab /></NTabPane>
  </NTabs>
</template>
