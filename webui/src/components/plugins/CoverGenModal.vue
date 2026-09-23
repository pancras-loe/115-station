<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import {
  NButton,
  NColorPicker,
  NImage,
  NImageGroup,
  NInput,
  NInputNumber,
  NModal,
  NPopconfirm,
  NRadioButton,
  NRadioGroup,
  NSelect,
  NSlider,
  NSpin,
  NSwitch,
  NTabPane,
  NTabs,
} from 'naive-ui'
import { Dices, ImageUp, RefreshCw } from '@lucide/vue'
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
  style: 'static_1',
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
const saving = ref(false)

const STYLES = [
  { v: 'static_1', label: '层叠卡片', desc: '斜向海报墙' },
  { v: 'static_2', label: '对角色块', desc: '标题 + 主海报' },
  { v: 'static_3', label: '矩阵海报', desc: '标题 + 海报矩阵' },
  { v: 'static_4', label: '沉浸背景', desc: '主海报铺满' },
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
// 只影响真实预览的字段：换样式（缩略图四张都有了）、换取图范围
watch(
  () => [form.value.style, form.value.strategy, form.value.poster_count, form.value.include, form.value.blacklist],
  () => {
    if (show.value && liveOn.value) schedule(() => void loadLive())
  },
)

const stageSrc = computed(() => {
  if (liveOn.value) return liveImage.value
  return samples.value[form.value.style === 'random' ? 'static_1' : form.value.style] ?? ''
})
const stageBusy = computed(() => (liveOn.value ? liveLoading.value : samplesLoading.value && !stageSrc.value))
const showBlur = computed(() => form.value.style === 'static_4' || form.value.style === 'random')

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
  <NModal v-model:show="show" preset="card" title="媒体库海报" class="cg-modal" :style="{ width: '920px', maxWidth: '96vw' }">
    <NTabs v-model:value="tab" type="line" animated>
      <!-- ============ 样式 ============ -->
      <NTabPane name="style" tab="封面样式">
        <div class="pane">
          <div class="studio">
            <div class="stage-col">
              <div class="stage">
                <img v-if="stageSrc" :src="stageSrc" alt="封面预览" />
                <div v-else-if="liveErr" class="stage-msg err">{{ liveErr }}</div>
                <div v-if="stageBusy" class="stage-mask"><NSpin size="small" /></div>
                <span class="stage-tag">
                  <template v-if="liveOn">真实海报{{ liveLib ? ` · ${liveLib}` : '' }}</template>
                  <template v-else>示意海报</template>
                  <template v-if="form.style === 'random'"> · 随机：每个库按名称固定一种</template>
                </span>
              </div>
              <div class="stage-bar">
                <NButton size="small" :type="liveOn ? 'primary' : 'default'" :secondary="liveOn" @click="toggleLive">
                  <template #icon><ImageUp :size="14" /></template>
                  {{ liveOn ? '切回示意图' : '用真实海报预览' }}
                </NButton>
                <NSelect
                  v-if="liveOn && liveLibs.length"
                  v-model:value="liveLib"
                  size="small"
                  class="lib-select"
                  :options="liveLibs.map((x) => ({ label: x, value: x }))"
                />
                <NButton v-if="liveOn" size="small" quaternary :loading="liveLoading" @click="loadLive">
                  <template #icon><RefreshCw :size="14" /></template>
                </NButton>
                <span class="bar-hint">预览不保存、不推送 Emby</span>
              </div>
            </div>

            <div class="knobs">
              <div class="knob">
                <div class="knob-label">背景取色</div>
                <NSelect v-model:value="form.background" size="small" :options="BACKGROUNDS" />
              </div>
              <div v-if="form.background === 'custom'" class="knob">
                <div class="knob-label">自定义颜色</div>
                <NColorPicker
                  v-model:value="form.custom_color"
                  size="small"
                  :modes="['hex']"
                  :show-alpha="false"
                  :swatches="SWATCHES"
                />
              </div>
              <div class="knob">
                <div class="knob-label">
                  色彩浓度 <span class="knob-val">{{ Math.round(form.color_ratio * 100) }}%</span>
                </div>
                <NSlider v-model:value="form.color_ratio" :min="0" :max="1" :step="0.01" :tooltip="false" />
              </div>
              <div v-if="showBlur" class="knob">
                <div class="knob-label">
                  遮罩浓度 <span class="knob-val">{{ form.blur }}</span>
                </div>
                <NSlider v-model:value="form.blur" :min="0" :max="95" :step="1" :tooltip="false" />
                <div class="knob-hint">仅「沉浸背景」：越大海报越暗、标题越清楚</div>
              </div>
            </div>
          </div>

          <div class="thumbs">
            <button
              v-for="s in STYLES"
              :key="s.v"
              type="button"
              class="thumb"
              :class="{ on: form.style === s.v }"
              @click="form.style = s.v"
            >
              <span class="thumb-img">
                <img v-if="samples[s.v]" :src="samples[s.v]" :alt="s.label" />
              </span>
              <span class="thumb-cap"><b>{{ s.label }}</b>{{ s.desc }}</span>
            </button>
            <button type="button" class="thumb" :class="{ on: form.style === 'random' }" @click="form.style = 'random'">
              <span class="thumb-img dice"><Dices :size="26" :stroke-width="1.6" /></span>
              <span class="thumb-cap"><b>随机</b>每库一种</span>
            </button>
          </div>
        </div>
      </NTabPane>

      <!-- ============ 标题与范围 ============ -->
      <NTabPane name="title" tab="标题与范围">
        <div class="pane">
          <FieldRow label="标题映射" wide hint="每行：媒体库名=中文标题|英文副标题。没写的库用库名，英文按库名自动推断。">
            <NInput
              v-model:value="form.titles"
              type="textarea"
              :rows="6"
              placeholder="电影=电影|MOVIES&#10;华语剧集=国产剧|CHINESE DRAMA"
            />
          </FieldRow>
          <FieldRow label="仅生成这些库" wide hint="一行一个；留空表示全部媒体库。">
            <NInput v-model:value="form.include" type="textarea" :rows="3" />
          </FieldRow>
          <FieldRow label="排除这些库" wide hint="一行一个 Emby 库名；未配置 Emby 时填写本地分类名。">
            <NInput v-model:value="form.blacklist" type="textarea" :rows="3" />
          </FieldRow>
          <p v-if="liveLibs.length" class="muted libs">当前会生成：{{ liveLibs.join('、') }}</p>
        </div>
      </NTabPane>

      <!-- ============ 生成与定时 ============ -->
      <NTabPane name="run" tab="生成与定时">
        <div class="pane">
          <FieldRow label="启用定时生成" tip="关闭后仍可在插件卡片上手动生成。">
            <NSwitch v-model:value="form.enabled" />
          </FieldRow>
          <FieldRow label="执行计划" tip="例：0 0 * * * = 每天 0 点。到点重新生成全部封面并推送 Emby。">
            <CronField v-model="form.cron" placeholder="0 0 * * *" />
          </FieldRow>
          <FieldRow label="海报选取策略">
            <NSelect v-model:value="form.strategy" :options="STRATEGIES" />
          </FieldRow>
          <FieldRow label="取图数量" tip="每个媒体库按选取策略取 1–12 张海报；层叠卡片用前 5 张，矩阵用前 6 张。">
            <NInputNumber v-model:value="form.poster_count" :min="1" :max="12" />
          </FieldRow>
          <FieldRow label="输出分辨率">
            <NRadioGroup v-model:value="form.resolution" size="small">
              <NRadioButton value="480p">480p</NRadioButton>
              <NRadioButton value="720p">720p</NRadioButton>
              <NRadioButton value="1080p">1080p</NRadioButton>
            </NRadioGroup>
          </FieldRow>
        </div>
      </NTabPane>

      <!-- ============ 已生成 ============ -->
      <NTabPane name="gallery" :tab="`已生成${covers.length ? ` (${covers.length})` : ''}`">
        <div class="pane">
          <div class="gallery-head">
            <span class="muted">本地缓存的最近一次生成结果，点击可放大。</span>
            <NButton size="small" quaternary @click="loadCovers">
              <template #icon><RefreshCw :size="14" /></template>
              刷新
            </NButton>
            <NPopconfirm @positive-click="clean">
              <template #trigger><NButton size="small" type="error" ghost :disabled="!covers.length">清理缓存</NButton></template>
              只删除本地生成缓存，不会删除 Emby 当前海报。继续？
            </NPopconfirm>
          </div>
          <NImageGroup v-if="covers.length">
            <div class="gallery">
              <figure v-for="c in covers" :key="c.name" class="cover">
                <NImage
                  :src="`${pluginsApi.coverPreviewUrl(c.name)}&s=${coverStamp}`"
                  :alt="c.name"
                  lazy
                  object-fit="cover"
                  class="cover-img"
                />
                <figcaption>{{ c.name }}<span>{{ c.time }}</span></figcaption>
              </figure>
            </div>
          </NImageGroup>
          <p v-else class="muted empty">还没有生成过封面，保存配置后在插件卡片上点「立即生成」。</p>
        </div>
      </NTabPane>
    </NTabs>

    <template #footer>
      <div class="foot">
        <NButton @click="show = false">取消</NButton>
        <NButton type="primary" :loading="saving" @click="save">保存</NButton>
      </div>
    </template>
  </NModal>
</template>

<style scoped>
/* 各页签等高：切页签时弹窗不跳；内容超出时只在页签内部滚 */
.pane {
  height: min(512px, calc(100vh - 240px));
  overflow-y: auto;
  padding: 4px 2px 2px;
}

.studio {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 280px;
  gap: 16px;
}
.stage {
  position: relative;
  aspect-ratio: 16 / 9;
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
  border-radius: var(--radius-sm);
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
  display: flex;
  flex-direction: column;
  gap: 16px;
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
  display: grid;
  grid-template-columns: repeat(5, minmax(0, 1fr));
  gap: 10px;
  margin-top: 12px;
}
.thumb {
  all: unset;
  cursor: pointer;
  display: flex;
  flex-direction: column;
  border: 1.5px solid var(--c-border);
  border-radius: var(--radius);
  overflow: hidden;
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
  background: var(--c-bg-raised);
  display: grid;
  place-items: center;
  color: var(--c-text-3);
}
.thumb-img img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}
.thumb-cap {
  display: flex;
  gap: 6px;
  align-items: baseline;
  padding: 5px 8px;
  font-size: 11px;
  color: var(--c-text-3);
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
  .studio {
    grid-template-columns: 1fr;
  }
  .thumbs {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
  .gallery {
    grid-template-columns: 1fr 1fr;
  }
  .bar-hint {
    display: none;
  }
}
</style>
