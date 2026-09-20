<script setup lang="ts">
/**
 * 「功能介绍」弹窗的排版外壳。
 *
 * 各页的介绍只写内容（`section.block` + `h3.block-title` + `p.p` / `ul.ul` / `table.tb` / `p.note`），
 * 排版统一在这里，用 :deep 作用到 slot 里的元素——否则每个介绍组件都要抄一份同样的 90 行 CSS。
 */
</script>

<template>
  <div class="doc"><slot /></div>
</template>

<style scoped>
.doc {
  display: flex;
  flex-direction: column;
  gap: 18px;
}
.doc :deep(.block-title) {
  margin: 0 0 9px;
  padding-bottom: 6px;
  font-size: 13px;
  font-weight: 600;
  color: var(--c-primary);
  border-bottom: 2px solid var(--c-primary-soft);
}
.doc :deep(.p) {
  margin: 0 0 8px;
  font-size: 13px;
  line-height: 1.9;
  color: var(--c-text-2);
}
.doc :deep(.p:last-child) {
  margin-bottom: 0;
}
.doc :deep(.ul) {
  margin: 0;
  padding-left: 18px;
  font-size: 13px;
  line-height: 1.9;
  color: var(--c-text-2);
}
.doc :deep(.ul li + li) {
  margin-top: 5px;
}
.doc :deep(.tb) {
  width: 100%;
  border-collapse: collapse;
  font-size: 12.5px;
}
.doc :deep(.tb th),
.doc :deep(.tb td) {
  padding: 7px 10px;
  border: 1px solid var(--c-border);
  text-align: left;
  vertical-align: top;
  line-height: 1.75;
}
.doc :deep(.tb th) {
  background: var(--c-bg-raised);
  font-weight: 600;
  color: var(--c-text-1);
  white-space: nowrap;
}
.doc :deep(.tb td) {
  color: var(--c-text-2);
}
.doc :deep(.note) {
  margin: 9px 0 0;
  padding: 8px 11px;
  border-radius: var(--radius);
  background: var(--c-bg-raised);
  border: 1px solid var(--c-border);
  font-size: 12px;
  line-height: 1.85;
  color: var(--c-text-3);
}
.doc :deep(code) {
  padding: 1px 5px;
  border-radius: 4px;
  background: var(--c-bg-raised);
  font-size: 12px;
}
</style>
