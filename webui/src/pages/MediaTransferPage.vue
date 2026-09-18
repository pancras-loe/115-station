<script setup lang="ts">
import { NTabPane, NTabs } from 'naive-ui'
import GuanyingTab from './transfer/GuanyingTab.vue'
import PansouTab from './transfer/PansouTab.vue'
import MukakuTab from './transfer/MukakuTab.vue'
import Re0Tab from './transfer/Re0Tab.vue'
import { useTabQuery } from '@/composables/useTabQuery'

const tab = useTabQuery('gy')
</script>

<template>
  <NTabs v-model:value="tab" type="line" animated>
    <NTabPane name="gy" tab="观影" display-directive="if"><GuanyingTab /></NTabPane>
    <NTabPane name="pansou" tab="盘搜" display-directive="if"><PansouTab /></NTabPane>
    <NTabPane name="mukaku" tab="不太灵影视" display-directive="if"><MukakuTab /></NTabPane>
    <NTabPane name="re0" tab="RE0" display-directive="if"><Re0Tab /></NTabPane>
  </NTabs>
</template>
