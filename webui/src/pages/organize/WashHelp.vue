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
        再拿新文件和库内同一部（剧集是同一集）的文件比谁更好，新版赢才替换，全部打平就保守不换。
        <strong>不填 <code>priority_level</code></strong> 时按默认画质比：分辨率 → 片源 → HDR → 视频编码，
        前面分出高下就不看后面（2160p &gt; 1080p，REMUX &gt; BluRay &gt; WEB-DL，DV &gt; HDR10+ &gt; HDR10 &gt; HDR）。
        <strong>填了</strong>就是高级设置：先比命中第几条（越靠上分越高，没命中最低），
        命中同一条再按默认画质比；这条里写了的字段最多按命中的值算，比如只写了
        <code>resource_effect: "HDR10+,HDR"</code>，DV 的资源不会因为是 DV 胜过 HDR10+。
        想在同一档里留体积小的（比如 4K 只要小体积），在那条上写 <code>size_prefer: small</code>：
        两份都命中这一条时先比体积，相差不到 5% 再比画质；<code>resource_size: "&lt;30G"</code> 则是体积条件。
        <code>old_version_target: delete</code> 将旧版移入 115 回收站；替换后清理旧 STRM 并通知 Emby。
      </p>
    </section>

    <section class="block">
      <h3 class="block-title">画质偏好（prefer）</h3>
      <pre class="sample">洗版策略:
  mode: replace
  prefer:
    max_pix: 1080p        # 最高 1080p（4K 当作不要）；不写 = 越高越好
    dv: any               # prefer 优先要 / any 无所谓 / avoid 不要
    size: small           # 画质一样时留小的；large 留大的；不写不看体积
    avoid: [hdtv, cam]    # 不要：hdtv / cam（枪版）/ dvdrip / le720（720p 及以下）
    avoid_words: [60fps]  # 文件名里有这些词的也不要
    teams: [WiKi]         # 画质一样时偏好的发布组</pre>
      <p class="note">
        这就是「简单设置」生成的写法。没有分档：依次比 不要的 → 分辨率 → 杜比视界（只在 prefer 时）→ 片源 → HDR → 视频编码 → 偏好发布组 → 体积，
        前面分出高下就停。不要的排在最后，库里没有这部片时照样入库。写了 prefer 时 priority_level 不生效。
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
