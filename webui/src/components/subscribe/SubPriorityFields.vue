<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ArrowDown, ArrowUp, Plus, Trash2 } from '@lucide/vue'
import HButton from '@/components/hero/HButton.vue'
import HNumberInput from '@/components/hero/HNumberInput.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import SubCondFields from '@/components/subscribe/SubCondFields.vue'
import { condToRule, ruleToCond, type PrioMode, type SubCond, type WashRule } from '@/api/subscribe'

/**
 * 资源优先级（订阅设置的默认值与单个订阅共用，后端 subpriority.go）：
 * 基础 / 跟随洗版策略 / 自定义，比法与整理时洗版同一套，命中第 1 条规则算「达标」。
 * inherit：单个订阅多一个「跟随订阅设置」（mode 为空串）。
 * 自定义规则逐条用资源条件那套预设标签编辑（SubCondFields part=quality），存的是洗版规则的字段
 */
const props = defineProps<{ inherit?: boolean }>()
const mode = defineModel<PrioMode | ''>('mode', { required: true })
const rules = defineModel<WashRule[]>('rules', { required: true })
const limit = defineModel<number>('limit', { required: true })

const modeOptions = computed(() => [
  ...(props.inherit ? [{ label: '跟随设置', value: '' }] : []),
  { label: '基础', value: 'basic' },
  { label: '跟随洗版策略', value: 'follow' },
  { label: '自定义', value: 'custom' },
])

const modeHint = computed(() => {
  switch (mode.value) {
    case 'basic':
      return '不看规则，只按 分辨率 → 片源 → HDR → 视频编码 比（同洗版的默认画质顺序）。没有「达标」，有缺先补集数'
    case 'follow':
      return '用这部片入库时整理会用的那条洗版策略（按它会进的分类挑）：命中第 1 条算达标，达标的资源优先；都不达标先补集数、再比画质。没配洗版策略时等同基础'
    case 'custom':
      return '自己写几条，越靠上越想要；命中第 1 条算达标。同一条里再按默认画质顺序比'
  }
  return '按「发现与订阅 → 订阅设置 → 资源筛选」'
})

// 编辑用的副本：每条规则转成资源条件的形状给预设标签用，改了再转回去
const conds = ref<SubCond[]>([])
let syncing = false
watch(
  rules,
  (rs) => {
    const next = JSON.stringify(rs ?? [])
    if (next === JSON.stringify(conds.value.map(condToRule))) return
    syncing = true
    conds.value = (rs ?? []).map(ruleToCond)
    syncing = false
  },
  { immediate: true, deep: true },
)
watch(
  conds,
  (cs) => {
    if (!syncing) rules.value = cs.map(condToRule)
  },
  { deep: true },
)

function add() {
  conds.value.push(ruleToCond({}))
}
function remove(i: number) {
  conds.value.splice(i, 1)
}
function move(i: number, d: -1 | 1) {
  const j = i + d
  if (j < 0 || j >= conds.value.length) return
  const cs = [...conds.value]
  ;[cs[i], cs[j]] = [cs[j], cs[i]]
  conds.value = cs
}

const limitModel = computed({
  get: () => (limit.value > 0 ? limit.value : null),
  set: (v) => (limit.value = v ?? 0),
})
</script>

<template>
  <FieldRow label="资源优先级" :hint="modeHint">
    <HSegmented v-model="mode" :options="modeOptions" />
  </FieldRow>
  <div v-if="mode === 'custom'" class="rules">
    <div v-for="(_, i) in conds" :key="i" class="rule">
      <div class="rule-head">
        <b>第 {{ i + 1 }} 条</b>
        <span v-if="i === 0" class="muted">（达标）</span>
        <span class="spacer" />
        <HButton size="sm" variant="ghost" icon-only :disabled="i === 0" aria-label="上移" @click="move(i, -1)">
          <ArrowUp :size="14" />
        </HButton>
        <HButton size="sm" variant="ghost" icon-only :disabled="i === conds.length - 1" aria-label="下移" @click="move(i, 1)">
          <ArrowDown :size="14" />
        </HButton>
        <HButton size="sm" variant="ghost" icon-only aria-label="删除这条" @click="remove(i)">
          <Trash2 :size="14" />
        </HButton>
      </div>
      <SubCondFields v-model="conds[i]" part="quality" :legend="i === 0" />
    </div>
    <p v-if="!conds.length" class="muted empty">还没有规则，至少加一条。</p>
    <HButton size="sm" variant="secondary" @click="add"><Plus :size="14" />加一条</HButton>
  </div>
  <FieldRow
    v-if="mode !== 'basic'"
    label="只要命中前 N 条"
    hint="留空 = 不限，规则只用来排先后。设了以后标题写明不符的不要；标题没写的分享会逐个文件判，磁力标题没写的不下"
  >
    <HNumberInput v-model="limitModel" :min="1" placeholder="不限" aria-label="只要命中前 N 条" />
  </FieldRow>
</template>

<style scoped>
.rules {
  display: flex;
  flex-direction: column;
  gap: 10px;
  margin: 4px 0 8px;
}
.rule {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: var(--r-lg);
}
.rule-head {
  display: flex;
  align-items: center;
  gap: 4px;
}
.spacer {
  flex: 1;
}
.muted {
  color: var(--muted);
  font-size: 13px;
}
.empty {
  margin: 0;
}
</style>
