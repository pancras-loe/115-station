<script setup lang="ts">
/**
 * 规则大纲：把 YAML 按「实际匹配顺序」摊平成一串卡片，ID / 代码翻成中文。
 * 规则文件最容易出问题的不是语法，而是顺序——摊平之后一眼能看出谁被谁挡住了。
 */
import type { OutlineGroup } from './ruleLint'

defineProps<{ groups: OutlineGroup[]; emptyText: string }>()
const emit = defineEmits<{ jump: [number] }>()
</script>

<template>
  <div class="rule-outline">
    <p v-if="!groups.length" class="empty">{{ emptyText }}</p>

    <section v-for="g in groups" :key="`${g.title}-${g.line}`" class="group">
      <header class="group-head" @click="emit('jump', g.line)">
        <h4>{{ g.title }}</h4>
        <span v-if="g.subtitle">{{ g.subtitle }}</span>
      </header>

      <ol class="items">
        <li
          v-for="it in g.items"
          :key="it.line"
          class="item"
          :class="{ muted: it.muted }"
          @click="emit('jump', it.line)"
        >
          <span class="label">{{ it.label }}</span>
          <span v-for="t in it.tags" :key="t" class="tag">{{ t }}</span>
          <span v-if="it.muted" class="tag warn">不会生效</span>
          <span v-for="c in it.chips" :key="c" class="chip">{{ c }}</span>
          <span v-if="!it.chips.length && !it.tags.length" class="chip plain">无条件，匹配一切</span>
        </li>
        <li v-if="!g.items.length" class="item plain">（这一段还没有规则）</li>
      </ol>
    </section>
  </div>
</template>

<style scoped>
/* 不能叫 .outline：那是 Tailwind 的工具类（outline-style: solid），会画出一圈黑框 */
.rule-outline {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.empty {
  margin: 0;
  font-size: 12px;
  color: var(--c-text-4);
}

.group-head {
  display: flex;
  align-items: baseline;
  gap: 8px;
  flex-wrap: wrap;
  padding-bottom: 5px;
  border-bottom: 1px solid var(--c-border);
  cursor: pointer;
}
.group-head h4 {
  margin: 0;
  font-size: 12.5px;
  font-weight: 600;
  color: var(--c-primary);
}
.group-head span {
  font-size: 11px;
  color: var(--c-text-4);
}

.items {
  list-style: none;
  margin: 6px 0 0;
  padding: 0;
  counter-reset: rule;
}
.item {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  padding: 5px 8px;
  border-radius: var(--r-sm);
  font-size: 12px;
  color: var(--c-text-2);
  cursor: pointer;
}
.item::before {
  counter-increment: rule;
  content: counter(rule);
  flex-shrink: 0;
  width: 17px;
  text-align: center;
  font-size: 10.5px;
  color: var(--c-text-4);
}
.item:hover {
  background: var(--c-primary-soft);
}
.item.muted {
  opacity: 0.55;
  text-decoration: line-through;
}
.item.plain {
  color: var(--c-text-4);
  cursor: default;
}
.item.plain::before {
  content: '';
}

.label {
  font-weight: 600;
}
.tag {
  padding: 1px 6px;
  border-radius: 999px;
  background: var(--c-bg-hover);
  font-size: 10.5px;
  color: var(--c-text-3);
}
.tag.warn {
  background: color-mix(in srgb, var(--c-warning) 18%, transparent);
  color: var(--c-warning);
}
.chip {
  padding: 1px 7px;
  border-radius: var(--r-sm);
  background: var(--c-bg-raised);
  font-size: 11px;
  color: var(--c-text-3);
}
.chip.plain {
  background: transparent;
  color: var(--c-text-4);
}
</style>
