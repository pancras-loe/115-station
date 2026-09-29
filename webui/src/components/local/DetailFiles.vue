<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Captions, Check, ChevronRight, FileText, Image, Minus, TriangleAlert, X } from '@lucide/vue'
import HChip from '@/components/hero/HChip.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import CopyBox from '@/components/ui/CopyBox.vue'
import type { LocalEntry, LocalFile, LocalSeason, LocalTitleDetail } from '@/api/local'
import { bytes } from '@/utils/format'
import { fullTime } from '@/utils/time'

/**
 * 片目详情 ·「刮削文件」：刮削会写的每一个产物在不在。
 * 片目级（NFO、海报、背景图…）平铺成卡片；剧集按季折叠，每季一行汇总，展开看每一集。
 * 必需的缺了标红（再刮一次就会补），可选的缺了标灰（TMDB 上本来就可能没有）。
 */
const props = defineProps<{ detail: LocalTitleDetail }>()

const tv = computed(() => props.detail.media_type === 'tv')
const onlyMissing = ref<'all' | 'missing'>('all')

type Mark = 'ok' | 'miss' | 'none'
function mark(f?: LocalFile | null): Mark {
  if (!f) return 'none'
  if (f.exists) return 'ok'
  return f.optional ? 'none' : 'miss'
}
function fileTip(f?: LocalFile | null, what = '') {
  if (!f) return `${what}不适用`
  if (f.exists) return `${f.name} · ${bytes(f.size)} · ${fullTime(f.mod_at)}`
  return f.optional ? `${f.name}：未生成（TMDB 上可能没有这张图，或按占位剧照跳过）` : `${f.name}：缺失，刮削会补上`
}

function isImage(f: LocalFile) {
  return !f.name.toLowerCase().endsWith('.nfo')
}

// ---- 季分组 ----

interface SeasonGroup {
  key: string
  season?: LocalSeason
  title: string
  entries: LocalEntry[]
  nfo: [number, number]
  thumb: [number, number]
  subs: number
  problems: number
}

function count(list: (LocalFile | undefined)[]): [number, number] {
  const fs = list.filter(Boolean) as LocalFile[]
  return [fs.filter((f) => f.exists).length, fs.length]
}

const groups = computed<SeasonGroup[]>(() => {
  const d = props.detail
  if (!tv.value) return []
  const bySeason = new Map<number, LocalEntry[]>()
  const loose: LocalEntry[] = []
  for (const e of d.entries) {
    if (!e.episode) loose.push(e)
    else bySeason.set(e.season ?? 1, [...(bySeason.get(e.season ?? 1) ?? []), e])
  }
  const out: SeasonGroup[] = (d.seasons ?? []).map((s) => {
    const entries = bySeason.get(s.season) ?? []
    const nfo = count(entries.map((e) => e.nfo))
    const thumb = count(entries.map((e) => e.thumb))
    const problems =
      entries.filter((e) => !e.nfo.exists || e.strm_missing || e.orphan).length +
      (s.nfo && !s.nfo.exists ? 1 : 0)
    return {
      key: `s${s.season}`,
      season: s,
      title: s.season === 0 ? '特别篇' : `第 ${s.season} 季`,
      entries,
      nfo,
      thumb,
      subs: entries.filter((e) => e.subtitles?.length).length,
      problems,
    }
  })
  if (loose.length) {
    out.push({
      key: 'loose',
      title: '解析不出集号',
      entries: loose,
      nfo: count(loose.map((e) => e.nfo)),
      thumb: [0, 0],
      subs: loose.filter((e) => e.subtitles?.length).length,
      problems: 0,
    })
  }
  return out
})

const shownGroups = computed(() =>
  onlyMissing.value === 'all' ? groups.value : groups.value.filter((g) => g.problems > 0 || mark(g.season?.poster) === 'miss'),
)

