<script setup lang="ts">
import HTabs from '@/components/hero/HTabs.vue'
import GuanyingTab from './transfer/GuanyingTab.vue'
import PansouTab from './transfer/PansouTab.vue'
import MukakuTab from './transfer/MukakuTab.vue'
import Re0Tab from './transfer/Re0Tab.vue'
import { useTabQuery } from '@/composables/useTabQuery'

const tab = useTabQuery('gy')

const TABS = [
  { value: 'gy', label: '观影' },
  { value: 'pansou', label: '盘搜' },
  { value: 'mukaku', label: '不太灵影视' },
  { value: 're0', label: 'RE0' },
]
</script>

<template>
  <div class="h-tabs-page">
    <HTabs v-model="tab" :items="TABS" />
    <GuanyingTab v-if="tab === 'gy'" />
    <PansouTab v-else-if="tab === 'pansou'" />
    <MukakuTab v-else-if="tab === 'mukaku'" />
    <Re0Tab v-else-if="tab === 're0'" />
  </div>
</template>
