<script setup lang="ts">
/**
 * 洗版策略的「排序预览 + 试一试 + 体检」（2026-10-23，设计见 WASH-SIMPLIFY-PLAN.md）。
 * 拿的是编辑中的草稿（简单设置的表单或专家模式的 YAML），后端纯计算、零 115 请求；
 * 判定与整理时同一个 decideWash，这里不另写一套比法。
 */
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { organizeApi } from '@/api'
import type { WashLadderItem, WashLintItem, WashSimpleForm, WashVerdict } from '@/api/organize'
import HInput from '@/components/hero/HInput.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import { ApiError } from '@/api/client'

const props = defineProps<{
  form?: WashSimpleForm
  yaml?: string
}>()

const mediaType = ref<'movie' | 'tv'>('movie')
const oldName = ref('')
const oldSize = ref('')
const newName = ref('')
const newSize = ref('')

const ladder = ref<WashLadderItem[]>([])
const lint = ref<WashLintItem[]>([])
const verdict = ref<WashVerdict | null>(null)
const strategy = ref('')
const error = ref('')

interface Preset {
  label: string
  type: 'movie' | 'tv'
  old: [string, number]
  nw: [string, number]
}
const PRESETS: Preset[] = [
  { label: '1080p 蓝光 → 4K WEB-DL', type: 'movie', old: ['Oppenheimer.2023.1080p.BluRay.x264-CMCT.mkv', 14.2], nw: ['Oppenheimer.2023.2160p.WEB-DL.HDR10.H265-ADWeb.mkv', 22.5] },
  { label: '4K 蓝光 → 4K DV 原盘', type: 'movie', old: ['Oppenheimer.2023.2160p.BluRay.HDR10.x265-WiKi.mkv', 31], nw: ['Oppenheimer.2023.2160p.BluRay.REMUX.DV.HDR10.HEVC-FRDS.mkv', 78.4] },
  { label: '同一版本，体积不同', type: 'movie', old: ['Oppenheimer.2023.2160p.WEB-DL.HDR10.H265-ADWeb.mkv', 26], nw: ['Oppenheimer.2023.2160p.WEB-DL.HDR10.H265-HHWEB.mkv', 14.8] },
  { label: '剧集 1080p → 4K DV', type: 'tv', old: ['The.Bear.S03E02.1080p.WEB-DL.H264-ADWeb.mkv', 2.1], nw: ['The.Bear.S03E02.2160p.WEB-DL.DV.H265-HHWEB.mkv', 6.4] },
  { label: 'HDTV → WEB-DL', type: 'tv', old: ['庆余年.S02E05.1080p.HDTV.H264-CNHK.mkv', 1.6], nw: ['庆余年.S02E05.1080p.WEB-DL.H264-ADWeb.mkv', 1.4] },
]
function applyPreset(p: Preset) {
  mediaType.value = p.type
  oldName.value = p.old[0]
  oldSize.value = String(p.old[1])
  newName.value = p.nw[0]
  newSize.value = String(p.nw[1])
}
applyPreset(PRESETS[0])

const typeOptions = [
  { label: '电影', value: 'movie' as const },
  { label: '剧集', value: 'tv' as const },
]

const gb = (s: string) => {
  const n = parseFloat(s)
  return Number.isFinite(n) && n > 0 ? n : 0
}

let timer: ReturnType<typeof setTimeout> | undefined
let seq = 0
async function run() {
  const my = ++seq
  try {
    const d = await organizeApi.previewWash({
      ...(props.form ? { form: props.form } : { yaml: props.yaml ?? '' }),
      media_type: mediaType.value,
      old: { name: oldName.value, size_gb: gb(oldSize.value) },
      new: { name: newName.value, size_gb: gb(newSize.value) },
    })
    if (my !== seq) return
    ladder.value = d.ladder ?? []
    lint.value = d.lint ?? []
    verdict.value = d.verdict
    strategy.value = d.strategy
    error.value = ''
  } catch (e) {
    if (my !== seq) return
    // YAML 写到一半是常态，别弹提示，原地说一句
    error.value = e instanceof ApiError ? e.message : '预览失败'
  }
}
watch(
  () => [props.form, props.yaml, mediaType.value, oldName.value, oldSize.value, newName.value, newSize.value],
  () => {
    clearTimeout(timer)
    timer = setTimeout(run, 300)
  },
  { deep: true, immediate: true },
)
onBeforeUnmount(() => clearTimeout(timer))

const kept = computed(() => ladder.value.filter((it) => !(it.prefer && it.tier < 0)))
const avoided = computed(() => ladder.value.filter((it) => it.prefer && it.tier < 0))
const shortName = (n: string) => n.replace(/^(Dune\.Part\.Two\.2024|Shogun\.S01E03)\./, '')
</script>

