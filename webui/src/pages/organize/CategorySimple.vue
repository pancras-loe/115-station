<script setup lang="ts">
/**
 * 二级分类的简单模式（2026-10-23，后端 catsimple.go）。
 *
 * 和专家模式存的是同一份 YAML：保存时后端由表单生成 YAML，读的时候从 YAML 认回来。
 * 别在前端另存一份表单状态（两处各存一份就会互相覆盖，洗版的 WashTab 同样的约定）。
 * 每个媒体类型一张有顺序的清单，「其余都放这里」固定在最后、只能改目录名，
 * 所以「兜底写在中间把后面的全吞掉」从结构上就不会出现；中间没写条件的规则照样标出来。
 */
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Lock, Plus } from '@lucide/vue'
import { libraryApi } from '@/api'
import { ApiError } from '@/api/client'
import type { CatDirChange, CatSimpleForm, CatSimpleRule, SchemeDetail } from '@/api/library'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import MediaTypeChip from '@/components/MediaTypeChip.vue'
import FormActions from '@/components/ui/FormActions.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import { toastError, useFeedback } from '@/composables/useFeedback'
import CategoryRuleDialog from './CategoryRuleDialog.vue'
import CategoryRuleRow from './CategoryRuleRow.vue'
import CategoryTry from './CategoryTry.vue'
import { CAT_TEMPLATES, emptyRule, formKey, lintRules, type CatTemplate, type Media, type RuleLint } from './catSimple'

const props = defineProps<{ schemeId: number; detail: SchemeDetail }>()
const emit = defineEmits<{ saved: []; dirty: [boolean] }>()

const { message, dialog } = useFeedback()

const clone = (f: CatSimpleForm): CatSimpleForm => JSON.parse(JSON.stringify(f))
const defaultTpl = CAT_TEMPLATES.find((t) => t.key === 'default')!

const form = ref<CatSimpleForm>(clone(props.detail.simple ?? defaultTpl.build()))
const savedKey = computed(() => (props.detail.simple ? formKey(props.detail.simple) : ''))
const curKey = computed(() => formKey(form.value))
const dirty = computed(() => curKey.value !== savedKey.value)
watch(dirty, (v) => emit('dirty', v), { immediate: true })

// 上级重新读了方案（保存后）：表单换成后端认回来的那份
watch(
  () => props.detail,
  (d) => {
    form.value = clone(d.simple ?? defaultTpl.build())
    dlgShow.value = false
  },
)

// v-for 的 key：规则对象本身改来改去，用对象身份认
const keys = new WeakMap<CatSimpleRule, number>()
let keySeq = 0
const keyOf = (r: CatSimpleRule) => {
  if (!keys.has(r)) keys.set(r, ++keySeq)
  return keys.get(r)!
}
// 编辑弹窗：改的是草稿，点「完成」才放回清单；新建的取消了就不留下（CategoryRuleDialog.vue）
const dlgShow = ref(false)
const dlg = ref<{ media: Media; index: number; creating: boolean; rule: CatSimpleRule }>({
  media: 'movie', index: 0, creating: false, rule: emptyRule(),
})
function openEdit(m: Media, i: number) {
  dlg.value = { media: m, index: i, creating: false, rule: form.value[m][i] }
  dlgShow.value = true
}
/** 把草稿放回清单里那个位置再检查：重名要和别的条目比 */
function lintDraft(draft: CatSimpleRule): RuleLint {
  const { media, index, creating } = dlg.value
  const list = [...form.value[media]]
  list.splice(index, creating ? 0 : 1, draft)
  return lintRules(list, form.value.fallback[media]).rules[index]
}
function onDone(r: CatSimpleRule) {
  const { media, index, creating, rule } = dlg.value
  const list = form.value[media]
  if (creating) list.push(r)
  else {
    // 按对象身份找回原来那条，序号只作兜底
    const at = list.indexOf(rule)
    list[at < 0 ? index : at] = r
  }
}

const columns = computed(() =>
  (['movie', 'tv'] as Media[]).map((m) => {
    const list = form.value[m]
    return { media: m, list, lint: lintRules(list, form.value.fallback[m]) }
  }),
)
const hasError = computed(() => columns.value.some((c) => !!c.lint.fallback.error || c.lint.rules.some((l) => l.error)))

function move(m: Media, i: number, d: number) {
  const list = form.value[m]
  const j = i + d
  if (j < 0 || j >= list.length) return
  ;[list[i], list[j]] = [list[j], list[i]]
}
function remove(m: Media, i: number) {
  form.value[m].splice(i, 1)
}
function add(m: Media) {
  const list = form.value[m]
  dlg.value = { media: m, index: list.length, creating: true, rule: emptyRule(m === 'movie' ? '电影/新分类' : '剧集/新分类') }
  dlgShow.value = true
}

// ---- 模板 ----
async function applyTemplate(t: CatTemplate) {
  if (formKey(t.build()) === curKey.value) return
  const ok = await dialog.confirm({
    title: `套用「${t.name}」`,
    content: '电影、剧集两栏现在的分类会全部换成这个模板（不会立刻保存，保存前还能逐条改）。',
    actions: [
      { label: '取消', value: false, variant: 'tertiary' },
      { label: '套用', value: true, variant: 'primary' },
    ],
  })
  if (!ok) return
  form.value = t.build()
}

