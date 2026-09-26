<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Clapperboard, Star } from '@lucide/vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HSearchField from '@/components/hero/HSearchField.vue'
import HSkeleton from '@/components/hero/HSkeleton.vue'
import { resourcesApi } from '@/api'
import type { TmdbCandidate } from '@/api/resources'

/**
 * 内嵌在刮削 / 整理弹窗里的 TMDB 条目选择：搜索框 + 候选列表。
 * 与整理记录的 RedoDialog 同一个搜索接口（片名或纯数字 TMDB ID 都行），
 * 这里不自成弹窗 —— 弹窗里再叠弹窗在手机上是两层底部抽屉。
 */
const props = defineProps<{ initial: string }>()
const picked = defineModel<TmdbCandidate | null>({ default: null })

const keyword = ref('')
const items = ref<TmdbCandidate[]>([])
const hint = ref('')
const loading = ref(false)

async function search() {
  const q = keyword.value.trim()
  if (!q) return
  loading.value = true
  items.value = []
  hint.value = ''
  picked.value = null
  try {
    const d = await resourcesApi.tmdbSearch(q)
    items.value = d.data ?? []
    if (!items.value.length) hint.value = d.hint || '没找到匹配条目，换个片名或直接填 TMDB ID 试试'
  } catch (e) {
    hint.value = e instanceof Error ? e.message : '搜索失败'
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  // 原名去掉扩展名当初始关键词：多数情况下改两个字就能搜到
  keyword.value = props.initial.replace(/\.[a-z0-9]{2,4}$/i, '')
  if (keyword.value) void search()
})

const isPicked = (it: TmdbCandidate) => picked.value?.id === it.id && picked.value?.media_type === it.media_type
</script>

<template>
  <div class="picker">
    <div class="search">
      <HSearchField v-model="keyword" class="search-input" placeholder="输入片名，或直接填 TMDB ID（纯数字）" @search="search" />
      <HButton variant="secondary" :loading="loading" @click="search">搜索</HButton>
    </div>

    <div v-if="loading" class="cands" aria-busy="true">
      <div v-for="i in 2" :key="i" class="cand">
        <HSkeleton width="40px" height="60px" radius="8px" />
        <div class="skel-lines">
          <HSkeleton width="50%" height="13px" radius="999px" />
          <HSkeleton width="85%" height="11px" radius="999px" />
        </div>
      </div>
    </div>
    <p v-else-if="hint" class="hint">{{ hint }}</p>
    <div v-else class="cands" role="radiogroup" aria-label="TMDB 候选">
      <button
        v-for="it in items"
        :key="`${it.media_type}-${it.id}`"
        type="button"
        role="radio"
        class="cand"
        :aria-checked="isPicked(it)"
        :class="{ picked: isPicked(it) }"
        @click="picked = it"
      >
        <img v-if="it.poster" :src="resourcesApi.tmdbImageUrl(it.poster)" class="poster" loading="lazy" :alt="it.title" />
        <div v-else class="poster poster-none"><Clapperboard :size="16" /></div>
        <div class="cand-body">
          <div class="cand-head">
            <HChip :color="it.media_type === 'tv' ? 'accent' : 'warning'">{{ it.media_type === 'tv' ? '剧集' : '电影' }}</HChip>
            <b class="cand-title">{{ it.title }}</b>
            <span class="cand-meta">{{ it.year }}</span>
            <span class="cand-meta">tmdb={{ it.id }}</span>
            <span v-if="it.vote" class="cand-vote"><Star :size="11" />{{ it.vote.toFixed(1) }}</span>
          </div>
          <p v-if="it.overview" class="cand-overview">{{ it.overview }}</p>
        </div>
      </button>
    </div>
  </div>
</template>

<style scoped>
.picker {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.search {
  display: flex;
  gap: 8px;
}
.search-input {
  flex: 1;
  min-width: 0;
}
.hint {
  margin: 0;
  padding: 16px 0;
  text-align: center;
  color: var(--muted);
  font-size: 13px;
}
.cands {
  display: flex;
  flex-direction: column;
  gap: 6px;
  max-height: 300px;
  overflow-y: auto;
}
.cand {
  all: unset;
  box-sizing: border-box;
  display: flex;
  gap: 10px;
  padding: 8px;
  border-radius: 14px;
  background: var(--surface-secondary);
  cursor: pointer;
  transition: background-color 150ms ease, box-shadow 150ms ease;
}
@media (hover: hover) {
  .cand:hover {
    background: color-mix(in oklab, var(--surface-secondary) 70%, var(--surface-tertiary));
  }
}
.cand:focus-visible {
  box-shadow: 0 0 0 2px var(--focus);
}
.cand.picked {
  background: var(--accent-soft);
  box-shadow: inset 0 0 0 2px var(--accent);
}
.skel-lines {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 8px;
  justify-content: center;
}
.poster {
  width: 40px;
  height: 60px;
  flex: none;
  object-fit: cover;
  border-radius: 8px;
  background: var(--default);
}
.poster-none {
  display: grid;
  place-items: center;
  color: var(--muted);
}
.cand-body {
  min-width: 0;
  flex: 1;
}
.cand-head {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.cand-title {
  font-size: 13.5px;
  font-weight: 600;
  color: var(--foreground);
}
.cand-meta {
  font-size: 12px;
  color: var(--muted);
}
.cand-vote {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  font-size: 12px;
  color: var(--warning-soft-foreground);
}
.cand-overview {
  margin: 4px 0 0;
  font-size: 12px;
  line-height: 1.55;
  color: color-mix(in oklab, var(--foreground) 70%, var(--muted));
  display: -webkit-box;
  -webkit-line-clamp: 2;
  line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
</style>
