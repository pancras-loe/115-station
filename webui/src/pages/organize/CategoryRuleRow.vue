<script setup lang="ts">
/**
 * 简单模式清单里的一条分类：一行目录名 + 条件摘要。点这一行或铅笔打开弹窗编辑（CategoryRuleDialog.vue）；
 * 原来在这里原地展开，两栏并排时编辑区太窄、一条分类被拉得很长。
 */
import { computed } from 'vue'
import { ChevronDown, ChevronUp, Pencil, Trash2 } from '@lucide/vue'
import type { CatSimpleRule } from '@/api/library'
import HButton from '@/components/hero/HButton.vue'
import { ruleSummary, type Media, type RuleLint } from './catSimple'

const props = defineProps<{
  media: Media
  index: number
  total: number
  rule: CatSimpleRule
  lint: RuleLint
  /** 「试一试」那部片命中的就是这条 */
  hit: boolean
}>()
const emit = defineEmits<{ up: []; down: []; remove: []; edit: [] }>()

const summary = computed(() => ruleSummary(props.rule, props.media))
</script>

<template>
  <li class="cr" :class="{ hit, bad: !!lint.error }">
    <div class="cr-head">
      <span class="num" aria-hidden="true">{{ index + 1 }}</span>
      <button type="button" class="name" :title="`编辑「${rule.name}」`" @click="emit('edit')">
        {{ rule.name || '（没填目录名）' }}
      </button>
      <span v-if="hit" class="hit-tag">试一试命中</span>
      <div class="ops">
        <HButton icon-only size="sm" variant="ghost" aria-label="上移" :disabled="index === 0" @click="emit('up')">
          <ChevronUp :size="16" />
        </HButton>
        <HButton icon-only size="sm" variant="ghost" aria-label="下移" :disabled="index === total - 1" @click="emit('down')">
          <ChevronDown :size="16" />
        </HButton>
        <HButton icon-only size="sm" variant="ghost" aria-label="编辑这条分类" @click="emit('edit')">
          <Pencil :size="15" />
        </HButton>
        <HButton icon-only size="sm" variant="ghost" aria-label="删除这条分类" @click="emit('remove')">
          <Trash2 :size="15" />
        </HButton>
      </div>
    </div>

    <div v-if="summary.length || lint.error || lint.warn" class="cr-tags">
      <span v-for="s in summary" :key="s" class="cond">{{ s }}</span>
      <span v-if="lint.error" class="note err">{{ lint.error }}</span>
      <span v-if="lint.warn" class="note warn">{{ lint.warn }}</span>
    </div>
  </li>
</template>

<style scoped>
.cr {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 8px 8px 8px 6px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  border: 1px solid transparent;
}
.cr.hit {
  border-color: var(--accent);
  background: color-mix(in srgb, var(--accent) 8%, var(--surface-secondary));
}
.cr.bad {
  border-color: var(--danger);
}
.cr-head {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.num {
  flex: none;
  width: 22px;
  height: 22px;
  border-radius: 999px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
  color: var(--muted);
  background: var(--default);
}
.name {
  flex: 1;
  min-width: 0;
  padding: 2px 0;
  border: 0;
  background: none;
  color: var(--foreground);
  text-align: left;
  cursor: pointer;
  font-family: var(--font-mono);
  font-size: 13px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.hit-tag {
  flex: none;
  font-size: 11px;
  padding: 1px 8px;
  border-radius: 999px;
  background: var(--accent);
  color: var(--accent-foreground);
}
.ops {
  flex: none;
  display: flex;
  gap: 2px;
}
.cr-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  padding-left: 30px;
}
.cond {
  font-size: 12px;
  padding: 1px 8px;
  border-radius: 6px;
  background: var(--default);
  color: var(--foreground);
}
.note {
  font-size: 12px;
  padding: 1px 8px;
  border-radius: 6px;
}
.note.warn {
  color: var(--warning);
  background: color-mix(in srgb, var(--warning) 14%, transparent);
}
.note.err {
  color: var(--danger);
  background: color-mix(in srgb, var(--danger) 14%, transparent);
}
@media (max-width: 720px) {
  .cr-tags {
    padding-left: 0;
  }
}
.name:hover {
  color: var(--accent);
}
.name:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
  border-radius: 4px;
}
</style>
