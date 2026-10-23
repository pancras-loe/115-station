<script setup lang="ts">
/**
 * 简单模式里编辑一条分类的弹窗。原来是在清单里原地展开，电影 / 剧集两栏并排时编辑区只剩 300 多像素宽，
 * 一条分类被拉得很长。改的是一份草稿：点「完成」才交回去，取消（含新建时取消）原样不动。
 * 条件值是 YAML 原样的逗号串，点选标签与「其他」之间怎么换算见 catSimple.ts。
 */
import { computed, ref, watch } from 'vue'
import type { CatSimpleRule } from '@/api/library'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import HModal from '@/components/hero/HModal.vue'
import HTagsInput from '@/components/hero/HTagsInput.vue'
import MediaTypeChip from '@/components/MediaTypeChip.vue'
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
  /** 第几条（0 起）；新建时是它将要排的位置 */
  index: number
  creating: boolean
  /** 打开时的那条；弹窗里改的是它的副本 */
  rule: CatSimpleRule
  /** 按草稿算的检查结果（由上级把草稿放回清单里算，重名要和别的条目比） */
  lintOf: (draft: CatSimpleRule) => RuleLint
}>()
const show = defineModel<boolean>('show', { required: true })
const emit = defineEmits<{ done: [CatSimpleRule] }>()

const draft = ref<CatSimpleRule>({ ...props.rule })
watch(
  () => [show.value, props.rule] as const,
  ([open]) => {
    if (open) draft.value = { ...props.rule }
  },
)

type ChipField = 'genre_ids' | 'origin_country' | 'original_language'
const groups = computed<{ field: ChipField; label: string; chips: CondChip[]; other: string }[]>(() => [
  { field: 'genre_ids', label: '类型', chips: GENRE_CHIPS[props.media], other: '其他类型 ID，回车添加（如 18）' },
  { field: 'origin_country', label: '地区', chips: REGION_CHIPS, other: '其他国家代码，回车添加（如 FR；!CN 表示排除）' },
  { field: 'original_language', label: '原语言', chips: LANG_CHIPS, other: '其他语言代码，回车添加（如 th；!ja 表示排除）' },
])
const toggle = (field: ChipField, chip: CondChip) => (draft.value[field] = toggleChip(draft.value[field], chip))
const leftover = (field: ChipField, chips: CondChip[]) => leftoverOf(draft.value[field], chips)
const setRest = (field: ChipField, chips: CondChip[], rest: string[]) =>
  (draft.value[field] = setLeftover(draft.value[field], chips, rest))

const prodCountries = computed({
  get: () => splitTokens(draft.value.production_countries),
  set: (v: string[]) => (draft.value.production_countries = v.join(',')),
})

const lint = computed(() => props.lintOf(draft.value))
const summary = computed(() => ruleSummary(draft.value, props.media))
const title = computed(() => (props.creating ? '添加分类' : '编辑分类'))
const idp = computed(() => `cat-${props.media}-${props.index}`)

function done() {
  emit('done', { ...draft.value, name: draft.value.name.trim() })
  show.value = false
}
</script>

