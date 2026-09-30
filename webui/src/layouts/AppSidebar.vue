<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { Pin, PinOff } from '@lucide/vue'
import { navItems } from './navItems'
import { recordStats } from '@/stores/recordStats'
import BrandMark from '@/components/BrandMark.vue'

/**
 * docked：桌面端形态。侧栏始终绝对定位在左侧占位列里（占位列宽度由 AppLayout 按 pinned 过渡），
 * 自己的宽度只在 72px（图标栏）与完整宽度之间过渡 —— 固定、取消固定、悬停浮出走的是同一条宽度动画，
 * 不会在「浮层」和「占位」两种布局之间硬切。
 * 未固定时鼠标移上去（或键盘 Tab 进来）浮出展开，盖在内容上面；固定后占位列跟着展开，内容让出位置。
 * 抽屉里（平板 / 手机「更多」）永远是完整形态，不传 docked。
 */
const props = defineProps<{ version?: string; docked?: boolean; pinned?: boolean }>()
const emit = defineEmits<{ navigate: []; 'toggle-pin': [] }>()
const route = useRoute()

// 展开状态由脚本判定而不是 CSS :hover：取消固定时鼠标正停在图钉上，
// 靠 :hover 的话侧栏会一直开着、直到鼠标移走才收，点了像没反应。
// hold = 刚取消固定，这次悬停不算数，等鼠标离开后恢复
const hovering = ref(false)
const focused = ref(false)
const hold = ref(false)
const open = computed(() => !props.docked || props.pinned || (!hold.value && (hovering.value || focused.value)))
const floating = computed(() => !!props.docked && !props.pinned && open.value)

function onLeave() {
  hovering.value = false
  hold.value = false
}
// 只认键盘带进来的焦点（:focus-visible）；鼠标点导航项 / 图钉产生的焦点不该把侧栏撑开
function onFocusIn(e: FocusEvent) {
  focused.value = (e.target as HTMLElement).matches?.(':focus-visible') ?? false
}
function onFocusOut(e: FocusEvent) {
  const next = e.relatedTarget as Node | null
  if (!next || !(e.currentTarget as HTMLElement).contains(next)) focused.value = false
}
watch(
  () => props.pinned,
  (p, was) => {
    if (was && !p) {
      hold.value = true
      focused.value = false
      ;(document.activeElement as HTMLElement | null)?.blur?.()
    }
  },
)

/** 入口上的角标：任务中心挂「待确认」条数 —— 开着人工确认时，不点进去也要看得见有活 */
const badges = computed<Record<string, number>>(() => ({ tasks: recordStats.value.awaiting || 0 }))
const showPin = computed(() => !!props.docked)
</script>

<template>
  <aside
    class="sidebar"
    :class="{ 'is-docked': docked, 'is-open': open, 'is-floating': floating }"
    @mouseenter="hovering = true"
    @mouseleave="onLeave"
    @focusin="onFocusIn"
    @focusout="onFocusOut"
  >
    <div class="brand">
      <BrandMark :size="34" class="brand-mark" />
      <div class="brand-text fade">
        <span class="brand-name">Strm<span class="brand-accent">Station</span></span>
        <span class="brand-sub">媒体库自动化</span>
      </div>
    </div>

    <nav class="nav">
      <template v-for="item in navItems" :key="item.name">
        <div v-if="item.group" class="nav-group">
          <span class="fade">{{ item.group }}</span>
        </div>
        <RouterLink
          :to="{ name: item.name }"
          class="nav-item"
          :class="{ 'is-active': route.name === item.name }"
          :aria-label="item.label"
          @click="emit('navigate')"
        >
          <span class="nav-icon-wrap">
            <component :is="item.icon" :size="18" :stroke-width="1.75" class="nav-icon" />
            <span v-if="badges[item.name]" class="nav-dot" aria-hidden="true" />
          </span>
          <span class="nav-label fade">{{ item.label }}</span>
          <span v-if="badges[item.name]" class="nav-badge" :title="`${badges[item.name]} 项待确认`">
            {{ badges[item.name] }}
          </span>
        </RouterLink>
      </template>
    </nav>

    <div class="sidebar-foot">
      <button
        v-if="showPin"
        type="button"
        class="pin-btn"
        :aria-label="pinned ? '收起为图标栏' : '固定展开侧栏'"
        :title="pinned ? '收起为图标栏' : '固定展开侧栏'"
        @click="emit('toggle-pin')"
      >
        <component :is="pinned ? PinOff : Pin" :size="15" :stroke-width="1.75" />
      </button>
      <div class="foot-row fade">
        <span class="foot-version">{{ version || 'StrmStation' }}</span>
        <a class="foot-link" href="https://t.me/+7b_HYMltYMozZTk1" target="_blank" rel="noopener">TG 交流群</a>
      </div>
    </div>
  </aside>
</template>

<style scoped>
/* 侧栏比页面底色高一档（surface），靠一条细分隔线和内容区分开：
   影院风的内容区大量是深色海报，侧栏如果和页面同底色会被吃掉边界 */
.sidebar {
  --rail-w: 72px;
  width: var(--sidebar-w);
  height: 100%;
  display: flex;
  flex-direction: column;
  background: var(--surface);
  border-right: 1px solid var(--separator);
  overflow: hidden;
}

/* ---- 桌面形态：宽度在图标栏与完整之间过渡 ---- */
.sidebar.is-docked {
  position: absolute;
  inset: 0 auto 0 0;
  width: var(--rail-w);
  transition:
    width 240ms cubic-bezier(0.2, 0.8, 0.2, 1),
    box-shadow 240ms ease;
}
.sidebar.is-docked.is-open {
  width: var(--sidebar-w);
}
/* 悬停浮出：有 120ms 的意图延迟（鼠标只是从侧栏上划过去不该弹出来），带投影表示它盖在内容上面。
   固定后投影淡出、宽度不变，占位列在下面跟着展开 */
