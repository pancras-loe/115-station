<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import HColorInput from '@/components/hero/HColorInput.vue'
import HSlider from '@/components/hero/HSlider.vue'
import HButton from '@/components/hero/HButton.vue'
import HSpinner from '@/components/hero/HSpinner.vue'
import HTabs from '@/components/hero/HTabs.vue'
import HInput from '@/components/hero/HInput.vue'
import HModal from '@/components/hero/HModal.vue'
import HNumberInput from '@/components/hero/HNumberInput.vue'
import HPopconfirm from '@/components/hero/HPopconfirm.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSelect from '@/components/hero/HSelect.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import { ImageUp, RefreshCw } from '@lucide/vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import CronField from '@/components/ui/CronField.vue'
import { pluginsApi } from '@/api'
import type { CoverGenConfig } from '@/api/plugins'
import { toastError, useFeedback } from '@/composables/useFeedback'

const show = defineModel<boolean>('show', { required: true })
const { message } = useFeedback()

const DEFAULTS: CoverGenConfig = {
  enabled: true,
  cron: '0 0 * * *',
  style: 'editorial_c',
  strategy: 'added',
  include: '',
  blacklist: '',
  titles: '',
  resolution: '720p',
  poster_count: 6,
  background: 'auto',
  custom_color: '#263445',
  blur: 36,
  color_ratio: 0.72,
  use_primary: true,
}

const form = ref<CoverGenConfig>({ ...DEFAULTS })
const tab = ref('style')
const thumbsEl = ref<HTMLElement | null>(null)
const saving = ref(false)

const STYLES = [
  { v: 'editorial_a', label: 'A · 电影档案馆', desc: '宋体标题 · 阶梯海报' },
  { v: 'editorial_b', label: 'B · 美术馆画册', desc: '浅色海报底纹 · 四宫格' },
  { v: 'editorial_c', label: 'C · 流媒体主视觉', desc: '四海报拼贴' },
  { v: 'editorial_d', label: 'D · 主海报标题栏', desc: '单张主视觉 · 深蓝标题' },
  { v: 'editorial_e', label: 'E · 胶片序列', desc: '五格画面 · 胶片齿孔' },
  { v: 'editorial_f', label: 'F · 倾斜海报墙', desc: '错位海报墙 · 左侧标题' },
  { v: 'editorial_g', label: 'G · 拍立得', desc: '散落相片 · 模糊背景' },
  { v: 'editorial_h', label: 'H · 大字海报', desc: '巨型标题压图 · 竖排英文' },
  { v: 'editorial_i', label: 'I · 封面流', desc: '居中放大 · 地面倒影' },
]

const STRATEGIES = [
  { label: '按加入日期，取最新的', value: 'added' },
  { label: '按发行日期，取最新的', value: 'release' },
  { label: '按标题排序，取前面的', value: 'title' },
  { label: '按评分，取最高的', value: 'rating' },
]

const BACKGROUNDS = [
  { label: '按媒体库稳定配色', value: 'auto' },
  { label: '提取主海报颜色', value: 'poster' },
  { label: '使用自定义颜色', value: 'custom' },
]

const SWATCHES = ['#263445', '#3a2d4f', '#1f4a45', '#5a2b36', '#6b4a24', '#2b3a67', '#111418']

// ============ 预览 ============
// 缩略图用后端合成的占位海报画，打开弹窗即有；「真实海报」按需开，按当前未保存的配置出图。
const samples = ref<Record<string, string>>({})
const samplesLoading = ref(false)
const liveOn = ref(false)
const liveImage = ref('')
const liveLib = ref<string | null>(null)
const liveLibs = ref<string[]>([])
const liveLoading = ref(false)
const liveErr = ref('')

// 滑块拖动、连续改色会连发请求；只认最后一次的响应，旧的晚到也丢掉。
let sampleSeq = 0
let liveSeq = 0

async function loadSamples() {
  const seq = ++sampleSeq
  samplesLoading.value = true
  try {
    const d = await pluginsApi.coverGenSample({ config: form.value, live: false, library: liveLib.value ?? '' })
    if (seq === sampleSeq) samples.value = d.samples ?? {}
  } catch (e) {
    if (seq === sampleSeq) toastError(e, '预览生成失败')
  } finally {
    if (seq === sampleSeq) samplesLoading.value = false
  }
}

