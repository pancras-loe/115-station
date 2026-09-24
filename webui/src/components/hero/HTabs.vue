<script setup lang="ts" generic="T extends string">
import { nextTick, ref, watch } from 'vue'
import { TabsIndicator, TabsList, TabsRoot, TabsTrigger } from 'reka-ui'

/**
 * 页签栏。只管「那一排按钮」，内容区由调用方按 v-model 自己 v-if——
 * 这样和原来 NTabPane 的 display-directive="if" 一样，没选中的页签不挂载、不发请求。
 *
 * 手机上十来个页签放不下：列表横向滚动，切换后把选中项滚进视野。
 */
export interface TabItem<V extends string = string> {
  value: V
  label: string
  /** 页签右侧的计数角标；0 或不传不显示 */
  count?: number
  /** 角标颜色，默认 accent */
  countTone?: 'accent' | 'warning' | 'danger'
  /** 标签右上的小圆点（「这个通道已配置」这类只要有 / 无的状态） */
  dot?: boolean
}

withDefaults(
  defineProps<{
    items: TabItem<T>[]
    /** primary：HeroUI 的分段胶囊；secondary：下划线 */
    variant?: 'primary' | 'secondary'
  }>(),
  { variant: 'primary' },
)
const model = defineModel<T>({ required: true })

const scroller = ref<HTMLElement | null>(null)
watch(
  model,
  async () => {
    await nextTick()
    const el = scroller.value?.querySelector<HTMLElement>('[data-selected="true"]')
    el?.scrollIntoView({ block: 'nearest', inline: 'nearest', behavior: 'smooth' })
  },
  { immediate: true },
)
</script>

<template>
  <TabsRoot
    v-model="model"
    class="tabs"
    :class="variant === 'secondary' && 'tabs--secondary'"
    orientation="horizontal"
  >
    <div class="tabs__list-container h-tabs-scroll">
      <div ref="scroller" class="h-tabs-scroller">
        <TabsList class="tabs__list" data-orientation="horizontal">
          <TabsIndicator class="tabs__indicator h-tabs-indicator" />
          <TabsTrigger
            v-for="it in items"
            :key="it.value"
            :value="it.value"
            class="tabs__tab h-tab"
            :data-selected="model === it.value || undefined"
          >
            <span>{{ it.label }}</span>
            <i v-if="it.dot" class="h-tab-dot" aria-label="已配置" />
            <span v-if="it.count" class="h-tab-count" :class="`tone-${it.countTone ?? 'accent'}`">
              {{ it.count > 99 ? '99+' : it.count }}
            </span>
          </TabsTrigger>
        </TabsList>
      </div>
    </div>
  </TabsRoot>
</template>

<style scoped>
.h-tabs-scroll {
  max-width: 100%;
  width: fit-content;
}
.h-tabs-scroller {
  overflow-x: auto;
  scrollbar-width: none;
  overscroll-behavior-x: contain;
  border-radius: inherit;
}
.h-tabs-scroller::-webkit-scrollbar {
  display: none;
}
/* 滑块（TabsIndicator）必须以滚动的列表本身为定位参照：否则它相对外层容器定位，
   列表一横滑，滑块就飞出视口，把整页撑出横向滚动条（手机上十个页签时必现） */
.h-tabs-scroller :deep(.tabs__list) {
  position: relative;
}
.h-tab {
  width: auto;
  gap: 6px;
  white-space: nowrap;
}
.h-tab-count {
  min-width: 18px;
  height: 18px;
  padding: 0 5px;
  border-radius: 999px;
  display: inline-grid;
  place-items: center;
  font-size: 11px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}
.h-tab-dot {
  width: 6px;
  height: 6px;
  margin-left: -2px;
  border-radius: 999px;
  background: var(--success);
}
.tone-accent {
  background: var(--accent-soft);
  color: var(--accent-soft-foreground);
}
.tone-warning {
  background: var(--warning);
  color: var(--warning-foreground);
}
.tone-danger {
  background: var(--danger);
  color: var(--danger-foreground);
}
</style>
