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
  <div class="card card--default stat">
    <div class="stat-top">
      <span class="stat-label">{{ label }}</span>
      <span v-if="icon" class="stat-icon" :class="`tone-${tone ?? 'primary'}`">
        <component :is="icon" :size="16" :stroke-width="2" />
      </span>
    </div>
    <div class="stat-value">{{ value }}</div>
    <div v-if="sub" class="stat-sub">{{ sub }}</div>
  </div>
</template>

<style scoped>
.stat {
  gap: 0;
  padding: 18px 20px;
  min-width: 0;
}
.stat-top {
  display: flex;
  align-items: center;
  gap: 8px;
}
.stat-label {
  flex: 1;
  min-width: 0;
  font-size: 13px;
  color: var(--muted);
}
.stat-icon {
  width: 30px;
  height: 30px;
  flex-shrink: 0;
  border-radius: 999px;
  display: grid;
  place-items: center;
}
.tone-primary {
  background: var(--accent-soft);
  color: var(--accent-soft-foreground);
}
.tone-success {
  background: var(--success-soft);
  color: var(--success-soft-foreground);
}
.tone-warning {
  background: var(--warning-soft);
  color: var(--warning-soft-foreground);
}
.tone-danger {
  background: var(--danger-soft);
  color: var(--danger-soft-foreground);
}

.stat-value {
  margin-top: 6px;
  font-size: 28px;
  font-weight: 600;
  line-height: 1.15;
  letter-spacing: -0.025em;
  color: var(--foreground);
  font-variant-numeric: tabular-nums;
}
.stat-sub {
  margin-top: 4px;
  font-size: 12px;
  color: var(--muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 手机上两列并排，卡片只有 ~160px 宽：收小字号、副标题允许折行，否则全被省略号吃掉 */
@media (max-width: 720px) {
  .stat {
    padding: 14px 16px;
  }
  .stat-icon {
    width: 26px;
    height: 26px;
  }
  .stat-value {
    font-size: 22px;
  }
  .stat-sub {
    white-space: normal;
    font-size: 11.5px;
  }
}
</style>
