<script setup lang="ts">
import { NAlert, NButton, NRadioButton, NRadioGroup } from 'naive-ui'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import { useSetting } from '@/composables/useSetting'
import { ORG_BASIC_DEFAULTS } from './orgBasic'

/** 补全策略与基础配置的三个目录同住 org-basic 这个 key——见 orgBasic.ts */
const { model, saving, save, load } = useSetting('org-basic', ORG_BASIC_DEFAULTS)

/**
 * 整对象覆盖存，所以保存前把库里最新的拉回来，只把本页这份 enrich 盖上去——
 * 否则会用打开本页时的旧快照覆盖掉「基础配置」页签刚存的目录。
 */
async function saveEnrich() {
  const enrich = { ...model.value.enrich }
  await load()
  model.value.enrich = enrich
  await save()
}

const ROWS = [
  {
    key: 'mode' as const,
    label: '保守度',
    tip: '保守 = 只补缺失信息；标准 = 缺失补充 + 名实冲突按探测修改（完整命名不动）；激进 = 完整命名冲突也按探测修改。',
    options: [
      { v: 'conservative', l: '保守' },
      { v: 'standard', l: '标准' },
      { v: 'aggressive', l: '激进' },
    ],
  },
  {
    key: 'missing' as const,
    label: '缺信息时',
    tip: '如 蜘蛛侠.2016.mkv 探测出 1080p：补充 = 把画质写进文件名；保留 = 保持原名。',
    options: [
      { v: 'rename', l: '补充' },
      { v: 'keep', l: '保留' },
    ],
  },
  {
    key: 'conflict_low' as const,
    label: '探测高于命名',
    tip: '文件名标 1080p 但探测实际是 2160p（发布站低标）。以探测为准 = 改成 2160p。',
    options: [
      { v: 'rename', l: '以探测为准' },
      { v: 'keep', l: '保留命名' },
    ],
  },
  {
    key: 'conflict_high' as const,
    label: '探测低于命名',
    tip: '文件名标 2160p 但探测实际是 1080p（拿 1080p 冒充 4K）。以探测为准 = 改成 1080p。',
    options: [
      { v: 'rename', l: '以探测为准' },
      { v: 'keep', l: '保留命名' },
    ],
  },
  {
    key: 'full_named' as const,
    label: '完整命名冲突',
    tip: '文件名已含来源 + 发布组（如 BluRay-HDS）但与探测不符：专业组命名通常可信，默认保留。跨 3 档极端差异始终保留并记录日志。',
    options: [
      { v: 'keep', l: '保留' },
      { v: 'rename', l: '以探测为准' },
    ],
  },
]
</script>

<template>
  <div class="stack">
    <SectionCard title="媒体补全" hint="ffprobe 探测真实画质">
      <NAlert class="note" type="info" :bordered="false">
        文件名缺分辨率 / 编码时（如 <code>蜘蛛侠.2016.mkv</code>），用 ffprobe 读取 115
        直链头部探测真实画质（只拉几 MB，不下载全文件），按下方策略规范命名。
        探测是「事实」、命名是「声明」：默认只在缺失或名实冲突时修改；片名 / 集数 / 年份 /
        来源 / 发布组等原有信息一律保留。
      </NAlert>

      <FieldRow label="媒体补全" tip="文件名缺分辨率 / 编码时自动探测规范命名。开启后新整理的文件生效。">
        <NRadioGroup v-model:value="model.enrich.enabled">
          <NRadioButton :value="true">开启</NRadioButton>
          <NRadioButton :value="false">关闭</NRadioButton>
        </NRadioGroup>
      </FieldRow>

      <FieldRow v-for="r in ROWS" :key="r.key" :label="r.label" :tip="r.tip">
        <NRadioGroup v-model:value="model.enrich[r.key]">
          <NRadioButton v-for="o in r.options" :key="o.v" :value="o.v">{{ o.l }}</NRadioButton>
        </NRadioGroup>
      </FieldRow>

      <FormActions>
        <NButton type="primary" :loading="saving" @click="saveEnrich">保存策略</NButton>
      </FormActions>
    </SectionCard>
  </div>
</template>

<style scoped>
.stack {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.note {
  margin-bottom: 12px;
}
</style>
