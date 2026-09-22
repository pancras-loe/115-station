<script setup lang="ts">
/**
 * 规则生成器：填表 → 生成 YAML → 插到正确位置。
 *
 * 手写这两份 YAML 的三个坑，这里各堵一个：
 *   1) 类型 ID / 国家代码要翻文档 → 全部做成带中文名的下拉；
 *   2) 缩进错一格就保存失败 → YAML 由代码拼，用户不碰缩进；
 *   3) 分类规则讲顺序，写在兜底后面等于白写 → 插入点由结构决定（见 ruleEdit）。
 */
import { computed, ref, watch } from 'vue'
import {
  NButton,
  NInput,
  NModal,
  NRadioButton,
  NRadioGroup,
  NSelect,
  type SelectOption,
} from 'naive-ui'
import FieldRow from '@/components/ui/FieldRow.vue'
import {
  COUNTRIES,
  LANGUAGES,
  MOVIE_GENRES,
  TV_GENRES,
  WASH_FIELDS,
  WASH_MODES,
  WASH_VALUE_OPTIONS,
} from './categoryRef'
import {
  appendWashStrategy,
  categorySnippet,
  insertCategoryRule,
  washSnippet,
  type DraftCond,
} from './ruleEdit'

const props = defineProps<{ kind: 'category' | 'wash'; yaml: string }>()
const emit = defineEmits<{ apply: [string] }>()

const show = defineModel<boolean>('show', { required: true })

// ---------------- 二级分类 ----------------

const media = ref<'movie' | 'tv'>('movie')
const catName = ref('')
const genres = ref<string[]>([])
const countries = ref<string[]>([])
const langs = ref<string[]>([])
const regex = ref('')

const genreOptions = computed<SelectOption[]>(() =>
  (media.value === 'movie' ? MOVIE_GENRES : TV_GENRES).map(([v, l]) => ({ label: `${l}（${v}）`, value: v })),
)
const countryOptions: SelectOption[] = COUNTRIES.map(c => ({
  type: 'group',
  key: c.group,
  label: c.group,
  children: c.items.map(([v, l]) => ({ label: `${l}（${v}）`, value: v })),
}))
const langOptions: SelectOption[] = LANGUAGES.map(([v, l]) => ({ label: `${l}（${v}）`, value: v }))

interface CategoryPreset {
  label: string
  media: 'movie' | 'tv'
  name: string
  genres?: string[]
  countries?: string[]
  langs?: string[]
}

/** 常见配方：点一下把表单填好，再按自己的库改名字 */
const CATEGORY_PRESETS: CategoryPreset[] = [
  { label: '动画电影', media: 'movie', name: '电影/动画电影', genres: ['16'] },
  { label: '华语电影', media: 'movie', name: '电影/华语电影', langs: ['zh', 'cn'] },
  { label: '纪录片', media: 'movie', name: '电影/纪录片', genres: ['99'] },
  { label: '外语电影', media: 'movie', name: '电影/外语电影' },
  { label: '全放电影/（兜底）', media: 'movie', name: '电影' },
  { label: '国漫', media: 'tv', name: '电视剧/国漫', genres: ['16'], countries: ['CN', 'TW', 'HK'] },
  { label: '日番', media: 'tv', name: '电视剧/日番', genres: ['16'], countries: ['JP'] },
  { label: '国产剧', media: 'tv', name: '电视剧/国产剧', countries: ['CN', 'TW', 'HK'] },
  { label: '欧美剧', media: 'tv', name: '电视剧/欧美剧', countries: ['US', 'GB', 'FR', 'DE', 'ES', 'IT', 'NL', 'PT', 'RU'] },
  { label: '日韩剧', media: 'tv', name: '电视剧/日韩剧', countries: ['JP', 'KR', 'KP', 'TH', 'IN', 'SG'] },
  { label: '纪录剧集', media: 'tv', name: '电视剧/纪录片', genres: ['99'] },
  { label: '全放剧集/（兜底）', media: 'tv', name: '剧集' },
  { label: '动漫番剧（平铺）', media: 'tv', name: '动漫番剧', genres: ['16'] },
  { label: '综艺（平铺）', media: 'tv', name: '综艺', genres: ['10764', '10767'] },
]

