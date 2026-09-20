<script setup lang="ts">
import { computed, ref } from 'vue'
import { NButton, NCheckbox, NDynamicTags, NInput, NInputNumber, NModal, NSelect, NTag } from 'naive-ui'
import { Plus, Trash2 } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import RecognizeHelp from './RecognizeHelp.vue'
import { useSetting } from '@/composables/useSetting'
import {
  PREVIEW_SAMPLE,
  RULE_PRESETS,
  type RecognizeConfig,
  type ReplaceRule,
  normalizeRecognize,
  previewRules,
  re2Unsupported,
} from './recognize'

/** 默认值做成工厂：数组字段不能和「重置配置」写回的那份共用引用 */
function defaults(): RecognizeConfig {
  return { replace_rules: [], release_groups: [], min_size: 0 }
}

const { model, saving, save, reset } = useSetting<RecognizeConfig>('org-recognize', defaults(), {
  normalize: normalizeRecognize,
})

const MODES = [
  { label: '文本', value: 'text' },
  { label: '正则', value: 'regex' },
]

function addRule(rule?: ReplaceRule) {
  model.value.replace_rules.push(rule ? { ...rule } : { from: '', to: '', regex: false })
}
function removeRule(i: number) {
  model.value.replace_rules.splice(i, 1)
}
/** 上移/下移：规则是按顺序依次套用的，次序本身就是配置的一部分 */
function moveRule(i: number, delta: number) {
  const list = model.value.replace_rules
  const j = i + delta
  if (j < 0 || j >= list.length) return
  ;[list[i], list[j]] = [list[j], list[i]]
}

// ---- 常用规则 ----
const presetVisible = ref(false)
const picked = ref<string[]>([])

/** 已经在列表里的预设不再重复添加（同 from 即视为同一条） */
const existingFroms = computed(() => new Set(model.value.replace_rules.map((r) => r.from)))

function openPresets() {
  picked.value = RULE_PRESETS.filter((p) => !existingFroms.value.has(p.rule.from)).map((p) => p.key)
  presetVisible.value = true
}
function applyPresets() {
  for (const p of RULE_PRESETS) {
    if (picked.value.includes(p.key) && !existingFroms.value.has(p.rule.from)) addRule(p.rule)
  }
  presetVisible.value = false
}

// ---- 效果预览 ----
const sample = ref(PREVIEW_SAMPLE)
const preview = computed(() => previewRules(sample.value, model.value.replace_rules))

/** 前端能跑、后端 RE2 跑不了的规则：整理时会被整条跳过，得当场说 */
const re2Bad = computed(() =>
  model.value.replace_rules
    .map((r, i) => ({ r, i }))
    .filter(({ r }) => r.regex && r.from && re2Unsupported(r.from))
    .map(({ i }) => i + 1),
)

const helpVisible = ref(false)
</script>

