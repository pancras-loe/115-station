<script setup lang="ts">
import HButton from '@/components/hero/HButton.vue'

/** 页签底部的保存条：有改动时变色提醒，没改动时显示一句常驻说明 */
defineProps<{ dirty: boolean; saving: boolean; note?: string }>()
defineEmits<{ save: [] }>()
</script>

<template>
  <div class="save-bar" :class="{ 'is-dirty': dirty }">
    <span class="save-note">{{ dirty ? '有未保存的改动' : note }}</span>
    <HButton variant="primary" size="sm" :loading="saving" @click="$emit('save')">保存配置</HButton>
  </div>
</template>

<style scoped>
.save-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 8px 8px 16px;
  border-radius: var(--r-lg);
  box-shadow: inset 0 0 0 1px var(--border);
  transition:
    background-color 150ms ease,
    box-shadow 150ms ease;
}
.save-bar.is-dirty {
  background: color-mix(in oklab, var(--accent) 8%, transparent);
  box-shadow: inset 0 0 0 1px color-mix(in oklab, var(--accent) 40%, transparent);
}
.save-note {
  flex: 1;
  min-width: 0;
  font-size: 12.5px;
  color: var(--muted);
}
.is-dirty .save-note {
  color: var(--accent);
  font-weight: 500;
}
</style>
