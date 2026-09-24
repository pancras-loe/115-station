<script setup lang="ts">
/** 洗版规则说明。纯文档，表格数据都来自 categoryRef，不另立一份 */
import RefTable from './RefTable.vue'
import { WASH_FIELDS, WASH_MODES, WASH_RULES, WASH_STRATEGY_FIELDS } from './categoryRef'
</script>

<template>
  <div class="doc">
    <section class="block">
      <h3 class="block-title">怎么写</h3>
      <pre class="sample">电影洗版策略:        # 顶层键随便起名，只是标签
  mode: replace
  scope: all
  media_type: movie
  old_version_target: redundant
  priority_level:
  - resource_pix: "2160p"
    resource_type: "BluRay"
    resource_effect: "!DV"   # ! 前缀 = 排除
  - resource_pix: "1080p"</pre>
      <p class="note">
        整理时先按 <code>media_type</code> / <code>category</code> 选中第一条命中的策略，
        再拿新文件和库内同一部（剧集是同一集）的文件，沿 <code>priority_level</code>
        从上往下比：第一条能分出高下的决定胜负，新版赢才替换，全平手就保守不换。
        <code>mode: replace</code> 未填写优先级时，同一影片（剧集同一集）直接新替旧。
        <code>old_version_target: delete</code> 将旧版移入 115 回收站；替换后清理旧 STRM 并通知 Emby。
      </p>
    </section>

    <RefTable title="洗版模式（mode）" :items="WASH_MODES" mono />
    <RefTable title="策略字段" :items="WASH_STRATEGY_FIELDS" mono />
    <RefTable title="优先级字段（priority_level 每一项）" :items="WASH_FIELDS" mono />
    <RefTable title="匹配规则" :items="WASH_RULES" />
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
