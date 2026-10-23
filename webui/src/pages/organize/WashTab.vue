<script setup lang="ts">
/**
 * 洗版策略页签（2026-10-23，设计见 docs/115-station-notes/WASH-SIMPLIFY-PLAN.md）。
 *
 * 「简单设置」和「专家模式」存的是同一份 YAML（后端 washapi.go）：简单设置保存时由后端生成 YAML，
 * 读的时候从 YAML 认回来；认不回来（按分类分开、priority_level 分档…）就只能在专家模式里改。
 * 别在前端另存一份表单状态：两处各存一份就会互相覆盖。
 */
import { computed, onMounted, ref } from 'vue'
import { organizeApi } from '@/api'
import type { WashPrefer, WashSimpleForm } from '@/api/organize'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import HTagsInput from '@/components/hero/HTagsInput.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import { toastError, useFeedback } from '@/composables/useFeedback'
import WashPreview from './WashPreview.vue'
import YamlRuleTab from './YamlRuleTab.vue'

const { message, dialog } = useFeedback()

const view = ref<'simple' | 'expert'>('simple')
const loading = ref(true)
const saving = ref(false)
/** 已保存的那份能不能用简单设置显示 */
const representable = ref(true)
const savedEmpty = ref(false)
const form = ref<WashSimpleForm | null>(null)
let savedJSON = ''
const editing = ref<'movie' | 'tv'>('movie')
const yamlDraft = ref('')

const clone = (f: WashSimpleForm): WashSimpleForm => JSON.parse(JSON.stringify(f))

async function read() {
  loading.value = true
  try {
    const d = await organizeApi.getWash()
    representable.value = !!d.simple
    savedEmpty.value = !(d.config ?? '').trim()
    form.value = clone(d.simple ?? d.default_simple)
    savedJSON = d.simple ? JSON.stringify(d.simple) : ''
    yamlDraft.value = d.config ?? ''
    if (!d.simple && !savedEmpty.value) view.value = 'expert'
  } catch (e) {
    toastError(e, '读取洗版策略失败')
  } finally {
    loading.value = false
  }
}
onMounted(read)

const dirty = computed(() => !!form.value && JSON.stringify(form.value) !== savedJSON)

/** 正在改的那一套偏好：不分开时就是共用的 movie */
const pref = computed<WashPrefer | null>(() => {
  const f = form.value
  if (!f) return null
  return f.split && editing.value === 'tv' ? f.tv : f.movie
})

// 「越高越好」「不看体积」存的是空串；单选组的值别用空串（Reka 会当成没选），这里换成具名值
const pixModel = computed({
  get: () => (pref.value?.max_pix ? '1080p' : 'max'),
  set: (v: string) => pref.value && (pref.value.max_pix = v === '1080p' ? '1080p' : ''),
})
const sizeModel = computed({
  get: () => pref.value?.size || 'any',
  set: (v: string) => pref.value && (pref.value.size = v === 'small' || v === 'large' ? v : ''),
})

function onSplit(on: boolean) {
  const f = form.value
  if (!f) return
  f.split = on
  // 刚分开时剧集从电影那套抄一份，改起来从同一个起点出发
  if (on) f.tv = JSON.parse(JSON.stringify(f.movie))
  editing.value = 'movie'
}

const AVOIDS: { key: string; label: string }[] = [
  { key: 'hdtv', label: 'HDTV' },
  { key: 'cam', label: '枪版（TC / CAM）' },
  { key: 'dvdrip', label: 'DVDRip' },
  { key: 'le720', label: '720p 及以下' },
]
function toggleAvoid(key: string) {
  const p = pref.value
  if (!p) return
  p.avoid = p.avoid.includes(key) ? p.avoid.filter((k) => k !== key) : [...p.avoid, key]
}

