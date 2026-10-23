<script setup lang="ts">
/**
 * 简单模式清单里的一条分类：平时一行（目录名 + 条件摘要），点铅笔原地展开条件。
 * 条件值是 YAML 原样的逗号串，点选标签与「其他」之间怎么换算见 catSimple.ts。
 */
import { computed } from 'vue'
import { ChevronDown, ChevronUp, Pencil, Trash2 } from '@lucide/vue'
import type { CatSimpleRule } from '@/api/library'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import HTagsInput from '@/components/hero/HTagsInput.vue'
import {
  GENRE_CHIPS,
  LANG_CHIPS,
  REGION_CHIPS,
  chipOn,
  leftoverOf,
  ruleSummary,
  setLeftover,
  splitTokens,
  toggleChip,
  type CondChip,
  type Media,
  type RuleLint,
} from './catSimple'

const props = defineProps<{
  media: Media
  index: number
  total: number
  editing: boolean
  lint: RuleLint
  /** 「试一试」那部片命中的就是这条 */
  hit: boolean
}>()
const rule = defineModel<CatSimpleRule>('rule', { required: true })
const emit = defineEmits<{ up: []; down: []; remove: []; toggle: [] }>()

type ChipField = 'genre_ids' | 'origin_country' | 'original_language'

const groups = computed<{ field: ChipField; label: string; chips: CondChip[]; other: string }[]>(() => [
  { field: 'genre_ids', label: '类型', chips: GENRE_CHIPS[props.media], other: '其他类型 ID，回车添加（如 18）' },
  { field: 'origin_country', label: '地区', chips: REGION_CHIPS, other: '其他国家代码，回车添加（如 FR；!CN 表示排除）' },
  { field: 'original_language', label: '原语言', chips: LANG_CHIPS, other: '其他语言代码，回车添加（如 th）' },
])

function toggle(field: ChipField, chip: CondChip) {
  rule.value[field] = toggleChip(rule.value[field], chip)
}
const leftover = (field: ChipField, chips: CondChip[]) => leftoverOf(rule.value[field], chips)
function setRest(field: ChipField, chips: CondChip[], rest: string[]) {
  rule.value[field] = setLeftover(rule.value[field], chips, rest)
}

const prodCountries = computed({
  get: () => splitTokens(rule.value.production_countries),
  set: (v: string[]) => (rule.value.production_countries = v.join(',')),
})

const summary = computed(() => ruleSummary(rule.value, props.media))
</script>

<template>
  <li class="cr" :class="{ hit, editing, bad: !!lint.error }">
    <div class="cr-head">
      <span class="num" aria-hidden="true">{{ index + 1 }}</span>
      <HInput
        v-if="editing"
        v-model="rule.name"
        class="name-input"
        mono
        :status="lint.error ? 'error' : undefined"
        placeholder="目录名，多级用 / 分隔"
        :input-attrs="{ 'aria-label': '分类目录名' }"
      />
      <span v-else class="name">{{ rule.name || '（没填目录名）' }}</span>
      <span v-if="hit" class="hit-tag">试一试命中</span>
      <div class="ops">
        <HButton icon-only size="sm" variant="ghost" aria-label="上移" :disabled="index === 0" @click="emit('up')">
          <ChevronUp :size="16" />
        </HButton>
        <HButton icon-only size="sm" variant="ghost" aria-label="下移" :disabled="index === total - 1" @click="emit('down')">
          <ChevronDown :size="16" />
        </HButton>
        <HButton
          icon-only
          size="sm"
          :variant="editing ? 'secondary' : 'ghost'"
          :aria-label="editing ? '收起条件' : '编辑这条分类'"
          :aria-expanded="editing"
          @click="emit('toggle')"
        >
          <Pencil :size="15" />
        </HButton>
        <HButton icon-only size="sm" variant="ghost" aria-label="删除这条分类" @click="emit('remove')">
          <Trash2 :size="15" />
        </HButton>
      </div>
    </div>

    <div v-if="summary.length || lint.error || lint.warn" class="cr-tags">
      <span v-for="s in summary" :key="s" class="cond">{{ s }}</span>
      <span v-if="lint.error" class="note err">{{ lint.error }}</span>
      <span v-if="lint.warn" class="note warn">{{ lint.warn }}</span>
    </div>

    <div v-if="editing" class="cr-edit">
      <p class="tip">同一项里选了几个是「任一个」，不同项之间是「都要满足」；都不选表示不限。</p>
      <div v-for="g in groups" :key="g.field" class="grp">
        <span class="grp-label">{{ g.label }}</span>
        <div class="grp-body">
          <div class="chips" role="group" :aria-label="g.label">
            <button
              v-for="c in g.chips"
              :key="c.label"
              type="button"
              class="chip-t"
              :aria-pressed="chipOn(rule[g.field], c)"
              :title="c.codes.join(',')"
              @click="toggle(g.field, c)"
            >
              {{ c.label }}
            </button>
          </div>
          <HTagsInput
            :model-value="leftover(g.field, g.chips)"
            :placeholder="g.other"
            @update:model-value="(v: string[]) => setRest(g.field, g.chips, v)"
          />
        </div>
      </div>
      <div class="grp">
        <span class="grp-label">制片国家</span>
        <div class="grp-body">
          <HTagsInput v-model="prodCountries" placeholder="国家代码，回车添加（老电影常缺「地区」，按国家分电影时用它更稳）" />
        </div>
      </div>
      <div class="grp">
        <label class="grp-label" :for="`cy-${media}-${index}`">年份</label>
        <div class="grp-body">
          <HInput
            v-model="rule.release_year"
            class="short"
            placeholder="不限；如 2020 或 2000-2009"
            :input-attrs="{ id: `cy-${media}-${index}` }"
          />
        </div>
      </div>
      <div class="grp">
        <label class="grp-label" :for="`cr-${media}-${index}`">片名正则</label>
        <div class="grp-body">
          <HInput
            v-model="rule.custom_regex"
            mono
            placeholder="可不填；片名或原名命中就归这一类，不看上面的条件"
            :input-attrs="{ id: `cr-${media}-${index}` }"
          />
        </div>
      </div>
      <div v-if="rule.ext" class="grp">
        <label class="grp-label" :for="`ce-${media}-${index}`">文件后缀</label>
        <div class="grp-body">
          <HInput v-model="rule.ext" mono :input-attrs="{ id: `ce-${media}-${index}` }" />
          <span class="tip">后缀目前不参与匹配，清空它不影响分类结果。</span>
        </div>
      </div>
      <div class="done">
        <HButton size="sm" variant="primary" @click="emit('toggle')">完成</HButton>
      </div>
    </div>
  </li>