<template>
  <div class="wp">
    <SectionCard v-if="!form && lint.length" title="体检" hint="拿一组常见资源跑一遍这份 YAML，找出多半不是本意的结果。不拦保存，只提醒。">
      <ul class="lint">
        <li v-for="(it, i) in lint" :key="i" :class="it.level">
          <span class="lint-st">{{ it.strategy }}</span>{{ it.text }}
        </li>
      </ul>
    </SectionCard>

    <SectionCard title="从好到差" :hint="strategy && !form ? `按「${strategy}」这条策略排` : '一组常见资源按这套设置排出来的顺序'">
      <template #extra>
        <HSegmented v-model="mediaType" :options="typeOptions" size="sm" aria-label="看电影还是剧集" />
      </template>
      <p v-if="error" class="err">{{ error }}</p>
      <template v-else>
        <p v-if="!ladder.length" class="empty">没有适用的洗版策略。</p>
        <ol v-else class="ladder">
          <li v-for="it in kept" :key="it.name" :class="{ top: it.rank === 1 }">
            <span class="rk">{{ it.tie ? '=' : it.rank }}</span>
            <span class="tags" :title="shortName(it.name)">
              <span v-for="l in it.labels" :key="l" class="tag">{{ l }}</span>
              <span v-if="!it.prefer" class="tag" :class="it.tier < 0 ? 'dim' : 'hot'">
                {{ it.tier < 0 ? '没命中' : `第 ${it.tier + 1} 档` }}
              </span>
              <span v-if="it.tie" class="tie">和上一行一样好</span>
            </span>
          </li>
        </ol>
        <div v-if="avoided.length" class="avoided">
          <span class="avoided-head">不要的（排在最后）</span>
          <span v-for="it in avoided" :key="it.name" class="avoided-item">
            {{ it.labels.slice(0, 2).join(' ') }} — {{ it.avoid }}
          </span>
        </div>
        <p class="note">体积在这些名字上看不出；要比体积，到「试一试」里填。</p>
      </template>
    </SectionCard>

    <SectionCard title="试一试" hint="贴两个文件名，看会不会换、为什么。体积可不填。">
      <div class="presets">
        <button v-for="p in PRESETS" :key="p.label" type="button" @click="applyPreset(p)">{{ p.label }}</button>
      </div>
      <div class="try">
        <span class="lb">库里的</span>
        <HInput v-model="oldName" mono placeholder="库里那份的文件名" />
        <span class="sz"><HInput v-model="oldSize" type="number" placeholder="体积" />G</span>
        <span class="lb">新来的</span>
        <HInput v-model="newName" mono placeholder="新来那份的文件名" />
        <span class="sz"><HInput v-model="newSize" type="number" placeholder="体积" />G</span>
      </div>
      <div class="try-type">
        这是一部
        <HSegmented v-model="mediaType" :options="typeOptions" size="sm" aria-label="类型" />
      </div>
      <div v-if="verdict" class="verdict" :class="`v-${verdict.kind}`">
        <b>{{ verdict.head }}</b>
        <span>{{ verdict.text }}</span>
      </div>
    </SectionCard>
  </div>
</template>

<style scoped>
.wp {
  display: flex;
  flex-direction: column;
  gap: 16px;
  min-width: 0;
}
.ladder {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.ladder li {
  display: grid;
  grid-template-columns: 24px minmax(0, 1fr);
  gap: 8px;
  align-items: center;
  padding: 5px 8px;
  border-radius: var(--r-sm);
}
.ladder li:nth-child(odd) {
  background: var(--surface-secondary);
}
.rk {
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--muted);
  text-align: right;
  font-variant-numeric: tabular-nums;
}
.ladder li.top .rk {
  color: var(--accent);
  font-weight: 600;
}
.tags {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px;
  min-width: 0;
}
.tag {
  font-family: var(--font-mono);
  font-size: 11.5px;
  padding: 0 7px;
  border-radius: 4px;
  border: 1px solid var(--border);
  color: var(--foreground);
}
.tag.dim {
  color: var(--muted);
}
.tag.hot {
  border-color: var(--accent);
  color: var(--accent);
}
.tie {
  font-size: 11px;
  color: var(--muted);
}
.avoided {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
  margin-top: 10px;
  padding: 8px 10px;
  border-radius: var(--r-sm);
  background: var(--danger-soft);
  color: var(--danger);
  font-size: 12.5px;
}
.avoided-head {
  font-weight: 600;
  width: 100%;
}
.note,
.empty {
  margin: 10px 0 0;
  font-size: 12px;
  color: var(--muted);
}
.err {
  margin: 0;
  font-size: 12.5px;
  color: var(--danger);
}
.presets {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 12px;
}
.presets button {
  font: inherit;
  font-size: 12px;
  padding: 2px 10px;
  border-radius: 999px;
  border: 1px solid var(--border);
  background: var(--surface-secondary);
  color: var(--muted);
  cursor: pointer;
}
.presets button:hover {
  color: var(--foreground);
}
.try {
  display: grid;
  grid-template-columns: 52px minmax(0, 1fr) 96px;
  gap: 8px;
  align-items: center;
}
.lb {
  font-size: 12.5px;
  color: var(--muted);
}
.sz {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
  color: var(--muted);
}
@media (max-width: 520px) {
  .try {
    grid-template-columns: minmax(0, 1fr) 96px;
  }
  .lb {
    grid-column: 1 / 3;
    margin-bottom: -4px;
  }
}
.try-type {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 10px;
  font-size: 12.5px;
  color: var(--muted);
}
.verdict {
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin-top: 14px;
  padding: 10px 14px;
  border-radius: var(--r-sm);
  border-left: 3px solid var(--muted);
  background: var(--surface-secondary);
}
.verdict b {
  font-size: 14.5px;
}
.verdict span {
  font-size: 13px;
  color: var(--muted);
}
.v-good {
  border-color: var(--success);
  background: var(--success-soft);
}
.v-good b {
  color: var(--success);
}
.v-both {
  border-color: var(--accent);
  background: var(--accent-soft);
}
.v-both b {
  color: var(--accent);
}
.lint {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.lint li {
  padding: 8px 12px;
  border-radius: var(--r-sm);
  border-left: 3px solid var(--warning);
  background: var(--warning-soft);
  font-size: 12.5px;
  line-height: 1.7;
}
.lint li.info {
  border-color: var(--muted);
  background: var(--surface-secondary);
}
.lint-st {
  font-weight: 600;
  margin-right: 6px;
}
</style>
