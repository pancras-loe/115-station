<script setup lang="ts">
defineProps<{ title?: string; hint?: string }>()
</script>

<template>
  <section class="section">
    <header v-if="title || $slots.extra" class="section-head">
      <div class="section-titles">
        <h2 v-if="title" class="section-title">{{ title }}</h2>
        <p v-if="hint" class="section-hint">{{ hint }}</p>
      </div>
      <div v-if="$slots.extra" class="section-extra"><slot name="extra" /></div>
    </header>
    <div class="section-body"><slot /></div>
  </section>
</template>

<style scoped>
.section {
  background: var(--c-bg-elevated);
  border: 1px solid var(--c-border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-card);
}
.section-head {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 15px 18px;
  border-bottom: 1px solid var(--c-border);
}
.section-titles {
  flex: 1;
  min-width: 0;
}
.section-title {
  margin: 0;
  font-size: 14.5px;
  font-weight: 600;
  color: var(--c-text-1);
}
.section-hint {
  margin: 2px 0 0;
  font-size: 12px;
  color: var(--c-text-3);
}
.section-extra {
  flex-shrink: 0;
}
.section-body {
  padding: 18px;
}
</style>