const catPresets = computed(() => CATEGORY_PRESETS.filter(p => p.media === media.value))

function applyCategoryPreset(p: CategoryPreset) {
  catName.value = p.name
  genres.value = p.genres ?? []
  countries.value = p.countries ?? []
  langs.value = p.langs ?? []
  regex.value = ''
}

const catConds = computed<DraftCond[]>(() =>
  [
    { key: 'genre_ids', value: genres.value.join(',') },
    { key: 'original_language', value: langs.value.join(',') },
    { key: 'origin_country', value: countries.value.join(',') },
    { key: 'custom_regex', value: regex.value },
  ].filter(c => c.value.trim()),
)

// ---------------- 洗版 ----------------

const washName = ref('')
const washMode = ref('replace')
const washScope = ref('all')
const washMedia = ref('')
const washCategory = ref('')
const washTarget = ref('redundant')
/** 一级优先级 = 字段 → 多个取值，落到 YAML 时用逗号连起来 */
const levels = ref<Record<string, string[]>[]>([{}])

const modeOptions: SelectOption[] = WASH_MODES.map(([v, l]) => ({ label: `${v} — ${l}`, value: v }))
const scopeOptions: SelectOption[] = [
  { label: 'all — 同一影片（同一集）跨分辨率比较', value: 'all' },
  { label: 'group — 按分辨率分组各留一个', value: 'group' },
]
const mediaOptions: SelectOption[] = [
  { label: '不限（电影 + 剧集）', value: '' },
  { label: '仅电影 movie', value: 'movie' },
  { label: '仅剧集 tv', value: 'tv' },
]
const targetOptions: SelectOption[] = [
  { label: 'redundant — 旧版移到冗余目录', value: 'redundant' },
  { label: 'existing — 旧版移到已存在目录', value: 'existing' },
  { label: 'delete — 旧版移入 115 回收站', value: 'delete' },
]

const washFieldOptions: Record<string, SelectOption[]> = Object.fromEntries(
  Object.entries(WASH_VALUE_OPTIONS).map(([k, vs]) => [k, vs.map(v => ({ label: v, value: v }))]),
)

interface WashPreset {
  label: string
  levels: Record<string, string[]>[]
}

const WASH_PRESETS: WashPreset[] = [
  {
    label: '4K 优先',
    levels: [{ resource_pix: ['2160p'], resource_effect: ['!DV'] }, { resource_pix: ['1080p'] }],
  },
  {
    label: '蓝光优先',
    levels: [
      { resource_type: ['BluRay'], resource_pix: ['2160p'] },
      { resource_type: ['BluRay'], resource_pix: ['1080p'] },
      { resource_type: ['WEB-DL'], resource_pix: ['2160p'] },
      { resource_type: ['WEB-DL'], resource_pix: ['1080p'] },
    ],
  },
  {
    label: '认发布组',
    levels: [{ resource_team: ['WiKi'] }, { resource_pix: ['2160p'] }, { resource_pix: ['1080p'] }],
  },
  {
    label: '体积小优先',
    levels: [
      { resource_type: ['WEB-DL'], video_encode: ['H265'] },
      { resource_pix: ['1080p'], video_encode: ['H265'] },
      { resource_pix: ['1080p'] },
    ],
  },
  {
    label: '只要非杜比视界',
    levels: [{ resource_effect: ['!DV.HDR', '!DV'], resource_pix: ['2160p'] }, { resource_effect: ['!DV.HDR', '!DV'] }],
  },
]

function applyWashPreset(p: WashPreset) {
  levels.value = p.levels.map(l => ({ ...l }))
}

const washDraft = computed(() => ({
  name: washName.value,
  mode: washMode.value,
  scope: washScope.value,
  mediaType: washMedia.value,
  category: washCategory.value,
  oldTarget: washTarget.value,
  levels: levels.value.map(lv =>
    Object.entries(lv)
      .filter(([, vs]) => vs?.length)
      .map(([key, vs]) => ({ key, value: vs.join(',') })),
  ),
}))

// ---------------- 预览 / 落笔 ----------------

const preview = computed(() =>
  props.kind === 'category'
    ? categorySnippet({ media: media.value, name: catName.value || '分类名', conds: catConds.value })
    : washSnippet({ ...washDraft.value, name: washName.value || '策略名' }),
)

