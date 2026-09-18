<script setup lang="ts">
defineProps<{ text: string }>()
</script>

<template>
  <div class="empty">{{ text }}</div>
</template>

<style scoped>
.empty {
  padding: 30px 16px;
  text-align: center;
  font-size: 12.5px;
  color: var(--c-text-3);
}
</style>
