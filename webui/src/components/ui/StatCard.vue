<script setup lang="ts">
import type { Component } from 'vue'

defineProps<{
  label: string
  value: string
  sub?: string
  icon?: Component
  /** 图标底色，取设计令牌名（primary / success / warning / danger） */
  tone?: 'primary' | 'success' | 'warning' | 'danger'
}>()
</script>

<template>
  <div class="stat">
    <div v-if="icon" class="stat-icon" :class="`tone-${tone ?? 'primary'}`">
      <component :is="icon" :size="18" :stroke-width="1.8" />
    </div>
    <div class="stat-body">
      <div class="stat-label">{{ label }}</div>
      <div class="stat-value">{{ value }}</div>
      <div v-if="sub" class="stat-sub">{{ sub }}</div>
    </div>
  </div>
</template>

<style scoped>
.stat {
  display: flex;
  align-items: flex-start;
  gap: 13px;
  padding: 17px 18px;
  background: var(--c-bg-elevated);
  border: 1px solid var(--c-border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-card);
  transition: border-color 0.15s, box-shadow 0.15s;
}
.stat:hover {
  border-color: var(--c-border-strong);
  box-shadow: var(--shadow-md);
}

.stat-icon {
  width: 36px;
  height: 36px;
  flex-shrink: 0;
  border-radius: var(--radius);
  display: grid;
  place-items: center;
}
.tone-primary {
  background: var(--c-primary-soft);
  color: var(--c-primary);
}
.tone-success {
  background: var(--c-success-soft);
  color: var(--c-success);
}
.tone-warning {
  background: var(--c-warning-soft);
  color: var(--c-warning);
}
.tone-danger {
  background: var(--c-danger-soft);
  color: var(--c-danger);
}

.stat-body {
  min-width: 0;
  flex: 1;
}
.stat-label {
  font-size: 12.5px;
  color: var(--c-text-3);
}
.stat-value {
  margin-top: 3px;
  font-size: 23px;
  font-weight: 600;
  line-height: 1.15;
  letter-spacing: -0.02em;
  color: var(--c-text-1);
  font-variant-numeric: tabular-nums;
}
.stat-sub {
  margin-top: 3px;
  font-size: 11.5px;
  color: var(--c-text-3);
}
</style>