<template>
  <HModal v-model:show="show" :title="title" width="640px" persistent :return-focus="false">
    <div class="dlg">
      <div class="meta">
        <MediaTypeChip :type="media" size="sm" />
        <span class="muted small">第 {{ index + 1 }} 条 · 从上往下找第一条符合的</span>
      </div>

      <div class="field">
        <label class="lbl" :for="`${idp}-name`">目录名</label>
        <HInput
          v-model="draft.name"
          mono
          :status="lint.error ? 'error' : undefined"
          placeholder="多级用 / 分隔，如 电影/华语电影"
          :input-attrs="{ id: `${idp}-name` }"
        />
        <p v-if="lint.error" class="note err">{{ lint.error }}</p>
        <p v-else-if="lint.warn" class="note warn">{{ lint.warn }}</p>
      </div>

      <section class="box" aria-label="条件">
        <p class="tip">同一项选几个 = 任一个；不同项之间 = 都要满足；不选 = 不限。</p>
        <div v-for="g in groups" :key="g.field" class="row">
          <span class="lbl">{{ g.label }}</span>
          <div class="chips" role="group" :aria-label="g.label">
            <button
              v-for="c in g.chips"
              :key="c.label"
              type="button"
              class="chip-t"
              :aria-pressed="chipOn(draft[g.field], c)"
              :title="c.codes.join(',')"
              @click="toggle(g.field, c)"
            >
              {{ c.label }}
            </button>
            <HTagsInput
              class="other"
              :model-value="leftover(g.field, g.chips)"
              placeholder="+ 其他"
              :title="g.other"
              :aria-label="g.other"
              @update:model-value="(v: string[]) => setRest(g.field, g.chips, v)"
            />
          </div>
        </div>
      </section>

      <section class="box more" aria-label="更多条件">
        <div class="field">
          <span class="lbl">制片国家</span>
          <HTagsInput v-model="prodCountries" placeholder="国家代码，回车添加，如 US" />
          <p class="hint">老电影常缺「地区」，按国家分电影时用它更稳。</p>
        </div>
        <div class="field">
          <label class="lbl" :for="`${idp}-year`">年份</label>
          <HInput v-model="draft.release_year" placeholder="不限；如 2020 或 2000-2009" :input-attrs="{ id: `${idp}-year` }" />
        </div>
        <div class="field wide">
          <label class="lbl" :for="`${idp}-re`">片名正则</label>
          <HInput v-model="draft.custom_regex" mono placeholder="可不填" :input-attrs="{ id: `${idp}-re` }" />
          <p class="hint">片名或原名命中就归这一类，不看上面的条件。</p>
        </div>
        <div v-if="draft.ext" class="field wide">
          <label class="lbl" :for="`${idp}-ext`">文件后缀</label>
          <HInput v-model="draft.ext" mono :input-attrs="{ id: `${idp}-ext` }" />
          <p class="hint">后缀目前不参与匹配，清空它不影响分类结果。</p>
        </div>
      </section>

      <p class="sum">
        <span class="muted small">归这一类：</span>
        <template v-if="summary.length">
          <span v-for="s in summary" :key="s" class="cond">{{ s }}</span>
        </template>
        <span v-else class="muted small">还没写条件（会把后面的全接走）</span>
      </p>
    </div>

    <template #footer>
      <HButton variant="tertiary" @click="show = false">取消</HButton>
      <HButton variant="primary" @click="done">{{ creating ? '添加' : '完成' }}</HButton>
    </template>
  </HModal>
</template>

<style scoped>
.dlg {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.meta {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: -6px;
}
.small {
  font-size: 12px;
}
.muted {
  color: var(--muted);
}
.field {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
}
.lbl {
  font-size: 12.5px;
  color: var(--muted);
}
.box {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 12px 14px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.tip {
  margin: 0;
  font-size: 12px;
  color: var(--muted);
}
.row {
  display: grid;
  grid-template-columns: 56px minmax(0, 1fr);
  gap: 10px;
  align-items: start;
}
.row > .lbl {
  padding-top: 5px;
}
.chips {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.chip-t {
  font: inherit;
  font-size: 13px;
  line-height: 20px;
  padding: 3px 12px;
  border-radius: 999px;
  border: 1px solid var(--border);
  background: var(--surface);
  color: var(--foreground);
  cursor: pointer;
  transition:
    background-color 120ms ease,
    border-color 120ms ease;
}
.chip-t:hover {
  border-color: color-mix(in srgb, var(--accent) 50%, var(--border));
}
.chip-t[aria-pressed='true'] {
  border-color: var(--accent);
  color: var(--accent);
  background: var(--accent-soft, color-mix(in srgb, var(--accent) 14%, transparent));
}
.chip-t:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
}
/* 「其他」跟在标签后面，占满这一行剩下的地方；太窄就自己换到下一行 */
.chips .other {
  flex: 1 1 110px;
  min-height: 28px;
  padding: 2px 8px;
  background: var(--surface);
}
.chips .other :deep(.h-tags-input) {
  min-width: 64px;
  height: 22px;
  font-size: 12.5px;
}
.more {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px 14px;
}
.more .wide {
  grid-column: 1 / -1;
}
.hint {
  margin: 0;
  font-size: 12px;
  color: var(--muted);
}
.note {
  margin: 0;
  font-size: 12px;
}
.note.err {
  color: var(--danger);
}
.note.warn {
  color: var(--warning);
}
.sum {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  margin: 0;
}
.cond {
  font-size: 12px;
  padding: 1px 8px;
  border-radius: 6px;
  background: var(--default);
}
@media (max-width: 639px) {
  .row {
    grid-template-columns: minmax(0, 1fr);
    gap: 6px;
  }
  .row > .lbl {
    padding-top: 0;
  }
  .more {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