.sidebar.is-floating {
  transition-delay: 120ms;
  box-shadow: var(--overlay-shadow), 12px 0 40px -12px rgb(0 0 0 / 0.35);
}
.is-docked .fade {
  transition: opacity 160ms ease 120ms;
}
.is-docked:not(.is-open) .fade {
  opacity: 0;
  transition: opacity 100ms ease;
}

.brand {
  display: flex;
  align-items: center;
  gap: 12px;
  height: 68px;
  padding: 0 19px;
  flex-shrink: 0;
}
.brand-mark {
  border-radius: 10px;
  box-shadow: 0 6px 16px -6px color-mix(in oklab, var(--accent) 70%, transparent);
}
/* 品牌字与导航文字都不参与收缩（flex-shrink: 0）：宽度动画时由侧栏的 overflow 裁掉，
   而不是跟着逐帧重新截断、省略号来回跳 */
.brand-text {
  display: flex;
  flex-direction: column;
  gap: 1px;
  flex-shrink: 0;
  white-space: nowrap;
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
  overflow-x: hidden;
  padding: 4px 12px 12px;
  scrollbar-width: none;
}
.nav::-webkit-scrollbar {
  display: none;
}
.nav-group {
  position: relative;
  height: 34px;
  padding: 14px 12px 0;
  font-size: 11.5px;
  font-weight: 500;
  letter-spacing: 0.04em;
  color: var(--muted);
  white-space: nowrap;
}
/* 收起时组名换成一道短横线，分组的节奏还在 */
.is-docked .nav-group::before {
  content: '';
  position: absolute;
  left: 18px;
  top: 21px;
  width: 12px;
  height: 1.5px;
  border-radius: 1px;
  background: var(--border);
  transition: opacity 120ms ease;
}
.is-docked.is-open .nav-group::before {
  opacity: 0;
}
.nav-group:first-child {
  display: none;
}

.nav-item {
  position: relative;
  display: flex;
  align-items: center;
  gap: 10px;
  height: 40px;
  padding: 0 0 0 3px;
  margin-bottom: 2px;
  border-radius: 12px;
  font-size: 13.5px;
  color: color-mix(in oklab, var(--foreground) 70%, var(--muted));
  text-decoration: none;
  white-space: nowrap;
  transition:
    background-color 150ms ease,
    color 150ms ease;
}
.nav-icon-wrap {
  position: relative;
  width: 42px;
  height: 40px;
  flex-shrink: 0;
  display: grid;
  place-items: center;
}
.nav-label {
  flex-shrink: 0;
}
@media (hover: hover) {
  .nav-item:hover {
    background: var(--default);
    color: var(--foreground);
  }
}
/* 选中：强调色的淡底 + 图标着色 + 左缘一道光条，收起成图标栏时也一眼看得出在哪一页 */
.nav-item.is-active {
  background: var(--accent-soft);
  color: var(--foreground);
  font-weight: 500;
}
.nav-item.is-active::before {
  content: '';
  position: absolute;
  left: -12px;
  top: 10px;
  bottom: 10px;
  width: 3px;
  border-radius: 0 3px 3px 0;
  background: var(--accent);
  box-shadow: 0 0 12px var(--accent);
}
.nav-item.is-active .nav-icon {
  color: var(--accent);
}
.nav-badge {
  margin-left: auto;
  margin-right: 10px;
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
  flex-shrink: 0;
}
/* 收起时图标右上角亮一个点：数字放不下，但「有活」这件事不能藏。
   点和数字胶囊是两个元素、只切透明度 —— 原来是同一个元素在「点」和「胶囊」两种布局之间切换，
   展开动画一开始它就从图标上瞬移到行尾 */
.nav-dot {
  position: absolute;
  top: 9px;
  left: 26px;
  width: 8px;
  height: 8px;
  border-radius: 999px;
  background: var(--warning);
  box-shadow: 0 0 0 2px var(--surface);
  opacity: 0;
  transition: opacity 120ms ease;
}
.is-docked:not(.is-open) .nav-dot {
  opacity: 1;
}

/* 页脚在收起 / 展开两种状态下布局完全相同：图钉固定在最左、和导航图标同一列（中心 x=36），
   版本号那一行定宽跟在后面，收起时被侧栏裁掉、只淡出。
   原来收起时把这一行 display:none、图钉改成居中，展开第一帧就整排跳位 —— 用户看到的「卡一下」 */
.sidebar-foot {
  display: flex;
  align-items: center;
  gap: 10px;
  height: 52px;
  padding: 0 0 0 22px;
  flex-shrink: 0;
  border-top: 1px solid var(--separator);
  font-size: 11.5px;
  color: var(--muted);
}
.foot-row {
  /* 定宽 = 侧栏宽 - 左内边距 - 图钉 - 间距 - 右留白；不随侧栏宽度动画伸缩，文字不会逐帧重排 */
  width: calc(var(--sidebar-w) - 22px - 28px - 10px - 20px);
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  white-space: nowrap;
}
.foot-version {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
}
.foot-link {
  flex-shrink: 0;
  color: var(--accent);
  text-decoration: none;
}
.foot-link:hover {
  text-decoration: underline;
}
.pin-btn {
  width: 28px;
  height: 28px;
  flex-shrink: 0;
  display: grid;
  place-items: center;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--muted);
  cursor: pointer;
  transition:
    background-color 150ms ease,
    color 150ms ease;
}
.pin-btn:hover {
  background: var(--default);
  color: var(--foreground);
}
</style>
