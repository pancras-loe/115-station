<script setup lang="ts">
import { RouterLink, useRoute } from 'vue-router'
import { navItems } from './navItems'
import BrandMark from '@/components/BrandMark.vue'

defineProps<{ version?: string }>()
const emit = defineEmits<{ navigate: [] }>()
const route = useRoute()
</script>

<template>
  <aside class="sidebar">
    <div class="brand">
      <BrandMark :size="34" class="brand-mark" />
      <div class="brand-text">
        <span class="brand-name">Strm<span class="brand-accent">Station</span></span>
        <span class="brand-sub">媒体库自动化</span>
      </div>
    </div>

    <nav class="nav">
      <template v-for="item in navItems" :key="item.name">
        <div v-if="item.group" class="nav-group">{{ item.group }}</div>
        <RouterLink
          :to="{ name: item.name }"
          class="nav-item"
          :class="{ 'is-active': route.name === item.name }"
          @click="emit('navigate')"
        >
          <component :is="item.icon" :size="17" :stroke-width="1.75" class="nav-icon" />
          <span>{{ item.label }}</span>
        </RouterLink>
      </template>
    </nav>

    <div class="sidebar-foot">
      <span class="foot-version">{{ version || 'StrmStation' }}</span>
      <a
        class="foot-link"
        href="https://t.me/+7b_HYMltYMozZTk1"
        target="_blank"
        rel="noopener"
      >TG 交流群</a>
    </div>
  </aside>
</template>

<style scoped>
.sidebar {
  width: var(--sidebar-w);
  height: 100%;
  display: flex;
  flex-direction: column;
  background: var(--c-bg-elevated);
  border-right: 1px solid var(--c-border);
}

.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 18px 18px 14px;
}
.brand-mark {
  border-radius: var(--radius);
  box-shadow: 0 4px 12px -4px color-mix(in srgb, var(--c-primary) 55%, transparent);
}
.brand-text {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
}
.brand-name {
  font-size: 16px;
  font-weight: 700;
  letter-spacing: -0.025em;
  line-height: 1.2;
  color: var(--c-text-1);
}
.brand-accent {
  color: var(--c-primary);
}
.brand-sub {
  font-size: 11px;
  letter-spacing: 0.06em;
  color: var(--c-text-3);
}

.nav {
  flex: 1;
  overflow-y: auto;
  padding: 4px 10px 12px;
}
.nav-group {
  padding: 14px 8px 6px;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.04em;
  color: var(--c-text-3);
}
.nav-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  margin-bottom: 2px;
  border-radius: var(--radius);
  font-size: 13.5px;
  color: var(--c-text-2);
  text-decoration: none;
  transition: background-color 0.15s, color 0.15s;
}
.nav-item:hover {
  background: var(--c-bg-hover);
  color: var(--c-text-1);
}
.nav-item.is-active {
  background: var(--c-primary-soft);
  color: var(--c-primary);
  font-weight: 500;
}
.nav-icon {
  flex-shrink: 0;
}

.sidebar-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 12px 18px;
  border-top: 1px solid var(--c-border);
  font-size: 11.5px;
  color: var(--c-text-3);
}
.foot-version {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.foot-link {
  flex-shrink: 0;
  color: var(--c-primary);
  text-decoration: none;
}
.foot-link:hover {
  text-decoration: underline;
}
</style>
