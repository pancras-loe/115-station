<script setup lang="ts">
/** 表单底部按钮条：与 FieldRow 的标签列对齐，视觉上归属同一张表单 */
</script>

<template>
  <div class="actions">
    <div class="actions-gutter" />
    <div class="actions-body"><slot /></div>
  </div>
</template>

<style scoped>
.actions {
  display: flex;
  gap: 16px;
  margin-top: 6px;
  padding-top: 14px;
  border-top: 1px solid var(--c-border);
}
.actions-gutter {
  width: 168px;
  flex-shrink: 0;
}
.actions-body {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

@media (max-width: 720px) {
  .actions-gutter {
    display: none;
  }
}
</style>
