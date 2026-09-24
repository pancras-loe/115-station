<script setup lang="ts">
import type { StrmTab } from './tabs'

defineProps<{ tabs: StrmTab[] }>()
const active = defineModel<string>({ required: true })
</script>

<!--
  Naive 的 NTabs（其他配置页在用）是纯文字下划线，三个页签之间的关系全靠用户猜。
  这里换成带图标和一句说明的分段卡片：配置 / 全量 / 增量 是三种不同性质的操作
  （改设置、跑整库、跑增量），一眼能看出自己点进去会发生什么。
-->
<template>
  <nav class="tabbar" role="tablist">
    <button
      v-for="t in tabs"
      :key="t.key"
      class="tab"
      :class="{ active: active === t.key }"
      role="tab"
      :aria-selected="active === t.key"
      @click="active = t.key"
    >
      <span class="tab-icon"><component :is="t.icon" :size="17" /></span>
      <span class="tab-text">
        <span class="tab-label">{{ t.label }}</span>
        <span class="tab-hint">{{ t.hint }}</span>
      </span>
    </button>
  </nav>
</template>

<style scoped>
.tabbar {
  display: flex;
  gap: 8px;
  padding: 6px;
  border-radius: var(--r-lg);
  background: var(--c-bg-elevated);
  border: 1px solid var(--c-border);
  box-shadow: var(--shadow-card);
}

.tab {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 13px;
  border: 1px solid transparent;
  border-radius: var(--radius);
  background: transparent;
  color: var(--c-text-2);
  font: inherit;
  text-align: left;
  cursor: pointer;
  transition:
    background 0.15s,
    border-color 0.15s,
    color 0.15s;
}
.tab:hover:not(.active) {
  background: var(--c-bg-hover);
}
.tab.active {
  background: var(--c-primary-soft);
  border-color: var(--c-primary-border);
  color: var(--c-primary);
}

.tab-icon {
  flex: none;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border-radius: var(--r-sm);
  background: var(--c-bg-raised);
  border: 1px solid var(--c-border);
  color: var(--c-text-3);
  transition:
    background 0.15s,
    color 0.15s,
    border-color 0.15s;
}
.tab.active .tab-icon {
  background: var(--c-primary);
  border-color: var(--c-primary);
  color: #fff;
}

.tab-text {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 1px;
}
.tab-label {
  font-size: 13.5px;
  font-weight: 600;
  line-height: 1.3;
}
.tab-hint {
  font-size: 11.5px;
  line-height: 1.4;
  color: var(--c-text-3);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* 窄屏：说明文字先让位，再让整条横向滚动——三个页签挤成两行比滚动更难点 */
@media (max-width: 820px) {
  .tab-hint {
    display: none;
  }
}
@media (max-width: 560px) {
  .tabbar {
    overflow-x: auto;
  }
  .tab {
    flex: none;
  }
}
</style>
