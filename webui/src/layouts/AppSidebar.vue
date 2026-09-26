<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { navItems } from './navItems'
import { recordStats } from '@/stores/recordStats'
import BrandMark from '@/components/BrandMark.vue'

defineProps<{ version?: string }>()
const emit = defineEmits<{ navigate: [] }>()
const route = useRoute()

/** 入口上的角标：任务中心挂「待确认」条数 —— 开着人工确认时，不点进去也要看得见有活 */
const badges = computed<Record<string, number>>(() => ({ tasks: recordStats.value.awaiting || 0 }))
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
          <span v-if="badges[item.name]" class="nav-badge" :title="`${badges[item.name]} 项待确认`">
            {{ badges[item.name] }}
          </span>
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
/* HeroUI 的做法：侧栏不另起一块白板，直接坐在页面底色上；
   选中项是一块浮起的 surface（白底 + 柔和阴影），像分段控件里被选中的那一格 */
.sidebar {
  width: var(--sidebar-w);
  height: 100%;
  display: flex;
  flex-direction: column;
  background: var(--background);
}

.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 20px 20px 12px;
}
.brand-mark {
  border-radius: 10px;
  box-shadow: 0 4px 12px -4px color-mix(in oklab, var(--accent) 55%, transparent);
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
  color: var(--foreground);
}
.brand-accent {
  color: var(--accent);
}
.brand-sub {
  font-size: 11px;
  letter-spacing: 0.06em;
  color: var(--muted);
}

.nav {
  flex: 1;
  overflow-y: auto;
  padding: 4px 12px 12px;
}
.nav-group {
  padding: 16px 12px 6px;
  font-size: 11.5px;
  font-weight: 500;
  color: var(--muted);
}
.nav-item {
  display: flex;
  align-items: center;
  gap: 10px;
  height: 38px;
  padding: 0 12px;
  margin-bottom: 2px;
  border-radius: 12px;
  font-size: 13.5px;
  color: color-mix(in oklab, var(--foreground) 72%, var(--muted));
  text-decoration: none;
  transition:
    background-color 150ms ease,
    color 150ms ease,
    box-shadow 150ms ease;
}
@media (hover: hover) {
  .nav-item:hover {
    background: var(--default);
    color: var(--foreground);
  }
}
.nav-item.is-active {
  background: var(--surface);
  color: var(--foreground);
  font-weight: 500;
  box-shadow: var(--surface-shadow);
}
.nav-item.is-active .nav-icon {
  color: var(--accent);
}
.nav-icon {
  flex-shrink: 0;
}
.nav-badge {
  margin-left: auto;
  min-width: 18px;
  height: 18px;
  padding: 0 5px;
  border-radius: 999px;
  background: var(--warning);
  color: var(--warning-foreground);
  font-size: 11px;
  font-weight: 600;
  line-height: 18px;
  text-align: center;
}

.sidebar-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 14px 24px 18px;
  font-size: 11.5px;
  color: var(--muted);
}
.foot-version {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.foot-link {
  flex-shrink: 0;
  color: var(--accent);
  text-decoration: none;
}
.foot-link:hover {
  text-decoration: underline;
}
</style>
