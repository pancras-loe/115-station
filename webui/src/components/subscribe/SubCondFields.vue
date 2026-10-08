<script setup lang="ts">
import { computed } from 'vue'
import { Check, X } from '@lucide/vue'
import HNumberInput from '@/components/hero/HNumberInput.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import HTagsInput from '@/components/hero/HTagsInput.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import type { SubCond } from '@/api/subscribe'
import { COND_PRESETS, nextState, parseCond, serializeCond, type CondKey, type PresetState } from './condPresets'

/**
 * 资源条件的一组输入（订阅设置的默认值与单个订阅的自定义共用）。
 * 每项一排预设标签：点一下「要」、再点「不要」、第三下取消；同一项里「要」的几个命中任一即可。
 * 存的还是洗版规则那种逗号串（condPresets.ts），以前手填的认不出的写法原样留着、可以删
 */
const cond = defineModel<SubCond>({ required: true })

const keys = Object.keys(COND_PRESETS) as CondKey[]
const parsed = computed(() =>
  Object.fromEntries(keys.map((k) => [k, parseCond(cond.value[k], COND_PRESETS[k].presets)])) as Record<
    CondKey,
    ReturnType<typeof parseCond>
  >,
)

function toggle(k: CondKey, i: number) {
  const p = parsed.value[k]
  const states = [...p.states]
  states[i] = nextState(states[i])
  cond.value[k] = serializeCond({ states, extra: p.extra }, COND_PRESETS[k].presets)
}
function dropExtra(k: CondKey, i: number) {
  const p = parsed.value[k]
  cond.value[k] = serializeCond({ states: p.states, extra: p.extra.filter((_, j) => j !== i) }, COND_PRESETS[k].presets)
}

const stateTitle: Record<PresetState, string> = { off: '点一下：要', want: '要（再点一下：不要）', not: '不要（再点一下：取消）' }

/** 发布组没法预设：标签输入，前面加 ! 是不要 */
const teams = computed({
  get: () =>
    cond.value.team
      .split(/[,，]/)
      .map((s) => s.trim())
      .filter(Boolean),
  set: (v: string[]) => (cond.value.team = v.join(',')),
})

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
    <p class="legend muted">
      点一下 <span class="mark want"><Check :size="12" />要</span>，再点
      <span class="mark not"><X :size="12" />不要</span>，第三下取消；同一项里「要」的几个命中任一即可，都不点 = 不限。
    </p>
    <FieldRow v-for="k in keys" :key="k" :label="COND_PRESETS[k].label">
      <div class="chips" role="group" :aria-label="COND_PRESETS[k].label">
        <button
          v-for="(p, i) in COND_PRESETS[k].presets"
          :key="p.label"
          type="button"
          class="cchip"
          :class="parsed[k].states[i]"
          :title="stateTitle[parsed[k].states[i]]"
          :aria-pressed="parsed[k].states[i] !== 'off'"
          @click="toggle(k, i)"
        >
          <Check v-if="parsed[k].states[i] === 'want'" :size="13" />
          <X v-else-if="parsed[k].states[i] === 'not'" :size="13" />
          {{ p.label }}
        </button>
        <button
          v-for="(x, i) in parsed[k].extra"
          :key="'x' + x"
          type="button"
          class="cchip extra"
          :class="x.startsWith('!') ? 'not' : 'want'"
          :title="`手填的写法「${x}」，点击删除`"
          @click="dropExtra(k, i)"
        >
          {{ x }}<X :size="12" />
        </button>
      </div>
    </FieldRow>
    <FieldRow label="发布组" hint="资源名最后一个「-」后面那段，中文资源名常常取不准，建议只用于英文命名的资源。前面加 ! 是不要">
      <HTagsInput v-model="teams" placeholder="如 ADWeb，回车添加" />
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
.legend {
  margin: 0;
  font-size: 12px;
  line-height: 1.8;
}
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.cchip,
.mark {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  border-radius: 999px;
  font-size: 13px;
  line-height: 1;
  white-space: nowrap;
}
.cchip {
  padding: 6px 11px;
  border: 1px solid var(--border);
  background: var(--default);
  color: var(--foreground);
  cursor: pointer;
  transition:
    background-color 120ms ease,
    border-color 120ms ease,
    color 120ms ease;
}
.cchip:hover {
  border-color: var(--accent);
}
.cchip:focus-visible {
  outline: none;
  box-shadow: 0 0 0 2px var(--focus);
}
.cchip.want,
.mark.want {
  border-color: var(--accent);
  background: color-mix(in oklab, var(--accent) 16%, transparent);
  color: var(--accent);
}
.cchip.not,
.mark.not {
  border-color: var(--danger);
  background: color-mix(in oklab, var(--danger) 12%, transparent);
  color: var(--danger);
}
.cchip.not {
  text-decoration: line-through;
}
.cchip.extra {
  border-style: dashed;
}
.mark {
  padding: 2px 7px;
  border: 1px solid;
  font-size: 12px;
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
.legend.muted {
  white-space: normal;
}
</style>
