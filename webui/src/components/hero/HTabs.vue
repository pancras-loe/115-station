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
