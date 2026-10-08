<script setup lang="ts">
import type { Component } from 'vue'
import { RouterLink, type RouteLocationRaw } from 'vue-router'
import { Archive, ChevronRight, Copy, FolderInput, FolderPlus, Inbox, Library } from '@lucide/vue'
import HButton from '@/components/hero/HButton.vue'
import SectionCard from '@/components/ui/SectionCard.vue'

/**
 * 五个工作目录一览。原来这一页只在「缺了」的时候冒一条黄色提示，
 * 配好了反而看不到它们各在哪、各管什么 —— 而它们互不包含这条约束（workspace.go）恰恰要一眼看全才好判断。
 * 目录本身在各自的页面改（转存在影视转存、另外三个在自动整理），这里只负责看和跳过去；
 * 媒体库目录在本页「媒体库位置」卡里改。
 */
export interface WsSlot {
  key: 'library' | 'share' | 'pending' | 'existing' | 'redundant'
  /** 已配置时的显示路径（没存路径的老配置退回 cid） */
  path: string
  configured: boolean
}

defineProps<{ slots: WsSlot[]; canCreate: boolean; creating?: boolean }>()
const emit = defineEmits<{ create: [] }>()

const META: Record<WsSlot['key'], { label: string; desc: string; icon: Component; to?: RouteLocationRaw }> = {
  library: { label: '媒体库', desc: '整理入库的目的地，同步与洗版都以它为准', icon: Library },
  share: {
    label: '转存',
    desc: '分享转存 / 离线下载落地，守望者自动接管整理',
    icon: FolderInput,
    to: { name: 'media-transfer', query: { tab: 'settings' } },
  },
  pending: { label: '待整理', desc: '手动丢进来的素材，定时整理会处理', icon: Inbox, to: { name: 'organize', query: { tab: 'basic' } } },
  existing: { label: '已存在', desc: '洗版判输或重复的文件搬到这里', icon: Copy, to: { name: 'organize', query: { tab: 'basic' } } },
  redundant: { label: '冗余', desc: '被新版本替换下来的旧文件', icon: Archive, to: { name: 'organize', query: { tab: 'basic' } } },
}
</script>

<template>
  <SectionCard title="工作目录" hint="五个目录必须互不包含；整理在它们之间搬运文件">
    <template v-if="canCreate" #extra>
      <HButton variant="secondary" size="sm" :loading="creating" @click="emit('create')">
        <template #icon><FolderPlus :size="15" /></template>
        一键创建缺少的
      </HButton>
    </template>

    <ul class="ws">
      <li v-for="s in slots" :key="s.key" class="ws-item" :class="{ 'is-missing': !s.configured }">
        <span class="ws-icon"><component :is="META[s.key].icon" :size="17" :stroke-width="1.8" /></span>
        <div class="ws-body">
          <div class="ws-head">
            <span class="ws-label">{{ META[s.key].label }}</span>
            <span v-if="!s.configured" class="ws-missing">未配置</span>
          </div>
          <div v-if="s.configured" class="ws-path" :title="s.path">{{ s.path }}</div>
          <div class="ws-desc">{{ META[s.key].desc }}</div>
        </div>
        <RouterLink v-if="META[s.key].to" :to="META[s.key].to!" class="ws-go" :aria-label="`去设置${META[s.key].label}目录`">
          {{ s.configured ? '修改' : '去设置' }}<ChevronRight :size="14" />
        </RouterLink>
        <span v-else class="ws-here">见「媒体库位置」</span>
      </li>
    </ul>
  </SectionCard>
</template>

<style scoped>
.ws {
  margin: -4px 0;
  padding: 0;
  list-style: none;
}
.ws-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 0;
  border-top: 1px solid var(--separator);
}
.ws-item:first-child {
  border-top: 0;
}
.ws-icon {
  width: 36px;
  height: 36px;
  flex-shrink: 0;
  border-radius: 11px;
  display: grid;
  place-items: center;
  background: var(--accent-soft);
  color: var(--accent);
}
.is-missing .ws-icon {
  background: var(--default);
  color: var(--muted);
}
.ws-body {
  flex: 1;
  min-width: 0;
}
.ws-head {
  display: flex;
  align-items: center;
  gap: 8px;
}
.ws-label {
  font-size: 13.5px;
  font-weight: 600;
  color: var(--foreground);
}
.ws-missing {
  padding: 0 7px;
  border-radius: 999px;
  background: var(--warning-soft);
  color: var(--warning-soft-foreground);
  font-size: 11px;
  font-weight: 500;
  line-height: 18px;
}
.ws-path {
  margin-top: 2px;
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--foreground);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ws-desc {
  margin-top: 2px;
  font-size: 12px;
  color: var(--muted);
}
.ws-go {
  display: inline-flex;
  align-items: center;
  flex-shrink: 0;
  font-size: 12.5px;
  color: var(--muted);
  text-decoration: none;
  transition: color 150ms ease;
}
.ws-go:hover {
  color: var(--accent);
}
.is-missing .ws-go {
  color: var(--accent);
  font-weight: 500;
}
.ws-here {
  flex-shrink: 0;
  font-size: 12px;
  color: var(--muted);
}
</style>
