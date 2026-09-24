<script setup lang="ts">
import HTabs from '@/components/hero/HTabs.vue'
import TmdbTab from './settings/TmdbTab.vue'
import ProxyTab from './settings/ProxyTab.vue'
import EmbyTab from './settings/EmbyTab.vue'
import { useTabQuery } from '@/composables/useTabQuery'

const tab = useTabQuery('tmdb')

const TABS = [
  { value: 'tmdb', label: 'TMDB 配置' },
  { value: 'proxy', label: '代理配置' },
  { value: 'emby', label: 'EMBY 管理' },
]
</script>

<template>
  <div class="h-tabs-page">
    <HTabs v-model="tab" :items="TABS" />
    <TmdbTab v-if="tab === 'tmdb'" />
    <ProxyTab v-else-if="tab === 'proxy'" />
    <EmbyTab v-else-if="tab === 'emby'" />
  </div>
</template>
