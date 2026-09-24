<script setup lang="ts">
import type { Component } from 'vue'
import {
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuPortal,
  DropdownMenuRoot,
  DropdownMenuTrigger,
} from 'reka-ui'

/**
 * 下拉菜单（替代 NDropdown）：Reka 的 DropdownMenu 管键盘导航与定位，
 * 菜单项用 HeroUI 的 menu-item 外观。默认插槽是触发按钮。
 */
export interface MenuOption {
  key: string
  label: string
  icon?: Component
  danger?: boolean
}

defineProps<{ options: MenuOption[]; align?: 'start' | 'center' | 'end' }>()
const emit = defineEmits<{ select: [string] }>()
</script>

<template>
  <DropdownMenuRoot>
    <DropdownMenuTrigger as-child><slot /></DropdownMenuTrigger>
    <DropdownMenuPortal>
      <DropdownMenuContent
        class="popover h-popover h-menu"
        :align="align ?? 'end'"
        :side-offset="6"
        :collision-padding="12"
      >
        <DropdownMenuItem
          v-for="o in options"
          :key="o.key"
          class="menu-item h-menu-item"
          :class="o.danger ? 'menu-item--danger' : 'menu-item--default'"
          @select="emit('select', o.key)"
        >
          <component :is="o.icon" v-if="o.icon" :size="16" />
          <span data-slot="label">{{ o.label }}</span>
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenuPortal>
  </DropdownMenuRoot>
</template>

<style>
.h-menu {
  min-width: 160px;
  padding: 6px;
  transform-origin: var(--reka-dropdown-menu-content-transform-origin);
}
.h-menu-item {
  font-size: 13.5px;
  color: var(--foreground);
}
.h-menu-item[data-highlighted] {
  background: var(--default);
}
.h-menu-item.menu-item--danger {
  color: var(--danger);
}
</style>
