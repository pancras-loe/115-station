<script setup lang="ts">
/**
 * 「试一试」：搜一部 TMDB 影片，按页面上还没保存的规则算它进哪个目录、为什么跳过前面几条。
 * 判定在后端（POST /category/schemes/preview，与整理同一个 matchCategory），这里只显示。
 * 简单模式传 form，专家模式传 yaml。
 */
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { libraryApi, resourcesApi } from '@/api'
import type { CatSimpleForm, CatTryMedia, CatTryResult } from '@/api/library'
import type { TmdbCandidate } from '@/api/resources'
import HButton from '@/components/hero/HButton.vue'
import HSearchField from '@/components/hero/HSearchField.vue'
import MediaTypeChip from '@/components/MediaTypeChip.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import { countryName, genreName, languageName } from './categoryRef'

const props = defineProps<{ form?: CatSimpleForm | null; yaml?: string }>()
/** 命中的那条（给清单高亮）；没选片 / 出错时为 null */
const emit = defineEmits<{ hit: [{ media: 'movie' | 'tv'; index: number } | null] }>()

const keyword = ref('')
const searching = ref(false)
const cands = ref<TmdbCandidate[]>([])
const hint = ref('')
const picked = ref<TmdbCandidate | null>(null)

async function search() {
  const q = keyword.value.trim()
  if (!q) return
  searching.value = true
  hint.value = ''
  try {
    const d = await resourcesApi.tmdbSearch(q)
    cands.value = (d.data ?? []).slice(0, 8)
    if (!cands.value.length) hint.value = d.hint || '没找到，换个片名试试'
  } catch (e) {
    hint.value = e instanceof Error ? e.message : '搜索失败'
  } finally {
    searching.value = false
  }
}

const result = ref<CatTryResult | null>(null)
const media = ref<CatTryMedia | null>(null)
const err = ref('')
const busy = ref(false)
let timer: ReturnType<typeof setTimeout> | undefined
let seq = 0

async function run() {
  const p = picked.value
  if (!p) return
  const my = ++seq
  busy.value = true
  try {
    const d = await libraryApi.previewScheme({
      ...(props.form ? { simple: props.form } : { yaml: props.yaml ?? '' }),
      tmdb_id: p.id,
      media_type: p.media_type,
    })
    if (my !== seq) return
    result.value = d.try ?? null
    media.value = d.media ?? null
    err.value = ''
  } catch (e) {
    if (my !== seq) return
    result.value = null
    err.value = e instanceof Error ? e.message : '计算失败'
  } finally {
    if (my === seq) busy.value = false
  }
}

// 规则一改就重算（防抖：改目录名时一个字一个字地打）
watch(
  () => [picked.value?.id, JSON.stringify(props.form ?? null), props.yaml],
  () => {
    clearTimeout(timer)
    timer = setTimeout(run, 350)
  },
)
onBeforeUnmount(() => clearTimeout(timer))

function pick(c: TmdbCandidate) {
  picked.value = c
  result.value = null
  clearTimeout(timer)
  void run()
}

watch(
  () => [result.value, media.value] as const,
  ([r, m]) => emit('hit', r && m ? { media: m.media_type, index: r.index } : null),
)

const facts = computed(() => {
  const m = media.value
  if (!m) return ''
  const parts: string[] = []
  const countries = (m.origin_country ?? []).map((c) => countryName(c) || c)
  if (countries.length) parts.push(`地区 ${countries.join(' / ')}`)
  if (m.original_language) parts.push(`原语言 ${(languageName(m.original_language) || m.original_language).replace(/（.*）$/, '')}`)
  const genres = m.genre_ids.map((g) => (genreName(m.media_type, String(g)) || String(g)).replace(/（.*）$/, ''))
  if (genres.length) parts.push(`类型 ${genres.join(' / ')}`)
  const prod = (m.production_countries ?? []).map((c) => countryName(c) || c)
  if (prod.length) parts.push(`制片国家 ${prod.join(' / ')}`)
  const short = (l: string[] | null | undefined, n: number) =>
    (l ?? []).slice(0, n).join(' / ') + ((l?.length ?? 0) > n ? ' …' : '')
  if (m.keywords?.length) parts.push(`TMDB 标签 ${short(m.keywords, 8)}`)
  if (m.cast?.length) parts.push(`演员 ${short(m.cast, 6)}`)
  if (m.directors?.length) parts.push(`导演 ${short(m.directors, 6)}`)
  return parts.join(' · ')
})

