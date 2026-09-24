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
  margin-top: 10px;
  padding-top: 16px;
  border-top: 1px solid var(--separator);
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

/* 手机：按钮等分整行，拇指好点；三个以上自动折行 */
@media (max-width: 720px) {
  .actions-gutter {
    display: none;
  }
  .actions-body {
    width: 100%;
  }
  .actions-body > :deep(*) {
    flex: 1 1 auto;
  }
}
</style>
