<script setup lang="ts">
import { CircleCheck, CircleX, LoaderCircle } from '@lucide/vue'

export interface BannerState {
  status: 'ok' | 'err' | 'pending'
  title: string
  detail?: string
  /** 右侧附加信息，比如延迟 ms */
  trailing?: string
}

defineProps<{ state?: BannerState | null }>()
</script>

<template>
  <div v-if="state" class="banner" :class="state.status">
    <LoaderCircle v-if="state.status === 'pending'" class="ico spin" :size="15" />
    <CircleCheck v-else-if="state.status === 'ok'" class="ico" :size="15" />
    <CircleX v-else class="ico" :size="15" />
    <div class="body">
      <div class="title">{{ state.title }}</div>
      <div v-if="state.detail" class="detail">{{ state.detail }}</div>
    </div>
    <div v-if="state.trailing" class="trailing">{{ state.trailing }}</div>
  </div>
</template>

<style scoped>
.banner {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  margin-top: 8px;
  padding: 8px 11px;
  border-radius: var(--radius);
  border: 1px solid transparent;
  font-size: 12.5px;
  line-height: 1.5;
}
.banner.ok {
  background: var(--c-success-soft);
  border-color: color-mix(in srgb, var(--c-success) 26%, transparent);
  color: var(--c-success);
}
.banner.err {
  background: var(--c-danger-soft);
  border-color: color-mix(in srgb, var(--c-danger) 26%, transparent);
  color: var(--c-danger);
}
.banner.pending {
  background: var(--c-bg-raised);
  border-color: var(--c-border);
  color: var(--c-text-2);
}

.ico {
  flex-shrink: 0;
  margin-top: 1px;
}
.spin {
  animation: spin 0.9s linear infinite;
}
@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}

.body {
  flex: 1;
  min-width: 0;
}
.title {
  font-weight: 500;
}
.detail {
  margin-top: 1px;
  color: var(--c-text-2);
  word-break: break-all;
}
.trailing {
  flex-shrink: 0;
  font-variant-numeric: tabular-nums;
  color: var(--c-text-2);
}
</style>