const modeOptions = [
  { label: '换成更好的那份', value: 'replace' as const },
  { label: '两份都留着', value: 'coexist' as const },
  { label: '不要新的', value: 'skip' as const },
]
const pixOptions = [
  { label: '越高越好', value: 'max' },
  { label: '最高 1080p', value: '1080p' },
]
const dvOptions = [
  { label: '优先要', value: 'prefer' as const },
  { label: '无所谓', value: 'any' as const },
  { label: '不要', value: 'avoid' as const },
]
const sizeOptions = [
  { label: '留体积小的', value: 'small' },
  { label: '不看体积', value: 'any' },
  { label: '留体积大的', value: 'large' },
]
const targetOptions = [
  { label: '移到冗余目录', value: 'redundant' as const },
  { label: '移入 115 回收站', value: 'delete' as const },
]
const editOptions = [
  { label: '电影', value: 'movie' as const },
  { label: '剧集', value: 'tv' as const },
]

async function saveSimple() {
  if (!form.value) return
  if (!representable.value && !savedEmpty.value) {
    const ok = await dialog.confirm({
      title: '覆盖专家模式的写法？',
      content: '现在保存的洗版策略是在专家模式里写的（按分类分开或用了分档规则），简单设置表示不了。保存后会被这套简单设置替换，原来的 YAML 不再保留。',
      actions: [
        { label: '取消', value: false, variant: 'tertiary' },
        { label: '替换并保存', value: true, variant: 'danger' },
      ],
    })
    if (!ok) return
  }
  saving.value = true
  try {
    await organizeApi.saveWashSimple(form.value)
    message.success('保存成功')
    await read()
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}

async function switchView(v: 'simple' | 'expert') {
  if (v === view.value) return
  if (view.value === 'simple' && dirty.value) {
    const ok = await dialog.confirm({
      title: '简单设置还没保存',
      content: '切到专家模式看到的是已保存的那份，这里改的会丢掉。',
      actions: [
        { label: '留在这里', value: false, variant: 'tertiary' },
        { label: '丢掉并切换', value: true, variant: 'primary' },
      ],
    })
    if (!ok) return
  }
  view.value = v
  await read() // 两边看的都是已保存的那份
  view.value = v
}

/** 专家模式保存后再认一次：改成了简单设置能表示的写法，切回来就能接着用表单 */
async function persistYaml(yaml: string) {
  await organizeApi.saveWash(yaml)
  const d = await organizeApi.getWash().catch(() => null)
  if (d) {
    representable.value = !!d.simple
    savedEmpty.value = !(d.config ?? '').trim()
  }
}
</script>

<template>
  <div class="wash">
    <div class="wash-head">
      <HSegmented
        :model-value="view"
        :options="[
          { label: '简单设置', value: 'simple' },
          { label: '专家模式（YAML）', value: 'expert' },
        ]"
        aria-label="洗版策略的编辑方式"
        @update:model-value="(v) => switchView(v as 'simple' | 'expert')"
      />
      <span class="head-note">两边是同一份策略，整理、订阅、找资源的排名都按它。</span>
    </div>

    <template v-if="view === 'simple'">
      <HAlert v-if="!loading && !representable && !savedEmpty" status="warning" class="banner">
        现在用的洗版策略是在专家模式里写的，简单设置表示不了。下面是默认的简单设置，保存会替换掉原来那份；不想替换就到专家模式里改。
      </HAlert>
      <HAlert v-else-if="!loading && savedEmpty" status="default" class="banner">
        现在没有洗版策略：不比画质，同一份文件已在库里就移「已存在」，其余照常入库。保存下面的设置就开始洗版。
      </HAlert>

      <div class="grid">
        <SectionCard v-if="form && pref" title="洗版策略" hint="库里已经有这部片（剧集是这一集）时，又来了一份怎么办">
          <FieldRow label="又来了一份" stacked>
            <HSegmented v-model="form.mode" :options="modeOptions" aria-label="又来了一份怎么办" />
            <label v-if="form.mode === 'coexist'" class="sub-opt">
              <HSwitch v-model="form.group" size="sm" /> 每个分辨率各留一份（同分辨率的还是只留更好的）
            </label>
          </FieldRow>

          <div class="split-bar">
            <label class="split-switch">
              <HSwitch :model-value="form.split" size="sm" @update:model-value="onSplit" /> 剧集单独设置
            </label>
            <HSegmented v-if="form.split" v-model="editing" :options="editOptions" size="sm" aria-label="正在设置" />
            <span v-else class="muted">下面几项电影、剧集共用</span>
          </div>

          <FieldRow label="分辨率" stacked hint="选「最高 1080p」时 4K 当作不要：电视或网速撑不住 4K 的选这个。">
            <HSegmented v-model="pixModel" :options="pixOptions" aria-label="分辨率" />
          </FieldRow>
          <FieldRow label="杜比视界（DV）" stacked hint="播放器不支持 DV 时画面会发紫发绿，这种情况选「不要」。优先要：同分辨率下有 DV 的胜过没 DV 的，片源也比不过它。">
            <HSegmented v-model="pref.dv" :options="dvOptions" aria-label="杜比视界" />
          </FieldRow>
          <FieldRow label="画质差不多时" stacked hint="分辨率、片源、HDR、编码都一样时才看体积，相差不到 5% 算一样大。">
            <HSegmented v-model="sizeModel" :options="sizeOptions" aria-label="体积" />
          </FieldRow>
          <FieldRow label="不要的资源" stacked hint="不要的排在最后：库里没有这部片时照样入库（有总比没有好），之后来了任何别的版本都会换掉它。">
            <div class="chips">
              <button
                v-for="a in AVOIDS"
                :key="a.key"
                type="button"
                class="chip-t"
                :aria-pressed="pref.avoid.includes(a.key)"
                @click="toggleAvoid(a.key)"
              >
                {{ a.label }}
              </button>
            </div>
            <HTagsInput v-model="pref.avoid_words" placeholder="其他关键词，回车添加（如 60fps）" class="mt" />
          </FieldRow>
          <FieldRow label="偏好的发布组" stacked hint="画质都一样时，偏好的发布组胜出。可不填。">
            <HTagsInput v-model="pref.teams" placeholder="发布组，回车添加（如 WiKi）" />
          </FieldRow>
          <FieldRow label="被换下来的旧版" stacked>
            <HSegmented v-model="form.target" :options="targetOptions" aria-label="旧版去向" />
          </FieldRow>

          <FormActions>
            <HButton variant="primary" :loading="saving" :disabled="!dirty && representable" @click="saveSimple">保存</HButton>
            <HButton variant="tertiary" :disabled="!dirty" @click="read">放弃修改</HButton>
          </FormActions>
        </SectionCard>
        <div v-else class="skeleton" />

        <WashPreview v-if="form" :form="form" />
      </div>
    </template>

    <template v-else>
      <YamlRuleTab
        kind="wash"
        title="洗版策略（YAML）"
        hint="按分类分开设、写分档规则时用"
        note="能力与原来一样：按 media_type / category 从上往下取第一条命中的策略。写 prefer 就是简单设置那一套；写 priority_level 是分档规则，一条都没命中算最差，下面的体检会指出常见的反直觉结果。"
        :load="organizeApi.getWash"
        :persist="persistYaml"
        @change="yamlDraft = $event"
      />
      <WashPreview :yaml="yamlDraft" class="expert-preview" />
    </template>
  </div>