function shownEntries(g: SeasonGroup) {
  if (onlyMissing.value === 'all') return g.entries
  return g.entries.filter((e) => !e.nfo.exists || e.strm_missing || e.orphan || (e.thumb && !e.thumb.exists))
}

// 默认展开：只有一季时展开；多季时展开有缺失的第一季
const open = ref(new Set<string>())
watch(
  () => props.detail.key,
  () => {
    const gs = groups.value
    const first = gs.length === 1 ? gs[0] : gs.find((g) => g.problems > 0)
    open.value = new Set(first ? [first.key] : [])
  },
  { immediate: true },
)
function toggle(k: string) {
  const s = new Set(open.value)
  if (s.has(k)) s.delete(k)
  else s.add(k)
  open.value = s
}

function epLabel(e: LocalEntry) {
  if (!e.episode) return '—'
  return `E${String(e.episode).padStart(2, '0')}`
}
</script>

<template>
  <div class="files">
    <!-- 片目级 -->
    <section class="sec">
      <h4 class="sec-title">片目文件</h4>
      <div class="tiles">
        <div v-for="f in detail.files" :key="f.label" class="tile" :class="`is-${mark(f)}`" :title="fileTip(f)">
          <span class="tile-icon"><Image v-if="isImage(f)" :size="16" /><FileText v-else :size="16" /></span>
          <span class="tile-text">
            <span class="tile-label">{{ f.label }}</span>
            <span class="tile-name">{{ f.name }}</span>
          </span>
          <span class="tile-state">
            <template v-if="f.exists"><Check :size="14" />已有</template>
            <template v-else-if="f.optional"><Minus :size="14" />未生成</template>
            <template v-else><X :size="14" />缺失</template>
          </span>
        </div>
      </div>
    </section>

    <!-- 电影：每个视频一行 -->
    <section v-if="!tv" class="sec">
      <h4 class="sec-title">影片文件</h4>
      <div class="rows">
        <div v-for="e in detail.entries" :key="e.rel" class="movie-row">
          <div class="movie-name" :title="e.rel">
            {{ e.name }}
            <span v-if="e.size" class="dim">{{ bytes(e.size) }}</span>
          </div>
          <div class="movie-marks">
            <span class="m" :class="`is-${mark(e.nfo)}`" :title="fileTip(e.nfo)">
              <Check v-if="e.nfo.exists" :size="13" /><X v-else :size="13" />NFO
            </span>
            <span class="m" :class="e.subtitles?.length ? 'is-ok' : 'is-none'" :title="e.subtitles?.join('\n') || '没有同名外挂字幕'">
              <Captions :size="13" />{{ e.subtitles?.length ? `外挂字幕 ${e.subtitles.length}` : '无外挂字幕' }}
            </span>
            <HChip v-if="e.orphan" color="danger" size="sm">网盘源文件已失效</HChip>
            <HChip v-if="e.strm_missing" color="warning" size="sm">本地 STRM 不在</HChip>
          </div>
        </div>
        <p v-if="!detail.entries.length" class="empty">台账里这个片目没有视频。</p>
      </div>
    </section>

    <!-- 剧集：按季 -->
    <section v-else class="sec">
      <div class="sec-head">
        <h4 class="sec-title">分季 · 分集</h4>
        <HSegmented
          v-model="onlyMissing"
          size="sm"
          :options="[
            { label: '全部', value: 'all' },
            { label: '只看缺失', value: 'missing' },
          ]"
          aria-label="筛选"
        />
      </div>
      <p v-if="!shownGroups.length" class="empty">{{ onlyMissing === 'missing' ? '没有缺失的文件，全都齐了。' : '台账里这部剧没有视频。' }}</p>
      <div v-for="g in shownGroups" :key="g.key" class="season" :class="{ open: open.has(g.key) }">
        <button type="button" class="season-head" :aria-expanded="open.has(g.key)" @click="toggle(g.key)">
          <ChevronRight :size="16" class="chev" />
          <span class="season-title">{{ g.title }}</span>
          <span class="season-count">{{ g.entries.length }} 集</span>
          <span class="season-stats">
            <template v-if="g.season">
              <span class="stat" :class="g.nfo[0] === g.nfo[1] ? 'is-ok' : 'is-miss'">NFO {{ g.nfo[0] }}/{{ g.nfo[1] }}</span>
              <span class="stat" :class="g.thumb[0] === g.thumb[1] ? 'is-ok' : 'is-none'">剧照 {{ g.thumb[0] }}/{{ g.thumb[1] }}</span>
              <span class="stat" :class="`is-${mark(g.season.poster)}`" :title="fileTip(g.season.poster)">季海报</span>
              <span class="stat" :class="`is-${mark(g.season.nfo)}`" :title="g.season.nfo ? fileTip(g.season.nfo) : '集文件没有单独的季目录，刮削不写 season.nfo'">
                season.nfo
              </span>
            </template>
            <span v-else class="stat is-none">刮削会跳过这些视频</span>
          </span>
        </button>
        <div v-if="open.has(g.key)" class="eps">
          <div class="ep ep-head" aria-hidden="true">
            <span class="ep-no">集</span>
            <span class="ep-name">文件</span>
            <span class="ep-mark">NFO</span>
            <span class="ep-mark">剧照</span>
            <span class="ep-mark">字幕</span>
          </div>
          <div v-for="e in shownEntries(g)" :key="e.rel" class="ep">
            <span class="ep-no">{{ epLabel(e) }}</span>
            <span class="ep-name" :title="e.rel">
              <span class="ep-file">{{ e.name }}</span>
              <TriangleAlert v-if="e.orphan" :size="13" class="warn" aria-label="网盘源文件已失效" />
              <span v-if="e.orphan" class="flag danger">源文件失效</span>
              <span v-if="e.strm_missing" class="flag warning">STRM 不在</span>
            </span>
            <span class="ep-mark" :class="`is-${mark(e.nfo)}`" :title="fileTip(e.nfo)">
              <Check v-if="e.nfo.exists" :size="14" /><X v-else :size="14" />
            </span>
            <span class="ep-mark" :class="`is-${mark(e.thumb)}`" :title="fileTip(e.thumb, '剧照')">
              <Check v-if="e.thumb?.exists" :size="14" /><Minus v-else :size="14" />
            </span>
            <span class="ep-mark" :class="e.subtitles?.length ? 'is-ok' : 'is-none'" :title="e.subtitles?.join('\n') || '没有同名外挂字幕'">
              <template v-if="e.subtitles?.length">{{ e.subtitles.length }}</template><Minus v-else :size="14" />
            </span>
          </div>
        </div>
      </div>
    </section>

    <p class="legend">
      <span class="is-ok"><Check :size="12" />已有</span>
      <span class="is-miss"><X :size="12" />缺失，刮削会补</span>
      <span class="is-none"><Minus :size="12" />未生成 / 不适用（TMDB 上可能没有，或判为占位剧照）</span>
    </p>

    <section class="sec">
      <h4 class="sec-title">本地目录</h4>
      <CopyBox :value="detail.dir" />
    </section>
  </div>
