<script setup lang="ts">
defineProps<{
  on: boolean
  /** 登录态下的补充信息，通常是用户名 */
  sub?: string
  /** 即将过期等中间态 */
  warn?: boolean
}>()
</script>

<template>
  <span class="badge" :class="warn ? 'warn' : on ? 'on' : 'off'">
    <span class="dot" />
    {{ warn ? '即将过期' : on ? '已登录' : '未登录' }}
    <span v-if="sub" class="sub">{{ sub }}</span>
  </span>
</template>

<style scoped>
.badge {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12.5px;
  color: var(--c-text-3);
}
.dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--c-text-4);
}
.badge.on {
  color: var(--c-success);
}
.badge.on .dot {
  background: var(--c-success);
}
.badge.warn {
  color: var(--c-warning);
}
.badge.warn .dot {
  background: var(--c-warning);
}
.sub {
  color: var(--c-text-3);
}
</style>