async function loadLive() {
  const seq = ++liveSeq
  liveLoading.value = true
  liveErr.value = ''
  try {
    const d = await pluginsApi.coverGenSample({ config: form.value, live: true, library: liveLib.value ?? '' })
    if (seq !== liveSeq) return
    liveImage.value = d.image ?? ''
    liveLibs.value = d.libraries ?? []
    liveLib.value = d.library ?? liveLib.value
  } catch (e) {
    if (seq !== liveSeq) return
    liveImage.value = ''
    liveErr.value = e instanceof Error ? e.message : '预览失败'
  } finally {
    if (seq === liveSeq) liveLoading.value = false
  }
}

function toggleLive() {
  liveOn.value = !liveOn.value
  if (liveOn.value) void loadLive()
}

let timer: ReturnType<typeof setTimeout> | undefined
function schedule(fn: () => void) {
  clearTimeout(timer)
  timer = setTimeout(fn, 350)
}

// 影响画面的字段：缩略图与真实预览都要重画（选中的库名也是缩略图上的标题）
watch(
  () => [form.value.background, form.value.custom_color, form.value.color_ratio, form.value.blur, form.value.titles, liveLib.value],
  () => {
    if (!show.value) return
    schedule(() => {
      void loadSamples()
      if (liveOn.value) void loadLive()
    })
  },
)
// 只影响真实预览的字段：换样式（缩略图五张都有了）、换取图范围
watch(
  () => [form.value.style, form.value.strategy, form.value.poster_count, form.value.include, form.value.blacklist],
  () => {
    if (show.value && liveOn.value) schedule(() => void loadLive())
  },
)

const stageSrc = computed(() => {
  if (liveOn.value) return liveImage.value
  return samples.value[form.value.style] ?? ''
})
const stageBusy = computed(() => (liveOn.value ? liveLoading.value : samplesLoading.value && !stageSrc.value))
const showBlur = computed(() => form.value.style === 'editorial_c')
// A、F–I 的底色、压暗和点缀色都跟随「背景」配置，B–E 是固定配色。
const showBackground = computed(() =>
  ['editorial_a', 'editorial_f', 'editorial_g', 'editorial_h', 'editorial_i'].includes(form.value.style),
)

// ============ 已生成 ============
const covers = ref<{ name: string; time?: string }[]>([])
const coverStamp = ref(Date.now())

async function loadCovers() {
  try {
    covers.value = (await pluginsApi.coverGenList()).data ?? []
  } catch {
    covers.value = []
  }
  coverStamp.value = Date.now()
}

async function clean() {
  try {
    const d = await pluginsApi.cleanCoverGen()
    message.success(d.message || '缓存已清理')
    await loadCovers()
  } catch (e) {
    toastError(e, '清理失败')
  }
}

// ============ 打开 / 保存 ============
watch(show, async (v) => {
  if (!v) return
  tab.value = 'style'
  liveOn.value = false
  liveImage.value = ''
  liveErr.value = ''
  try {
    const d = await pluginsApi.coverGenConfig()
    const c = d.data ?? {}
    form.value = { ...DEFAULTS, ...Object.fromEntries(Object.entries(c).filter(([, x]) => x !== null && x !== '')) }
  } catch {
    form.value = { ...DEFAULTS }
  }
  void loadSamples()
  void loadCovers()
  // 已选样式可能排在列表后面，打开时把它滚进可视区。
  await nextTick()
  thumbsEl.value?.querySelector('.thumb.on')?.scrollIntoView({ block: 'nearest' })
})