// ---- 试一试命中哪一条 ----
const hit = ref<{ media: Media; index: number } | null>(null)
const hitOf = (m: Media, i: number) => hit.value?.media === m && hit.value.index === i
const onHit = (v: { media: Media; index: number } | null) => (hit.value = v)
const fbHit = (m: Media) => hit.value?.media === m && hit.value.index < 0

// ---- 保存后的 YAML（后端生成，防抖） ----
const yamlPreview = ref('')
let timer: ReturnType<typeof setTimeout> | undefined
watch(
  curKey,
  () => {
    clearTimeout(timer)
    timer = setTimeout(async () => {
      try {
        yamlPreview.value = (await libraryApi.previewScheme({ simple: form.value })).yaml ?? ''
      } catch {
        /* 预览出错不打扰，保存时会报 */
      }
    }, 300)
  },
  { immediate: true },
)
onBeforeUnmount(() => clearTimeout(timer))

// ---- 保存 ----
const saving = ref(false)

async function save() {
  if (hasError.value) {
    message.error('有分类的目录名没填或重名了，先改好标红的那几条')
    return
  }
  if (props.detail.comments) {
    const ok = await dialog.confirm({
      title: '保存会去掉 YAML 里的注释',
      content: '这套方案在专家模式里写过注释。简单模式保存时由后端重新生成 YAML，原来的注释不会保留，分类结果不受影响。',
      actions: [
        { label: '取消', value: false, variant: 'tertiary' },
        { label: '保存', value: true, variant: 'primary' },
      ],
    })
    if (!ok) return
  }
  saving.value = true
  try {
    await persist(false)
  } finally {
    saving.value = false
  }
}

async function persist(confirmed: boolean) {
  try {
    await libraryApi.updateScheme(props.schemeId, { simple: form.value, confirm_cat_change: confirmed || undefined })
    message.success('保存成功')
    emit('saved')
  } catch (e) {
    const changes = e instanceof ApiError && e.status === 409 ? (e.data.cat_changes as CatDirChange[] | undefined) : undefined
    if (!changes?.length || confirmed) {
      toastError(e, '保存失败')
      return
    }
    if (await confirmDirChanges(changes)) await persist(true)
  }
}

async function confirmDirChanges(changes: CatDirChange[]) {
  return dialog.confirm({
    title: '有分类改了名或删掉了',
    content:
      changes.map((c) => `${c.media_type === 'tv' ? '剧集' : '电影'}「${c.name}」：里面已经有 ${c.titles} 部`).join('\n') +
      '\n\n已经入库的片留在原来的目录里，不会自动搬；之后新来的按新规则放，同一类片可能分在两个目录里。' +
      '想把已有的也挪过去，保存后到「网盘文件」对这些片目点「整理」。',
    actions: [
      { label: '取消', value: false, variant: 'tertiary' },
      { label: '保存', value: true, variant: 'primary' },
    ],
  })
}

function discard() {
  form.value = clone(props.detail.simple ?? defaultTpl.build())
}
</script>

