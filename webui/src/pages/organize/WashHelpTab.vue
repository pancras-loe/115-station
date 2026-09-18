<script setup lang="ts">
import RefTable from './RefTable.vue'
import { WASH_FIELDS, WASH_MODES, WASH_RULES } from './categoryRef'
</script>

<template>
  <div class="doc">
    <RefTable title="洗版模式（mode）" :items="WASH_MODES" mono />
    <RefTable title="优先级字段" :items="WASH_FIELDS" mono />
    <RefTable title="匹配规则" :items="WASH_RULES" />
  </div>
</template>

<style scoped>
.doc {
  display: flex;
  flex-direction: column;
  gap: 18px;
  padding: 18px;
  background: var(--c-bg-elevated);
  border: 1px solid var(--c-border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-card);
}
</style>
