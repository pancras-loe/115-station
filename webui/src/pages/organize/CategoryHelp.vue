<script setup lang="ts">
/** 二级分类规则说明。纯文档，表格数据都来自 categoryRef，不另立一份 */
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
    <section class="block">
      <h3 class="block-title">怎么写</h3>
      <pre class="sample">movie:
  电影/动画电影:
    genre_ids: '16'
  电影/华语电影:
    original_language: 'zh,cn'
  电影/外语电影:   # 不写条件 = 兜底，放最后</pre>
      <p class="note">
        顶层只认 <code>movie</code> 和 <code>tv</code>；下一层是分类名，也就是 115 里的目录名（用
        <code>/</code> 可建多级，整理时不存在会自动创建）；再下一层才是条件。
        同一分类下的多个条件是「且」，一个条件里用逗号分隔的多个值是「或」。
      </p>
      <p class="note">
        分类名写什么就是什么，整理时不会再自动加一层。想要
        <code>影视/电影/动画电影/</code> 就写 <code>电影/动画电影</code>；
        想把动漫番剧、综艺、剧集平铺在媒体库根下，就在 <code>tv</code> 下并列写
        <code>动漫番剧</code> <code>综艺</code> <code>剧集</code>。
      </p>
      <p class="note">
        每类最后记得留一条不带任何条件的兜底 —— 不写兜底的话，没匹配上的片子会进
        <code>电影/未分类/</code>（或 <code>剧集/未分类/</code>）。
      </p>
    </section>

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
}

.block-title {
  margin: 0 0 9px;
  padding-bottom: 6px;
  font-size: 13px;
  font-weight: 600;
  color: var(--c-primary);
  border-bottom: 2px solid var(--c-primary-soft);
}

.sample {
  margin: 0;
  padding: 10px 12px;
  border-radius: var(--r-sm);
  background: var(--c-bg-raised);
  font-family: var(--font-mono);
  font-size: 11.5px;
  line-height: 1.7;
  color: var(--c-text-2);
  overflow-x: auto;
}

.note {
  margin: 10px 0 0;
  font-size: 11.5px;
  line-height: 1.7;
  color: var(--c-text-3);
}
.note code {
  font-family: var(--font-mono);
  color: var(--c-primary);
}
</style>
