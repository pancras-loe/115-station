<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import type { StrmTab } from './tabs'

defineProps<{ tabs: StrmTab[] }>()
const active = defineModel<string>({ required: true })

// 手机上整条横滑，切换后把选中项滚进视野（从地址栏 ?tab= 直接进来时也一样）
const bar = ref<HTMLElement | null>(null)
watch(
  active,
  async () => {
    await nextTick()
    bar.value?.querySelector('.active')?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  },
  { immediate: true },
)
</script>

<!--
  Naive 的 NTabs（其他配置页在用）是纯文字下划线，三个页签之间的关系全靠用户猜。
  这里换成带图标和一句说明的分段卡片：配置 / 全量 / 增量 是三种不同性质的操作
  （改设置、跑整库、跑增量），一眼能看出自己点进去会发生什么。
-->
<template>
  <nav ref="bar" class="tabbar" role="tablist">
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
/* HeroUI 分段控件的放大版：灰底槽 + 选中格浮起白底，每格多一个图标和一句说明 */
.tabbar {
  display: flex;
  gap: 4px;
  padding: 4px;
  border-radius: 24px;
  background: var(--default);
  overflow-x: auto;
  scrollbar-width: none;
}
.tabbar::-webkit-scrollbar {
  display: none;
}

.tab {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 12px;
  border: 0;
  border-radius: 20px;
  background: transparent;
  color: var(--muted);
  font: inherit;
  text-align: left;
  cursor: pointer;
  transition:
    background-color 150ms ease,
    box-shadow 150ms ease,
    color 150ms ease,
    transform 150ms ease;
}
@media (hover: hover) {
  .tab:hover:not(.active) {
    color: var(--foreground);
  }
}
.tab:active {
  transform: scale(0.98);
}
.tab:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
}
.tab.active {
  background: var(--segment);
  box-shadow: var(--surface-shadow);
  color: var(--foreground);
}

.tab-icon {
  flex: none;
  display: grid;
  place-items: center;
  width: 32px;
  height: 32px;
  border-radius: 999px;
  background: color-mix(in oklab, var(--surface) 60%, transparent);
  color: var(--muted);
  transition:
    background-color 150ms ease,
    color 150ms ease;
}
.tab.active .tab-icon {
  background: var(--accent-soft);
  color: var(--accent-soft-foreground);
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
  white-space: nowrap;
}
.tab-hint {
  font-size: 11.5px;
  font-weight: 400;
  line-height: 1.4;
  color: var(--muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* 窄屏：说明文字先让位；再窄就整条横向滚动——四个页签挤成两行比滚动更难点 */
@media (max-width: 960px) {
  .tab-hint {
    display: none;
  }
}
@media (max-width: 720px) {
  .tab {
    flex: none;
    padding: 6px 14px 6px 6px;
    gap: 8px;
  }
  .tab-icon {
    width: 28px;
    height: 28px;
  }
}
</style>