<template>
  <SectionCard title="识别规则" hint="文件名送去识别之前的预处理与过滤">
    <template #extra>
      <NButton size="small" quaternary @click="helpVisible = true">功能介绍</NButton>
    </template>

    <FieldRow
      label="替换规则"
      tip="识别前先把文件名里的广告、水印、语种标注清掉，TMDB 才搜得中。只影响识别，不改网盘里的文件名。"
      wide
    >
      <div v-if="model.replace_rules.length" class="rules">
        <div v-for="(r, i) in model.replace_rules" :key="i" class="rule">
          <span class="idx">{{ i + 1 }}</span>
          <NSelect
            :value="r.regex ? 'regex' : 'text'"
            :options="MODES"
            size="small"
            class="mode"
            @update:value="(v: string) => (r.regex = v === 'regex')"
          />
          <NInput
            v-model:value="r.from"
            size="small"
            :placeholder="r.regex ? '正则，如 【[^】]*】' : '原文本'"
          />
          <span class="arrow">→</span>
          <NInput v-model:value="r.to" size="small" placeholder="替换为（留空 = 删掉）" />
          <div class="ops">
            <NButton size="tiny" quaternary :disabled="i === 0" title="上移" @click="moveRule(i, -1)">
              ↑
            </NButton>
            <NButton
              size="tiny"
              quaternary
              :disabled="i === model.replace_rules.length - 1"
              title="下移"
              @click="moveRule(i, 1)"
            >
              ↓
            </NButton>
            <NButton size="tiny" quaternary title="删除" @click="removeRule(i)">
              <Trash2 :size="13" />
            </NButton>
          </div>
        </div>
      </div>
      <div v-else class="empty">还没有规则。点「常用规则」挑几条现成的，或者「添加规则」自己写一条。</div>

      <div class="rule-actions">
        <NButton size="small" dashed @click="openPresets()">常用规则</NButton>
        <NButton size="small" dashed @click="addRule()">
          <template #icon><Plus :size="14" /></template>
          添加规则
        </NButton>
      </div>

      <div v-if="preview.invalid.length" class="warn">
        第 {{ preview.invalid.map((i) => i + 1).join('、') }} 条正则写错了，整理时会被跳过。
      </div>
      <div v-else-if="re2Bad.length" class="warn">
        第 {{ re2Bad.join('、') }} 条用了断言或反向引用，后端正则（Go RE2）不支持，整理时会被整条跳过。
      </div>
      <div class="row-note">规则从上往下依次套用，后一条作用在前一条的结果上。</div>
    </FieldRow>

    <FieldRow label="效果预览" tip="只是本地试算，不会动网盘里的任何文件。" wide>
      <NInput v-model:value="sample" size="small" placeholder="粘一个真实文件名试试" />
      <div class="preview">
        <div v-if="!model.replace_rules.length" class="preview-idle">没有规则，文件名原样送去识别。</div>
        <template v-else>
          <div class="preview-line">
            <NTag size="small" :bordered="false">结果</NTag>
            <code :class="{ changed: preview.result !== sample }">{{ preview.result || '（空）' }}</code>
          </div>
          <div v-if="!preview.hits.length" class="preview-idle">这个名字一条规则都没命中。</div>
          <ul v-else class="hits">
            <li v-for="h in preview.hits" :key="h.index">
              <span class="hit-idx">第 {{ h.index + 1 }} 条</span>
              <code>{{ h.rule.from }}</code>
              →
              <code>{{ h.rule.to || '（删掉）' }}</code>
            </li>
          </ul>
        </template>
      </div>
    </FieldRow>

    <FieldRow
      label="发布组"
      tip="默认只认「文件名末尾 -GROUP」；把组名填进来，名字里任何位置出现都算命中。用于 {resource_team} 变量和洗版规则。"
      hint="回车添加一个，如 WiKi、FRDS"
    >
      <NDynamicTags v-model:value="model.release_groups" size="small" />
    </FieldRow>

    <FieldRow
      label="最小视频大小"
      tip="小于这个体积的视频不识别、不入库，直接移到冗余目录。挡的是预告片、引流视频这类小文件。"
      hint="0 = 不限制；常见取值 50~200 MB"
    >
      <div class="minsize">
        <NInputNumber v-model:value="model.min_size" :min="0" :step="10" style="width: 160px">
          <template #suffix>MB</template>
        </NInputNumber>
        <NButton
          v-for="q in [0, 50, 100, 200]"
          :key="q"
          size="tiny"
          :type="model.min_size === q ? 'primary' : 'default'"
          quaternary
          @click="model.min_size = q"
        >
          {{ q === 0 ? '不限制' : `${q}MB` }}
        </NButton>
      </div>
    </FieldRow>

    <FormActions>
      <NButton type="primary" :loading="saving" @click="save()">保存配置</NButton>
      <NButton @click="reset">重置配置</NButton>
    </FormActions>
  </SectionCard>

  <NModal v-model:show="presetVisible" preset="card" title="常用规则" style="width: min(660px, 92vw)">
    <div class="presets">
      <label v-for="p in RULE_PRESETS" :key="p.key" class="preset">
        <NCheckbox
          :checked="picked.includes(p.key)"
          :disabled="existingFroms.has(p.rule.from)"
          @update:checked="
            (v: boolean) => (picked = v ? [...picked, p.key] : picked.filter((k) => k !== p.key))
          "
        />
        <div class="preset-body">
          <div class="preset-title">
            {{ p.label }}
            <NTag size="tiny" :bordered="false" :type="p.rule.regex ? 'info' : 'default'">
              {{ p.rule.regex ? '正则' : '文本' }}
            </NTag>
            <span v-if="existingFroms.has(p.rule.from)" class="preset-added">已添加</span>
          </div>
          <div class="preset-desc">{{ p.desc }}</div>
          <code class="preset-sample">{{ p.sample }}</code>
        </div>
      </label>
    </div>
    <template #footer>
      <div class="modal-actions">
        <NButton size="small" @click="presetVisible = false">取消</NButton>
        <NButton size="small" type="primary" @click="applyPresets()">添加所选</NButton>
      </div>
    </template>
  </NModal>

  <NModal
    v-model:show="helpVisible"
    preset="card"
    title="识别规则是怎么回事"
    style="width: min(860px, 92vw)"
  >
    <RecognizeHelp />
  </NModal>
