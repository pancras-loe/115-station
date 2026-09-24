<script setup lang="ts">
defineProps<{ title?: string; hint?: string }>()
</script>

<template>
  <!-- HeroUI 的卡片不画描边、标题下不画分隔线：层级只靠底色与柔和阴影 -->
  <section class="card card--default section">
    <header v-if="title || $slots.extra" class="section-head">
      <div class="section-titles">
        <h2 v-if="title" class="section-title">{{ title }}</h2>
        <p v-if="hint" class="section-hint">{{ hint }}</p>
      </div>
      <div v-if="$slots.extra" class="section-extra"><slot name="extra" /></div>
    </header>
    <div class="section-body"><slot /></div>
  </section>
</template>

<style scoped>
.section {
  gap: 14px;
  padding: 20px;
  min-width: 0;
}
.section-head {
  display: flex;
  align-items: flex-start;
  gap: 12px;
}
.section-titles {
  flex: 1;
  min-width: 0;
}
.section-title {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
  line-height: 1.4;
  color: var(--foreground);
}
.section-hint {
  margin: 2px 0 0;
  font-size: 12.5px;
  color: var(--muted);
}
.section-extra {
  flex-shrink: 0;
}
.section-body {
  min-width: 0;
}

@media (max-width: 720px) {
  .section {
    padding: 16px;
    gap: 12px;
  }
}
</style>