const why = computed(() => {
  const r = result.value
  const m = media.value
  if (!r || !m) return ''
  const label = m.media_type === 'tv' ? '剧集' : '电影'
  if (r.index >= 0) return `命中${label}第 ${r.index + 1} 条`
  return r.skips.length ? `上面 ${r.skips.length} 条都不符合，放进「其余」` : '放进「其余」'
})
</script>

<template>
  <SectionCard title="试一试" hint="按页面上还没保存的规则算">
    <div class="search">
      <HSearchField v-model="keyword" placeholder="搜 TMDB：片名" class="grow" @search="search" />
      <HButton :loading="searching" @click="search">搜索</HButton>
    </div>
    <p v-if="hint" class="muted">{{ hint }}</p>
    <div v-if="cands.length" class="cands">
      <button
        v-for="c in cands"
        :key="`${c.media_type}-${c.id}`"
        type="button"
        class="cand"
        :aria-pressed="picked?.id === c.id && picked?.media_type === c.media_type"
        @click="pick(c)"
      >
        <MediaTypeChip :type="c.media_type" />
        <span>{{ c.title }}</span>
        <span v-if="c.year" class="muted">{{ c.year }}</span>
      </button>
    </div>

    <div v-if="picked" class="out" :aria-busy="busy">
      <div class="out-head">
        <MediaTypeChip :type="picked.media_type" />
        <b>{{ picked.title }}</b>
        <span v-if="picked.year" class="muted">{{ picked.year }}</span>
      </div>
      <p v-if="facts" class="muted small">{{ facts }}</p>
      <p v-if="err" class="err">{{ err }}</p>
      <template v-else-if="result">
        <div class="dest">
          <span class="muted small">会放进</span>
          <span class="dest-path">{{ result.dest }}</span>
          <span class="small">{{ why }}</span>
        </div>
        <ol v-if="result.skips.length" class="skips">
          <li v-for="s in result.skips" :key="s.index">
            <span class="muted">跳过第 {{ s.index + 1 }} 条</span>
            <span class="mono">{{ s.name }}</span>
            <span>{{ s.reason }}</span>
          </li>
        </ol>
      </template>
      <p v-else class="muted small">计算中…</p>
    </div>
    <p v-else-if="!cands.length && !hint" class="muted small">搜一部片，看它会进哪个目录、为什么没进前面那几个。</p>
  </SectionCard>
</template>

<style scoped>
.search {
  display: flex;
  gap: 8px;
}
.grow {
  flex: 1;
  min-width: 0;
}
.cands {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 10px;
}
.cand {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font: inherit;
  font-size: 13px;
  padding: 4px 10px;
  border-radius: var(--r-sm);
  border: 1px solid var(--border);
  background: transparent;
  color: var(--foreground);
  cursor: pointer;
}
.cand[aria-pressed='true'] {
  border-color: var(--accent);
  background: color-mix(in srgb, var(--accent) 12%, transparent);
}
.cand:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
}
.out {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-top: 12px;
  padding: 12px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.out-head {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}
.dest {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.dest-path {
  font-family: var(--font-mono);
  font-size: 16px;
  color: var(--accent);
  word-break: break-all;
}
.skips {
  margin: 0;
  padding: 8px 0 0;
  list-style: none;
  border-top: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 12px;
}
.skips li {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.mono {
  font-family: var(--font-mono);
}
.muted {
  color: var(--muted);
}
.small {
  margin: 0;
  font-size: 12px;
}
.err {
  margin: 0;
  font-size: 12.5px;
  color: var(--danger);
}
</style>