</template>

<style scoped>
.rules {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.rule {
  display: flex;
  align-items: center;
  gap: 6px;
}
.idx {
  width: 16px;
  flex-shrink: 0;
  font-size: 11.5px;
  color: var(--c-text-4);
  text-align: right;
}
.mode {
  width: 78px;
  flex-shrink: 0;
}
.arrow {
  flex-shrink: 0;
  color: var(--c-text-4);
}
.ops {
  display: flex;
  flex-shrink: 0;
}
.empty {
  padding: 12px 14px;
  border: 1px dashed var(--c-border);
  border-radius: var(--radius);
  font-size: 12.5px;
  color: var(--c-text-3);
}
.rule-actions {
  display: flex;
  gap: 8px;
  margin-top: 8px;
}
.warn {
  margin-top: 8px;
  font-size: 12px;
  color: var(--c-danger);
}
.row-note {
  margin-top: 6px;
  font-size: 11.5px;
  color: var(--c-text-4);
}

/* ---- 预览 ---- */
.preview {
  margin-top: 8px;
  padding: 10px 12px;
  border: 1px solid var(--c-border);
  border-radius: var(--radius);
  background: var(--c-bg-raised);
}
.preview-idle {
  font-size: 12px;
  color: var(--c-text-4);
}
.preview-line {
  display: flex;
  align-items: baseline;
  gap: 8px;
}
.preview-line code {
  min-width: 0;
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--c-text-2);
  word-break: break-all;
}
.preview-line code.changed {
  color: var(--c-primary);
}
.hits {
  margin: 8px 0 0;
  padding-left: 16px;
  font-size: 11.5px;
  line-height: 1.9;
  color: var(--c-text-3);
}
.hits code {
  font-family: var(--font-mono);
  word-break: break-all;
}
.hit-idx {
  margin-right: 6px;
  color: var(--c-text-4);
}

.minsize {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

/* ---- 常用规则弹窗 ---- */
.presets {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.preset {
  display: flex;
  gap: 10px;
  padding: 10px 12px;
  border: 1px solid var(--c-border);
  border-radius: var(--radius);
  cursor: pointer;
}
.preset-body {
  min-width: 0;
}
.preset-title {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--c-text-1);
}
.preset-added {
  font-size: 11px;
  color: var(--c-text-4);
}
.preset-desc {
  margin-top: 2px;
  font-size: 12px;
  color: var(--c-text-3);
}
.preset-sample {
  display: block;
  margin-top: 5px;
  font-family: var(--font-mono);
  font-size: 11.5px;
  color: var(--c-text-4);
  word-break: break-all;
}
.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

@media (max-width: 720px) {
  .rule {
    flex-wrap: wrap;
  }
  .arrow {
    display: none;
  }
}
</style>