async function save() {
  saving.value = true
  try {
    await pluginsApi.saveCoverGen({ ...form.value, cron: form.value.cron.trim() })
    message.success('配置已保存')
    show.value = false
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}

defineExpose({ loadCovers })
</script>

<template>
  <HModal v-model:show="show" title="媒体库海报" class="cg-modal" width="920px">
    <div class="h-tabs-page">
<HTabs v-model="tab" :items="[{ value: 'style', label: '封面样式' }, { value: 'title', label: '标题与范围' }, { value: 'run', label: '生成与定时' }, { value: 'gallery', label: '已生成', count: covers.length }]" />
<template v-if="tab === 'style'">
        <!-- 预览固定在左边、样式列表在右边独立滚动：之前列表排在预览下方，
             往下翻着挑样式时预览已经滚出视野，选完还得再翻回去看效果。 -->
        <div class="pane pane-style">
          <div class="studio">
            <div class="stage">
              <img v-if="stageSrc" :src="stageSrc" alt="封面预览" />
              <div v-else-if="liveErr" class="stage-msg err">{{ liveErr }}</div>
              <div v-if="stageBusy" class="stage-mask"><HSpinner size="sm" /></div>
              <span class="stage-tag">
                <template v-if="liveOn">真实海报{{ liveLib ? ` · ${liveLib}` : '' }}</template>
                <template v-else>示意海报</template>
              </span>
            </div>
            <div class="stage-bar">
              <HButton size="sm" :variant="liveOn ? 'secondary' : 'tertiary'" @click="toggleLive">
                <template #icon><ImageUp :size="14" /></template>
                {{ liveOn ? '切回示意图' : '用真实海报预览' }}
              </HButton>
              <HSelect v-if="liveOn && liveLibs.length" v-model="liveLib" class="lib-select" :options="liveLibs.map((x) => ({ label: x, value: x }))" />
              <HButton variant="ghost" size="sm" v-if="liveOn" :loading="liveLoading" @click="loadLive">
                <template #icon><RefreshCw :size="14" /></template>
              </HButton>
              <span class="bar-hint">预览不保存、不推送 Emby</span>
            </div>

            <div v-if="showBackground || showBlur" class="knobs">
              <div v-if="showBackground" class="knob">
                <div class="knob-label">强调色取色</div>
                <HSelect v-model="form.background" :options="BACKGROUNDS" />
              </div>
              <div v-if="showBackground" class="knob">
                <div class="knob-label">
                  强调色浓度 <span class="knob-val">{{ Math.round(form.color_ratio * 100) }}%</span>
                </div>
                <HSlider v-model="form.color_ratio" :min="0" :max="1" :step="0.01" aria-label="取色比例" />
              </div>
              <div v-if="showBackground && form.background === 'custom'" class="knob">
                <div class="knob-label">自定义颜色</div>
                <HColorInput v-model="form.custom_color" :swatches="SWATCHES" />
              </div>
              <div v-if="showBlur" class="knob">
                <div class="knob-label">
                  遮罩浓度 <span class="knob-val">{{ form.blur }}</span>
                </div>
                <HSlider v-model="form.blur" :min="0" :max="95" :step="1" aria-label="遮罩浓度" />
                <div class="knob-hint">越大海报越暗、标题越清楚</div>
              </div>
            </div>
          </div>

          <div ref="thumbsEl" class="thumbs" role="listbox" aria-label="封面样式">
            <button
              v-for="s in STYLES"
              :key="s.v"
              type="button"
              role="option"
              class="thumb"
              :class="{ on: form.style === s.v }"
              :aria-selected="form.style === s.v"
              @click="form.style = s.v"
            >
              <span class="thumb-img">
                <img v-if="samples[s.v]" :src="samples[s.v]" :alt="s.label" />
              </span>
              <span class="thumb-cap"><b>{{ s.label }}</b><span>{{ s.desc }}</span></span>
            </button>
          </div>
        </div>
</template>
<template v-if="tab === 'title'">
        <div class="pane">
          <FieldRow label="标题映射" wide hint="每行：媒体库名=中文标题|英文副标题。没写的库用库名，英文按库名自动推断。">
            <HInput v-model="form.titles" placeholder="电影=电影|MOVIES&#10;华语剧集=国产剧|CHINESE DRAMA" :rows="6" />
          </FieldRow>
          <FieldRow label="仅生成这些库" wide hint="一行一个；留空表示全部媒体库。">
            <HInput v-model="form.include" :rows="3" />
          </FieldRow>
          <FieldRow label="排除这些库" wide hint="一行一个 Emby 库名；未配置 Emby 时填写本地分类名。">
            <HInput v-model="form.blacklist" :rows="3" />
          </FieldRow>
          <p v-if="liveLibs.length" class="muted libs">当前会生成：{{ liveLibs.join('、') }}</p>
        </div>
</template>
<template v-if="tab === 'run'">
        <div class="pane">
          <FieldRow label="启用定时生成" tip="关闭后仍可在插件卡片上手动生成。">
            <HSwitch v-model="form.enabled" />
          </FieldRow>
          <FieldRow label="执行计划" tip="例：0 0 * * * = 每天 0 点。到点重新生成全部封面并推送 Emby。">
            <CronField v-model="form.cron" placeholder="0 0 * * *" />
          </FieldRow>
          <FieldRow label="海报选取策略">
            <HSelect v-model="form.strategy" :options="STRATEGIES" />
          </FieldRow>
          <FieldRow label="取图数量" tip="每个媒体库按选取策略取 1–12 张海报；A 用前 3 张，B/C 用前 4 张，D 用首张，E 用前 5 张。">
            <HNumberInput v-model="form.poster_count" :min="1" :max="12" />
          </FieldRow>
          <FieldRow label="输出分辨率">
            <HSegmented v-model="form.resolution" size="sm" :options="[{ label: '480p', value: '480p' }, { label: '720p', value: '720p' }, { label: '1080p', value: '1080p' }]" />
          </FieldRow>
        </div>
</template>
<template v-if="tab === 'gallery'">
        <div class="pane">
          <div class="gallery-head">
            <span class="muted">本地缓存的最近一次生成结果，点击在新标签打开原图。</span>
            <HButton variant="ghost" size="sm" @click="loadCovers">
              <template #icon><RefreshCw :size="14" /></template>
              刷新
            </HButton>
            <HPopconfirm @confirm="clean" danger :disabled="!covers.length">
<HButton variant="danger-soft" size="sm" :disabled="!covers.length">清理缓存</HButton>
<template #content>只删除本地生成缓存，不会删除 Emby 当前海报。继续？</template>
</HPopconfirm>
          </div>
          <!-- 点缩略图在新标签打开原图（替代 NImage 的灯箱预览：手机上新标签页能双指缩放，比灯箱顺手） -->
          <template v-if="covers.length">
            <div class="gallery">
              <figure v-for="c in covers" :key="c.name" class="cover">
                <a :href="`${pluginsApi.coverPreviewUrl(c.name)}&s=${coverStamp}`" target="_blank" rel="noopener">
                  <img
                    :src="`${pluginsApi.coverPreviewUrl(c.name)}&s=${coverStamp}`"
                    :alt="c.name"
                    loading="lazy"
                    class="cover-img"
                  />
                </a>
                <figcaption>{{ c.name }}<span>{{ c.time }}</span></figcaption>
              </figure>
            </div>
          </template>
          <p v-else class="muted empty">还没有生成过封面，保存配置后在插件卡片上点「立即生成」。</p>
        </div>
</template>
</div>

    <template #footer>
      <div class="foot">
        <HButton variant="tertiary" @click="show = false">取消</HButton>
        <HButton variant="primary" :loading="saving" @click="save">保存</HButton>
      </div>
    </template>
  </HModal>
</template>

<style scoped>
/* 各页签等高：切页签时弹窗不跳；内容超出时只在页签内部滚 */
.pane {
  height: min(512px, calc(100vh - 240px));
  overflow-y: auto;
  padding: 4px 2px 2px;
}

.pane-style {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 236px;
  gap: 16px;
  overflow: hidden;
}
.studio {
  display: flex;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
  overflow-y: auto;
}
.stage {
  position: relative;
  aspect-ratio: 16 / 9;
  object-fit: cover;
  border-radius: 12px;
  border-radius: var(--radius);
  overflow: hidden;
  background: var(--c-bg-raised);
  border: 1px solid var(--c-border);
}
.stage img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}
.stage-mask {
  position: absolute;
  inset: 0;
  display: grid;
  place-items: center;
  background: color-mix(in srgb, var(--c-bg-elevated) 45%, transparent);
}
.stage-msg {
  position: absolute;
  inset: 0;
  display: grid;
  place-items: center;
  padding: 24px;
  text-align: center;
  font-size: 13px;
}
.stage-msg.err {
  color: var(--c-danger);
}
.stage-tag {
  position: absolute;
  left: 8px;
  bottom: 8px;
  padding: 2px 8px;
  border-radius: var(--r-sm);
  font-size: 11px;
  color: var(--c-text-inverse);
  background: color-mix(in srgb, var(--c-bg-inverse) 72%, transparent);
}
.stage-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 10px;
}
.lib-select {
  width: 160px;
}
.bar-hint {
  margin-left: auto;
  font-size: 11.5px;
  color: var(--c-text-4);
}

