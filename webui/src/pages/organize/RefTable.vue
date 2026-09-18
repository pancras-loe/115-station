<script setup lang="ts">
/** 参考表的统一渲染：左列代码、右列说明 */
defineProps<{ title: string; items: [string, string][]; mono?: boolean }>()
</script>

<template>
  <section class="block">
    <h3 class="block-title">{{ title }}</h3>
    <div class="chips">
      <div v-for="it in items" :key="it[0]" class="chip">
        <code :class="{ plain: !mono }">{{ it[0] }}</code>
        <span>{{ it[1] }}</span>
      </div>
    </div>
  </section>
</template>

<style scoped>
.block-title {
  margin: 0 0 9px;
  padding-bottom: 6px;
  font-size: 13px;
  font-weight: 600;
  color: var(--c-primary);
  border-bottom: 2px solid var(--c-primary-soft);
}
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.chip {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  padding: 6px 11px;
  border-radius: var(--radius-sm);
  background: var(--c-bg-raised);
  font-size: 12.5px;
  color: var(--c-text-2);
  transition: background-color 0.15s;
}
.chip:hover {
  background: var(--c-primary-soft);
}
.chip code {
  font-family: var(--font-mono);
  font-size: 11.5px;
  font-weight: 600;
  color: var(--c-primary);
}
.chip code.plain {
  font-family: inherit;
}
</style>