const canApply = computed(() =>
  props.kind === 'category' ? !!catName.value.trim() : !!washName.value.trim(),
)

/** 分类规则没条件就是兜底，会截断它后面的所有规则——提前讲清楚 */
const catIsFallback = computed(() => props.kind === 'category' && !catConds.value.length)

function apply() {
  if (!canApply.value) return
  const next =
    props.kind === 'category'
      ? insertCategoryRule(props.yaml, { media: media.value, name: catName.value.trim(), conds: catConds.value })
      : appendWashStrategy(props.yaml, { ...washDraft.value, name: washName.value.trim() })
  emit('apply', next)
  show.value = false
}

function resetForm() {
  catName.value = ''
  genres.value = []
  countries.value = []
  langs.value = []
  regex.value = ''
  washName.value = ''
  washMode.value = 'replace'
  washScope.value = 'all'
  washMedia.value = ''
  washCategory.value = ''
  washTarget.value = 'redundant'
  levels.value = [{}]
}

watch(show, v => {
  if (v) resetForm()
})
</script>

<template>
  <NModal
    v-model:show="show"
    preset="card"
    :title="kind === 'category' ? '添加分类规则' : '添加洗版策略'"
    style="width: min(760px, 94vw)"
  >
    <div class="form">
      <!-- 二级分类 -->
      <template v-if="kind === 'category'">
        <FieldRow label="媒体类型">
          <NRadioGroup v-model:value="media" size="small">
            <NRadioButton value="movie">电影</NRadioButton>
            <NRadioButton value="tv">剧集</NRadioButton>
          </NRadioGroup>
        </FieldRow>

        <FieldRow label="常见配方" tip="点一下填好表单，再按自己的目录名改">
          <div class="presets">
            <NButton
              v-for="p in catPresets"
              :key="p.label"
              size="tiny"
              secondary
              @click="applyCategoryPreset(p)"
            >
              {{ p.label }}
            </NButton>
          </div>
        </FieldRow>

        <FieldRow label="分类名" required tip="即 115 目录名，用 / 可建多级，如 电影/动画电影" wide>
          <NInput v-model:value="catName" placeholder="电影/动画电影" />
        </FieldRow>

        <FieldRow label="类型" tip="TMDB genre_ids，多选之间是「或」" wide>
          <NSelect
            v-model:value="genres"
            multiple
            filterable
            clearable
            :options="genreOptions"
            placeholder="不限"
          />
        </FieldRow>

        <FieldRow label="国家 / 地区" tip="origin_country，多选之间是「或」" wide>
          <NSelect
            v-model:value="countries"
            multiple
            filterable
            clearable
            :options="countryOptions"
            placeholder="不限"
          />
        </FieldRow>

        <FieldRow label="语言" tip="original_language，多选之间是「或」" wide>
          <NSelect
            v-model:value="langs"
            multiple
            filterable
            clearable
            :options="langOptions"
            placeholder="不限"
          />
        </FieldRow>

        <FieldRow label="片名正则" tip="命中片名或原名即归此类，不要求其他条件同时成立" wide>
          <NInput v-model:value="regex" placeholder="选填，如 ^(哆啦A梦|蜡笔小新)" />
        </FieldRow>
      </template>

      <!-- 洗版 -->
      <template v-else>
        <FieldRow label="策略名" required tip="YAML 顶层键，只是给人看的标签" wide>
          <NInput v-model:value="washName" placeholder="电影洗版策略" />
        </FieldRow>

        <FieldRow label="适用媒体" wide>
          <NSelect v-model:value="washMedia" :options="mediaOptions" />
        </FieldRow>

        <FieldRow label="限定分类" tip="只对某些分类生效，逗号分隔；可写全路径「电视剧/日番」或只写末级「日番」；留空表示全部" wide>
          <NInput v-model:value="washCategory" placeholder="选填，如 电影/华语电影" />
        </FieldRow>

        <FieldRow label="洗版模式" wide>
          <NSelect v-model:value="washMode" :options="modeOptions" />
        </FieldRow>

        <FieldRow label="保留范围" wide>
          <NSelect v-model:value="washScope" :options="scopeOptions" />
        </FieldRow>

        <FieldRow label="旧版去向" wide>
          <NSelect v-model:value="washTarget" :options="targetOptions" />
        </FieldRow>

        <FieldRow label="常见配方" tip="点一下填好优先级阶梯，再按自己的口味改">
          <div class="presets">
            <NButton v-for="p in WASH_PRESETS" :key="p.label" size="tiny" secondary @click="applyWashPreset(p)">
              {{ p.label }}
            </NButton>
          </div>
        </FieldRow>

        <div class="levels">
          <div class="levels-head">
            <span>优先级阶梯</span>
            <em>replace 不填时新替旧；填写后从上到下比较，同一级条件是「且」，一个条件里多选是「或」</em>
          </div>

          <div v-for="(lv, i) in levels" :key="i" class="level">
            <div class="level-head">
              <span class="level-no">第 {{ i + 1 }} 优先</span>
              <NButton size="tiny" quaternary :disabled="levels.length <= 1" @click="levels.splice(i, 1)">
                删除
              </NButton>
            </div>
            <div class="level-grid">
              <label v-for="f in WASH_FIELDS" :key="f[0]" class="level-field">
                <span>{{ f[1].split('（')[0] }}</span>
                <NSelect
                  v-model:value="lv[f[0]]"
                  multiple
                  filterable
                  tag
                  clearable
                  size="small"
                  :options="washFieldOptions[f[0]]"
                  placeholder="不限"
                />
              </label>
            </div>
          </div>

          <NButton size="tiny" dashed @click="levels.push({})">+ 再加一级</NButton>
        </div>
      </template>

      <div class="preview">
        <div class="preview-head">
          将写入的内容
          <em v-if="kind === 'category'">插在 {{ media }} 段的兜底分类之前</em>
          <em v-else>追加到规则末尾</em>
        </div>
        <pre>{{ preview }}</pre>
        <p v-if="catIsFallback" class="preview-warn">
          没有勾选任何条件 —— 这条会成为兜底分类，匹配一切，排在它后面的规则将永远轮不到。
        </p>
      </div>
    </div>

    <template #footer>
      <div class="footer">
        <NButton @click="show = false">取消</NButton>
        <NButton type="primary" :disabled="!canApply" @click="apply">写入规则</NButton>
      </div>
    </template>
  </NModal>