</template>

<style scoped>
.cr {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 8px 8px 8px 6px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  border: 1px solid transparent;
}
.cr.editing {
  border-color: var(--border);
}
.cr.hit {
  border-color: var(--accent);
  background: color-mix(in srgb, var(--accent) 8%, var(--surface-secondary));
}
.cr.bad {
  border-color: var(--danger);
}
.cr-head {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.num {
  flex: none;
  width: 22px;
  height: 22px;
  border-radius: 999px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
  color: var(--muted);
  background: var(--default);
}
.name {
  flex: 1;
  min-width: 0;
  font-family: var(--font-mono);
  font-size: 13px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.name-input {
  flex: 1;
  min-width: 0;
}
.hit-tag {
  flex: none;
  font-size: 11px;
  padding: 1px 8px;
  border-radius: 999px;
  background: var(--accent);
  color: var(--accent-foreground);
}
.ops {
  flex: none;
  display: flex;
  gap: 2px;
}
.cr-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  padding-left: 30px;
}
.cond {
  font-size: 12px;
  padding: 1px 8px;
  border-radius: 6px;
  background: var(--default);
  color: var(--foreground);
}
.note {
  font-size: 12px;
  padding: 1px 8px;
  border-radius: 6px;
}
.note.warn {
  color: var(--warning);
  background: color-mix(in srgb, var(--warning) 14%, transparent);
}
.note.err {
  color: var(--danger);
  background: color-mix(in srgb, var(--danger) 14%, transparent);
}
.cr-edit {
  display: flex;
  flex-direction: column;
  gap: 12px;
  margin: 4px 0 2px 30px;
  padding: 12px;
  border-radius: var(--r-sm);
  background: var(--surface);
}
.tip {
  margin: 0;
  font-size: 12px;
  color: var(--muted);
}
.grp {
  display: flex;
  gap: 10px;
  align-items: flex-start;
}
.grp-label {
  flex: none;
  width: 56px;
  padding-top: 5px;
  font-size: 12.5px;
  color: var(--muted);
}
.grp-body {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.chip-t {
  font: inherit;
  font-size: 13px;
  padding: 3px 12px;
  border-radius: 999px;
  border: 1px dashed var(--border);
  background: transparent;
  color: var(--muted);
  cursor: pointer;
}
.chip-t[aria-pressed='true'] {
  border-style: solid;
  border-color: var(--accent);
  color: var(--accent);
  background: var(--accent-soft, color-mix(in srgb, var(--accent) 14%, transparent));
}
.chip-t:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
}
.short {
  max-width: 240px;
}
.done {
  display: flex;
  justify-content: flex-end;
}
@media (max-width: 720px) {
  .grp {
    flex-direction: column;
    gap: 4px;
  }
  .grp-label {
    width: auto;
    padding-top: 0;
  }
  .cr-edit {
    margin-left: 0;
  }
  .cr-tags {
    padding-left: 0;
  }
}
</style>