</template>

<style scoped>
.wash {
  display: flex;
  flex-direction: column;
  gap: 16px;
  min-width: 0;
}
.wash-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 14px;
}
.head-note,
.muted {
  font-size: 12.5px;
  color: var(--muted);
}
.grid {
  display: grid;
  grid-template-columns: minmax(0, 1.1fr) minmax(0, 1fr);
  gap: 16px;
  align-items: start;
}
@media (max-width: 1100px) {
  .grid {
    grid-template-columns: minmax(0, 1fr);
  }
}
.split-bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px 12px;
  margin: 6px 0;
  padding: 8px 12px;
  border-radius: var(--r-sm);
  background: var(--surface-secondary);
}
.split-switch,
.sub-opt {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  cursor: pointer;
}
.sub-opt {
  margin-top: 8px;
}
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
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
  border-color: var(--danger);
  color: var(--danger);
  background: var(--danger-soft);
}
.chip-t[aria-pressed='true']::before {
  content: '✕ ';
}
.chip-t:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
}
.mt {
  margin-top: 8px;
}
.skeleton {
  height: 520px;
  border-radius: var(--r-card);
  background: var(--surface-secondary);
}
.wash > .expert-preview {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(360px, 100%), 1fr));
  align-items: start;
}
.banner {
  margin: 0;
}
</style>