</template>

<style scoped>
.form {
  max-height: min(64vh, 620px);
  overflow-y: auto;
  padding-right: 4px;
}

.presets {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.levels {
  margin: 10px 0 4px;
  padding: 12px;
  border: 1px solid var(--c-border);
  border-radius: var(--radius);
}
.levels-head {
  display: flex;
  align-items: baseline;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 10px;
  font-size: 12.5px;
  font-weight: 600;
  color: var(--c-text-2);
}
.levels-head em {
  font-style: normal;
  font-weight: 400;
  font-size: 11.5px;
  color: var(--c-text-4);
}
.level {
  margin-bottom: 10px;
  padding: 10px;
  border-radius: var(--radius-sm);
  background: var(--c-bg-raised);
}
.level-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
}
.level-no {
  font-size: 12px;
  font-weight: 600;
  color: var(--c-primary);
}
.level-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 8px;
}
.level-field {
  display: flex;
  flex-direction: column;
  gap: 3px;
  font-size: 11.5px;
  color: var(--c-text-3);
}

.preview {
  margin-top: 12px;
  padding: 10px 12px;
  border: 1px solid var(--c-border);
  border-radius: var(--radius);
  background: var(--c-bg-raised);
}
.preview-head {
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin-bottom: 6px;
  font-size: 12px;
  font-weight: 600;
  color: var(--c-text-2);
}
.preview-head em {
  font-style: normal;
  font-weight: 400;
  font-size: 11px;
  color: var(--c-text-4);
}
.preview pre {
  margin: 0;
  font-family: var(--font-mono);
  font-size: 11.5px;
  line-height: 1.7;
  color: var(--c-text-1);
  white-space: pre-wrap;
  word-break: break-all;
}
.preview-warn {
  margin: 8px 0 0;
  font-size: 11.5px;
  line-height: 1.6;
  color: var(--c-warning);
}

.footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
