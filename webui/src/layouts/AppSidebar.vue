<script setup lang="ts">
import { RouterLink, useRoute } from 'vue-router'
import { navItems } from './navItems'

defineProps<{ version?: string }>()
const emit = defineEmits<{ navigate: [] }>()
const route = useRoute()
</script>

<template>
  <aside class="sidebar">
    <div class="brand">
      <div class="brand-mark">115</div>
      <div class="brand-text">
        <span class="brand-name">115<span class="brand-accent">Station</span></span>
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

    <div class="sidebar-foot">{{ version || '115-Station' }}</div>
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
  width: 34px;
  height: 34px;
  flex-shrink: 0;
  border-radius: var(--radius);
  display: grid;
  place-items: center;
  font-size: 12px;
  font-weight: 700;
  letter-spacing: -0.02em;
  color: #fff;
  background: linear-gradient(135deg, var(--c-primary), var(--c-primary-hover));
  box-shadow: var(--shadow-sm);
}
.brand-text {
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.brand-name {
  font-size: 15px;
  font-weight: 650;
  letter-spacing: -0.01em;
  color: var(--c-text-1);
}
.brand-accent {
  color: var(--c-primary);
}
.brand-sub {
  font-size: 11px;
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
  padding: 12px 18px;
  border-top: 1px solid var(--c-border);
  font-size: 11.5px;
  color: var(--c-text-3);
}
</style>