</template>

<style scoped>
.files {
  display: flex;
  flex-direction: column;
  gap: 20px;
}
.sec {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.sec-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.sec-title {
  margin: 0;
  font-size: 13px;
  font-weight: 600;
  color: var(--foreground);
}
.empty {
  margin: 0;
  font-size: 12.5px;
  color: var(--muted);
}
.dim {
  margin-left: 6px;
  font-size: 12px;
  color: var(--muted);
}

/* 状态色：已有 / 缺失 / 不适用 */
.is-ok {
  color: var(--success);
}
.is-miss {
  color: var(--danger);
}
.is-none {
  color: var(--muted);
}

.tiles {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 8px;
}
.tile {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
  padding: 10px 12px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  color: var(--foreground);
}
.tile-icon {
  display: grid;
  place-items: center;
  flex: none;
  width: 30px;
  height: 30px;
  border-radius: var(--r-sm);
  background: var(--surface);
  color: var(--muted);
}
.tile-text {
  display: flex;
  flex-direction: column;
  flex: 1;
  min-width: 0;
}
.tile-label {
  font-size: 13px;
  font-weight: 500;
}
.tile-name {
  overflow: hidden;
  font-size: 11.5px;
  color: var(--muted);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.tile-state {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  flex: none;
  font-size: 12px;
  font-weight: 500;
}
.tile.is-ok .tile-state {
  color: var(--success);
}
.tile.is-miss .tile-state {
  color: var(--danger);
}
.tile.is-miss {
  box-shadow: inset 0 0 0 1px color-mix(in oklab, var(--danger) 35%, transparent);
}
.tile.is-none .tile-state {
  color: var(--muted);
}

.rows {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.movie-row {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 10px 12px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.movie-name {
  font-size: 13px;
  word-break: break-all;
}
.movie-marks {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 12px;
}
.m {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  font-size: 12px;
}

.season {
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  overflow: hidden;
}
.season-head {
  all: unset;
  box-sizing: border-box;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px 10px;
  width: 100%;
  padding: 10px 12px;
  cursor: pointer;
}
.season-head:focus-visible {
  box-shadow: inset 0 0 0 2px var(--focus);
}
.chev {
  flex: none;
  color: var(--muted);
  transition: transform 0.15s;
}
.season.open .chev {
  transform: rotate(90deg);
}
.season-title {
  font-size: 13.5px;
  font-weight: 600;
}
.season-count {
  font-size: 12px;
  color: var(--muted);
}
.season-stats {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-left: auto;
}
.stat {
  padding: 1px 8px;
  border-radius: 999px;
  background: var(--surface);
  font-size: 11.5px;
  line-height: 1.6;
}
.eps {
  display: flex;
  flex-direction: column;
  padding: 0 6px 8px;
}
.ep {
  display: grid;
  grid-template-columns: 44px minmax(0, 1fr) 40px 40px 40px;
  align-items: center;
  gap: 6px;
  min-height: 32px;
  padding: 2px 6px;
  border-radius: var(--r-sm);
  font-size: 12.5px;
}
.ep:not(.ep-head):hover {
  background: var(--surface);
}
.ep-head {
  min-height: 26px;
  font-size: 11.5px;
  color: var(--muted);
}
.ep-no {
  font-variant-numeric: tabular-nums;
  color: var(--muted);
}
.ep-name {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.ep-file {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ep-mark {
  display: grid;
  place-items: center;
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}
.ep-head .ep-mark {
  color: var(--muted);
}
.warn {
  flex: none;
  color: var(--danger);
}
.flag {
  flex: none;
  padding: 0 6px;
  border-radius: 999px;
  font-size: 11px;
  line-height: 1.6;
}
.flag.danger {
  background: color-mix(in oklab, var(--danger) 14%, transparent);
  color: var(--danger);
}
.flag.warning {
  background: color-mix(in oklab, var(--warning) 16%, transparent);
  color: var(--warning);
}

.legend {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 14px;
  margin: -6px 0 0;
  font-size: 11.5px;
}
.legend span {
  display: inline-flex;
  align-items: center;
  gap: 3px;
}

@media (max-width: 720px) {
  .tiles {
    grid-template-columns: 1fr 1fr;
  }
  .tile {
    padding: 8px 10px;
    gap: 8px;
  }
  .tile-icon {
    display: none;
  }
  .season-stats {
    margin-left: 26px;
    width: 100%;
  }
  .ep {
    grid-template-columns: 36px minmax(0, 1fr) 30px 30px 30px;
    gap: 4px;
  }
}
</style>
