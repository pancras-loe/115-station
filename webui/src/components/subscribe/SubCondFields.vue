<script setup lang="ts">
import { computed } from 'vue'
import HInput from '@/components/hero/HInput.vue'
import HNumberInput from '@/components/hero/HNumberInput.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import type { SubCond } from '@/api/subscribe'

/**
 * 资源条件的一组输入（订阅设置的默认值与单个订阅的自定义共用）。
 * 语法同洗版规则：逗号分隔多个值命中任一即可，「!」开头是排除
 */
const cond = defineModel<SubCond>({ required: true })

const fields: { key: 'pix' | 'type' | 'effect' | 'video' | 'audio' | 'team'; label: string; placeholder: string }[] = [
  { key: 'pix', label: '分辨率', placeholder: '如：2160p,1080p' },
  { key: 'type', label: '质量', placeholder: '如：WEB-DL,BluRay,!REMUX' },
  { key: 'effect', label: '特效', placeholder: '如：DV,HDR 或 !DV' },
  { key: 'video', label: '视频编码', placeholder: '如：H265,x265,HEVC' },
  { key: 'audio', label: '音频编码', placeholder: '如：TrueHD,DTS,Atmos' },
  { key: 'team', label: '发布组', placeholder: '如：ADWeb,!XXX' },
]

const minGB = computed({
  get: () => (cond.value.min_gb > 0 ? cond.value.min_gb : null),
  set: (v) => (cond.value.min_gb = v ?? 0),
})
const maxGB = computed({
  get: () => (cond.value.max_gb > 0 ? cond.value.max_gb : null),
  set: (v) => (cond.value.max_gb = v ?? 0),
})
</script>

<template>
  <div class="cond">
    <FieldRow v-for="f in fields" :key="f.key" :label="f.label">
      <HInput v-model="cond[f.key]" :placeholder="f.placeholder" />
    </FieldRow>
    <FieldRow label="中文字幕" hint="资源标题或文件名写了中字 / 简繁 / 内封，或者分享里有跟着视频的字幕文件">
      <HSwitch v-model="cond.zh" aria-label="要中文字幕" />
    </FieldRow>
    <FieldRow label="单个视频大小" hint="按分享里每个视频文件判（剧集就是单集大小）；磁力只能看标题，只对电影判。留空不限">
      <div class="pair">
        <HNumberInput v-model="minGB" :min="0" :step="0.5" placeholder="不限" aria-label="最小 GB" />
        <span class="muted">到</span>
        <HNumberInput v-model="maxGB" :min="0" :step="0.5" placeholder="不限" aria-label="最大 GB" />
        <span class="muted">GB</span>
      </div>
    </FieldRow>
  </div>
</template>

<style scoped>
.cond {
  display: contents;
}
.pair {
  display: flex;
  align-items: center;
  gap: 8px;
}
.pair :deep(.number-field) {
  max-width: 140px;
}
.muted {
  color: var(--muted);
  white-space: nowrap;
}
</style>
