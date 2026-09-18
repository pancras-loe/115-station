<script setup lang="ts">
import { ref } from 'vue'
import { NTag } from 'naive-ui'
import { ChevronRight, ExternalLink } from '@lucide/vue'

export type RowAction = 'transfer' | 'offline' | 'open'

const props = defineProps<{
  tag?: string
  tagType?: 'default' | 'info' | 'success' | 'warning' | 'error'
  title: string
  meta?: string[]
  action: RowAction
  /** 触发动作，resolve 的字符串作为成功提示展示在行内 */
  run: () => Promise<string>
}>()

const ACTION_LABEL: Record<RowAction, string> = {
  transfer: '转存',
  offline: '离线下载',
  open: '打开链接',
}

// 行内状态而不是全局 toast：一次搜索可能提交十几条，toast 会互相覆盖，
// 而且用户需要看到「哪几条已经提交过了」
const state = ref<{ tone: 'idle' | 'busy' | 'ok' | 'err'; text: string }>({ tone: 'idle', text: '' })

async function click() {
  if (state.value.tone === 'ok') {
    state.value = { tone: 'ok', text: state.value.text + '（已提交过）' }
    return
  }
  if (state.value.tone === 'busy') return
  if (props.action === 'open') {
    await props.run()
    return
  }
  state.value = { tone: 'busy', text: props.action === 'transfer' ? '转存中…' : '提交 115 离线下载中…' }
  try {
    state.value = { tone: 'ok', text: await props.run() }
  } catch (e) {
    state.value = { tone: 'err', text: e instanceof Error ? e.message : '失败' }
  }
}
</script>

<template>
  <button class="row" @click="click">
    <NTag v-if="tag" size="small" :bordered="false" :type="tagType || 'default'">{{ tag }}</NTag>

    <div class="main">
      <div class="title" :title="title">{{ title }}</div>
      <div v-if="meta?.length" class="meta">
        <span v-for="(m, i) in meta" :key="i">{{ m }}</span>
      </div>
      <div v-if="state.text" class="state" :class="state.tone">{{ state.text }}</div>
    </div>

    <div class="side" :class="{ dim: action === 'open' }">
      {{ ACTION_LABEL[action] }}
      <ExternalLink v-if="action === 'open'" :size="13" />
      <ChevronRight v-else :size="14" />
    </div>
  </button>
</template>

<style scoped>
.row {
  all: unset;
  box-sizing: border-box;
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 9px 4px;
  cursor: pointer;
  border-bottom: 1px solid var(--c-border);
  transition: background-color 0.12s;
}
.row:last-child {
  border-bottom: none;
}
.row:hover {
  background: var(--c-bg-hover);
}

.main {
  flex: 1;
  min-width: 0;
}
.title {
  font-size: 13px;
  color: var(--c-text-1);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.meta {
  margin-top: 2px;
  font-size: 11.5px;
  color: var(--c-text-3);
}
.meta span + span::before {
  content: '·';
  margin: 0 6px;
  color: var(--c-text-4);
}

.state {
  margin-top: 4px;
  font-size: 11.5px;
}
.state.busy {
  color: var(--c-text-3);
}
.state.ok {
  color: var(--c-success);
}
.state.err {
  color: var(--c-danger);
}

.side {
  flex-shrink: 0;
  display: inline-flex;
  align-items: center;
  gap: 2px;
  font-size: 12.5px;
  color: var(--c-primary);
}
.side.dim {
  color: var(--c-text-3);
}
</style>