.knobs {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 14px 20px;
  margin-top: 14px;
  padding-top: 14px;
  border-top: 1px solid var(--c-border);
}
.knob-label {
  display: flex;
  justify-content: space-between;
  margin-bottom: 6px;
  font-size: 12.5px;
  color: var(--c-text-2);
}
.knob-val {
  color: var(--c-text-3);
  font-variant-numeric: tabular-nums;
}
.knob-hint {
  margin-top: 4px;
  font-size: 11.5px;
  color: var(--c-text-3);
}

.thumbs {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-height: 0;
  overflow-y: auto;
  padding: 2px 6px 2px 2px;
}
.thumb {
  all: unset;
  cursor: pointer;
  flex: none;
  display: grid;
  grid-template-columns: 96px minmax(0, 1fr);
  align-items: center;
  gap: 10px;
  padding: 5px;
  border: 1.5px solid var(--c-border);
  border-radius: var(--radius);
  transition: border-color 0.15s, box-shadow 0.15s;
}
.thumb:hover {
  border-color: var(--c-border-strong);
}
.thumb:focus-visible {
  outline: 2px solid var(--c-primary);
  outline-offset: 2px;
}
.thumb.on {
  border-color: var(--c-primary);
  box-shadow: 0 0 0 2px var(--c-primary-soft);
}
.thumb-img {
  aspect-ratio: 16 / 9;
  object-fit: cover;
  border-radius: 12px;
  border-radius: var(--r-sm);
  overflow: hidden;
  background: var(--c-bg-raised);
}
.thumb-img img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}
.thumb-cap {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
  font-size: 11px;
  line-height: 1.35;
  color: var(--c-text-3);
}
.thumb-cap > * {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.thumb-cap b {
  font-weight: 600;
  font-size: 12px;
  color: var(--c-text-1);
}
.thumb.on .thumb-cap b {
  color: var(--c-primary);
}

.libs {
  margin: 4px 0 0;
}
.muted {
  color: var(--c-text-3);
  font-size: 12px;
}

.gallery-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 10px;
}
.gallery-head .muted {
  flex: 1;
}
.gallery {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 10px;
}
.cover {
  margin: 0;
  border: 1px solid var(--c-border);
  border-radius: var(--radius);
  overflow: hidden;
}
.cover-img {
  display: block;
  width: 100%;
  aspect-ratio: 16 / 9;
  object-fit: cover;
  border-radius: 12px;
}
.cover-img :deep(img) {
  width: 100%;
  height: 100%;
}
.cover figcaption {
  display: flex;
  justify-content: space-between;
  gap: 6px;
  padding: 4px 8px;
  font-size: 11.5px;
  color: var(--c-text-2);
}
.cover figcaption span {
  color: var(--c-text-3);
}
.empty {
  padding: 60px 0;
  text-align: center;
}

.foot {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

@media (max-width: 720px) {
  .pane {
    height: auto;
    max-height: calc(100vh - 220px);
  }
  /* 窄屏上下排：样式改成预览上方的一条横向滑条，点选后效果就在正下方 */
  .pane-style {
    display: flex;
    flex-direction: column;
    overflow-y: auto;
  }
  .studio {
    overflow: visible;
  }
  .thumbs {
    order: -1;
    flex-direction: row;
    overflow: auto hidden;
    padding: 2px 2px 6px;
  }
  .thumb {
    grid-template-columns: 1fr;
    width: 132px;
    gap: 4px;
  }
  .knobs {
    grid-template-columns: 1fr;
  }
  .gallery {
    grid-template-columns: 1fr 1fr;
  }
  .bar-hint {
    display: none;
  }
}
</style>
