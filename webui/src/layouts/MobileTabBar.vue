<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { Ellipsis } from '@lucide/vue'
import { navItems } from './navItems'
import { recordStats } from '@/stores/recordStats'

/**
 * 手机底部导航：放最常用的四个入口，其余收进「更多」（打开侧栏抽屉）。
 * 抽屉菜单要伸手够左上角，单手握持时底栏才是拇指够得着的地方。
 */
// 任务中心占了原来 Strm 管理的位置：手机上最常做的是看进度、确认待确认的条目，
// Strm 管理基本是配置和偶尔一次全量，收进「更多」
const TAB_NAMES = ['dashboard', 'tasks', 'organize', 'media-transfer']
/** 底栏上的标签只有四五个字宽，用短名 */
const SHORT: Record<string, string> = {
  dashboard: '总览',
  tasks: '任务',
  organize: '整理',
  'media-transfer': '转存',
}

const emit = defineEmits<{ more: [] }>()
const route = useRoute()

const tabs = computed(() =>
  TAB_NAMES.map((n) => navItems.find((i) => i.name === n)).filter((i) => i !== undefined),
)
/** 当前页不在底栏四项里时，「更多」亮起，告诉用户自己在哪 */
const inMore = computed(() => !TAB_NAMES.includes(route.name as string))
const awaiting = computed(() => recordStats.value.awaiting || 0)
</script>

<template>
  <nav class="tabbar" aria-label="主导航">
    <RouterLink
      v-for="t in tabs"
      :key="t.name"
      :to="{ name: t.name }"
      class="tab"
      :class="{ 'is-active': route.name === t.name }"
    >
      <span class="tab-icon">
        <component :is="t.icon" :size="20" :stroke-width="1.9" />
        <span v-if="t.name === 'tasks' && awaiting" class="tab-dot" :aria-label="`${awaiting} 项待确认`" />
      </span>
      <span class="tab-label">{{ SHORT[t.name] ?? t.label }}</span>
    </RouterLink>
    <button type="button" class="tab" :class="{ 'is-active': inMore }" @click="emit('more')">
      <span class="tab-icon"><Ellipsis :size="20" :stroke-width="1.9" /></span>
      <span class="tab-label">更多</span>
    </button>
  </nav>
</template>

<style scoped>
.tabbar {
  position: fixed;
  left: 0;
  right: 0;
  bottom: 0;
  z-index: 20;
  display: grid;
  grid-template-columns: repeat(5, 1fr);
  height: calc(var(--tabbar-h) + env(safe-area-inset-bottom));
  padding: 6px 8px env(safe-area-inset-bottom);
  background: color-mix(in oklab, var(--surface) 86%, transparent);
  backdrop-filter: saturate(180%) blur(16px);
  -webkit-backdrop-filter: saturate(180%) blur(16px);
  border-top: 1px solid var(--separator);
}

.tab {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 2px;
  border: 0;
  background: none;
  padding: 0;
  color: var(--muted);
  font: inherit;
  text-decoration: none;
  cursor: pointer;
  -webkit-tap-highlight-color: transparent;
}
.tab-icon {
  display: grid;
  place-items: center;
  width: 48px;
  height: 28px;
  border-radius: 999px;
  transition:
    background-color 150ms ease,
    transform 150ms ease;
}
.tab-icon {
  position: relative;
}
.tab-dot {
  position: absolute;
  top: 3px;
  right: 11px;
  width: 8px;
  height: 8px;
  border-radius: 999px;
  background: var(--warning);
  box-shadow: 0 0 0 2px var(--surface);
}
.tab:active .tab-icon {
  transform: scale(0.92);
}
.tab-label {
  font-size: 11px;
  line-height: 1.2;
}
.tab.is-active {
  color: var(--foreground);
}
.tab.is-active .tab-icon {
  background: var(--accent-soft);
  color: var(--accent-soft-foreground);
}
.tab.is-active .tab-label {
  font-weight: 600;
}
</style>
