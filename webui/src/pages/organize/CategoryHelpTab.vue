<script setup lang="ts">
import RefTable from './RefTable.vue'
import {
  CATEGORY_FIELDS,
  CATEGORY_RULES,
  COUNTRIES,
  LANGUAGES,
  MOVIE_GENRES,
  TV_GENRES,
} from './categoryRef'
</script>

<template>
  <div class="doc">
    <RefTable title="可用字段" :items="CATEGORY_FIELDS" />
    <RefTable title="匹配规则" :items="CATEGORY_RULES" />
    <RefTable title="电影类型 ID（genre_ids）" :items="MOVIE_GENRES" mono />
    <RefTable title="电视剧类型 ID（genre_ids）" :items="TV_GENRES" mono />
    <RefTable
      v-for="c in COUNTRIES"
      :key="c.group"
      :title="`国家代码 · ${c.group}（origin_country）`"
      :items="c.items"
      mono
    />
    <RefTable title="语言代码（original_language）" :items="LANGUAGES" mono />
  </div>
</template>

<style scoped>
.doc {
  display: flex;
  flex-direction: column;
  gap: 18px;
  padding: 18px;
  background: var(--c-bg-elevated);
  border: 1px solid var(--c-border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-card);
}
</style>