<template>
  <div class="cs">
    <HAlert v-if="detail.empty" status="default" class="banner">
      这套方案还没有分类规则。下面先填了「按类型 + 地区」模板，挑一个合适的模板、改好后保存。
    </HAlert>

    <section class="tpls" aria-label="从模板开始">
      <div class="tpls-head">
        <h3>从模板开始</h3>
        <span class="muted">套用后还能逐条改；会替换下面两栏的全部分类</span>
      </div>
      <div class="tpl-grid">
        <button
          v-for="t in CAT_TEMPLATES"
          :key="t.key"
          type="button"
          class="tpl"
          :aria-pressed="formKey(t.build()) === curKey"
          @click="applyTemplate(t)"
        >
          <span class="tpl-name">{{ t.name }}<span v-if="formKey(t.build()) === curKey" class="cur">当前</span></span>
          <span class="muted small">{{ t.desc }}</span>
          <span class="tpl-tree">{{ t.tree }}</span>
        </button>
      </div>
    </section>

    <div class="main">
      <div class="cols">
        <SectionCard v-for="c in columns" :key="c.media">
          <template #extra>
            <HButton size="sm" variant="secondary" @click="add(c.media)"><Plus :size="14" />添加分类</HButton>
          </template>
          <div class="col-head">
            <MediaTypeChip :type="c.media" size="md" />
            <span class="muted small">{{ c.list.length }} 条分类 + 其余 · 从上往下找第一条符合的</span>
          </div>
          <ol class="list">
            <CategoryRuleRow
              v-for="(r, i) in c.list"
              :key="keyOf(r)"
              :rule="r"
              :media="c.media"
              :index="i"
              :total="c.list.length"
              :lint="c.lint.rules[i]"
              :hit="hitOf(c.media, i)"
              @up="move(c.media, i, -1)"
              @down="move(c.media, i, 1)"
              @remove="remove(c.media, i)"
              @edit="openEdit(c.media, i)"
            />
            <li class="fb" :class="{ hit: fbHit(c.media) }">
              <div class="fb-head">
                <Lock :size="15" class="lock" aria-hidden="true" />
                <span class="muted small fb-label">其余都放这里</span>
                <HInput
                  v-model="form.fallback[c.media]"
                  mono
                  class="fb-input"
                  :status="c.lint.fallback.error ? 'error' : undefined"
                  :input-attrs="{ 'aria-label': `${c.media === 'tv' ? '剧集' : '电影'}的「其余」目录名` }"
                />
                <span v-if="fbHit(c.media)" class="hit-tag">试一试命中</span>
              </div>
              <p v-if="c.lint.fallback.error" class="small fb-note err">{{ c.lint.fallback.error }}</p>
              <p v-else-if="c.lint.fallback.warn" class="small fb-note warn">{{ c.lint.fallback.warn }}</p>
              <p v-else class="muted small fb-note">上面都不符合的放这里。固定在最后，只能改目录名。</p>
            </li>
          </ol>
        </SectionCard>
      </div>

      <aside class="side">
        <CategoryTry :form="form" @hit="onHit" />
        <SectionCard title="保存后的 YAML" hint="和专家模式是同一份">
          <pre class="yaml">{{ yamlPreview }}</pre>
        </SectionCard>
      </aside>
    </div>

    <FormActions>
      <HButton variant="primary" :loading="saving" :disabled="!dirty && !detail.empty" @click="save">保存</HButton>
      <HButton variant="tertiary" :disabled="!dirty" @click="discard">放弃修改</HButton>
      <span class="muted small">
        <template v-if="detail.comments">保存时由后端重新生成 YAML，专家模式里写的注释不会保留。</template>
        改了目录名、原来的分类里已经有片的，保存前会再确认一次。
      </span>
    </FormActions>

    <CategoryRuleDialog
      v-model:show="dlgShow"
      :media="dlg.media"
      :index="dlg.index"
      :creating="dlg.creating"
      :rule="dlg.rule"
      :lint-of="lintDraft"
      @done="onDone"
    />
  </div>
</template>

<style scoped>
.cs {
  display: flex;
  flex-direction: column;
  gap: 16px;
  min-width: 0;
}
.banner {
  margin: 0;
}
.tpls {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.tpls-head {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 4px 10px;
}
.tpls-head h3 {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
}
.tpl-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(220px, 100%), 1fr));
  gap: 10px;
}
.tpl {
  display: flex;
  flex-direction: column;
  gap: 4px;
  text-align: left;
  font: inherit;
  padding: 12px 14px;
  border-radius: var(--r-lg);
  border: 1px solid var(--border);
  background: var(--surface);
  color: var(--foreground);
  cursor: pointer;
}
.tpl[aria-pressed='true'] {
  border-color: var(--accent);
  background: color-mix(in srgb, var(--accent) 8%, var(--surface));
}
.tpl:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
}
.tpl-name {
  display: flex;
  align-items: center;
  gap: 8px;
  font-weight: 600;
}
.cur {
  font-size: 11px;
  font-weight: 500;
  padding: 1px 8px;
  border-radius: 999px;
  background: var(--accent);
  color: var(--accent-foreground);
}
.tpl-tree {
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--muted);
}
.main {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(300px, 380px);
  gap: 16px;
  align-items: start;
}
.cols {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(340px, 100%), 1fr));
  gap: 16px;
  align-items: start;
  min-width: 0;
}
.side {
  display: flex;
  flex-direction: column;
  gap: 16px;
  min-width: 0;
}
@media (max-width: 1100px) {
  .main {
    grid-template-columns: minmax(0, 1fr);
  }
}
.col-head {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 10px;
}
.list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.fb {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 8px 8px 8px 6px;
  border-radius: var(--r-lg);
  border: 1px dashed var(--border);
}
.fb.hit {
  border-style: solid;
  border-color: var(--accent);
  background: color-mix(in srgb, var(--accent) 8%, transparent);
}
.fb-head {
  display: flex;
  align-items: center;
  gap: 8px;
}
.lock {
  flex: none;
  margin: 0 4px;
  color: var(--muted);
}
.fb-label {
  flex: none;
}
.fb-input {
  flex: 1;
  min-width: 0;
}
.fb-note {
  padding-left: 30px;
}
.hit-tag {
  flex: none;
  font-size: 11px;
  padding: 1px 8px;
  border-radius: 999px;
  background: var(--accent);
  color: var(--accent-foreground);
}
.yaml {
  margin: 0;
  max-height: 320px;
  overflow: auto;
  padding: 12px;
  border-radius: var(--r-sm);
  background: var(--surface-secondary);
  font-family: var(--font-mono);
  font-size: 12px;
  line-height: 1.6;
  white-space: pre;
}
.muted {
  color: var(--muted);
}
.err {
  color: var(--danger);
}
.warn {
  color: var(--warning);
}
.small {
  margin: 0;
  font-size: 12px;
}
@media (max-width: 720px) {
  .fb-note {
    padding-left: 0;
  }
  .fb-head {
    flex-wrap: wrap;
  }
}
</style>
