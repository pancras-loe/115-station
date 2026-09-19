<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NButton, NInput, NModal, NSpin, NTag } from 'naive-ui'
import { Clapperboard, Search, Star } from '@lucide/vue'
import { resourcesApi } from '@/api'
import type { TmdbCandidate } from '@/api/resources'
import type { OrganizeRecord } from '@/api/organize'

/**
 * 重新整理：让用户指定正确的 TMDB 条目，后端按记录里的 fid 原地重做。
 * 搜索框既吃片名也吃纯数字的 TMDB ID（后端 /tmdb/search 已经区分处理）。
 */
const props = defineProps<{ show: boolean; record: OrganizeRecord | null }>()
const emit = defineEmits<{
  'update:show': [boolean]
  /** 用户确认了某个条目 */
  confirm: [TmdbCandidate]
}>()

const keyword = ref('')
const items = ref<TmdbCandidate[]>([])
const hint = ref('')
const loading = ref(false)
const picked = ref<TmdbCandidate | null>(null)

/** 原名去掉扩展名和结尾的斜杠，当作搜索框的初始值 —— 多数情况下改两个字就能搜到 */
const defaultKeyword = computed(() => {
  const src = props.record?.title || props.record?.source || ''
  return src.replace(/\/$/, '').replace(/\.[a-z0-9]{2,4}$/i, '')
})

watch(
  () => props.show,
  (v) => {
    if (!v) return
    keyword.value = defaultKeyword.value
    items.value = []
    hint.value = ''
    picked.value = null
    if (keyword.value) search()
  },
)

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
</script>

<template>
  <NModal
    :show="show"
    preset="card"
    title="重新整理"
    style="width: 660px"
    @update:show="emit('update:show', $event)"
  >
    <div class="body">
      <p class="src">
        原条目：<b>{{ record?.source }}</b>
        <span v-if="record?.title"> · 当前识别为 {{ record.title }} {{ record.year }}</span>
      </p>

      <div class="search">
        <NInput
          v-model:value="keyword"
          placeholder="输入片名，或直接填 TMDB ID（纯数字）"
          clearable
          @keyup.enter="search"
        >
          <template #prefix><Search :size="14" /></template>
        </NInput>
        <NButton type="primary" :loading="loading" @click="search">搜索</NButton>
      </div>

      <div v-if="loading" class="state"><NSpin size="small" /><span>TMDB 搜索中…</span></div>

      <template v-else>
        <p v-if="hint" class="hint">{{ hint }}</p>

        <button
          v-for="it in items"
          :key="`${it.media_type}-${it.id}`"
          class="cand"
          :class="{ picked: picked?.id === it.id && picked?.media_type === it.media_type }"
          @click="picked = it"
        >
          <img
            v-if="it.poster"
            :src="resourcesApi.tmdbImageUrl(it.poster)"
            class="poster"
            loading="lazy"
            :alt="it.title"
          />
          <div v-else class="poster poster-none"><Clapperboard :size="18" /></div>

          <div class="cand-body">
            <div class="cand-head">
              <NTag size="small" :bordered="false" :type="it.media_type === 'tv' ? 'info' : 'warning'">
                {{ it.media_type === 'tv' ? '剧集' : '电影' }}
              </NTag>
              <b class="cand-title">{{ it.title }}</b>
              <span class="cand-year">{{ it.year }}</span>
              <span class="cand-id">tmdb={{ it.id }}</span>
              <span v-if="it.vote" class="cand-vote"><Star :size="12" />{{ it.vote.toFixed(1) }}</span>
            </div>
            <p v-if="it.overview" class="cand-overview">{{ it.overview }}</p>
          </div>
        </button>
      </template>
    </div>

    <template #footer>
      <div class="foot">
        <span class="foot-note">
          确认后会把这些文件从当前位置改名并搬到正确目录，旧的 STRM 与元数据一并清理。
        </span>
        <NButton @click="emit('update:show', false)">取消</NButton>
        <NButton type="primary" :disabled="!picked" @click="picked && emit('confirm', picked)">
          用这个条目重新整理
        </NButton>
      </div>
    </template>
  </NModal>
</template>

<style scoped>
.body {
  display: flex;
  flex-direction: column;
  gap: 10px;
  max-height: 62vh;
  overflow-y: auto;
}
.src {
  margin: 0;
  font-size: 12.5px;
  color: var(--c-text-3);
  word-break: break-all;
}
.src b {
  color: var(--c-text-1);
}
.search {
  display: flex;
  gap: 8px;
  position: sticky;
  top: 0;
  z-index: 1;
  background: var(--c-bg-elevated);
  padding-bottom: 2px;
}
.state {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 30px 0;
  color: var(--c-text-3);
  font-size: 13px;
}
.hint {
  margin: 0;
  padding: 20px 0;
  text-align: center;
  color: var(--c-text-3);
  font-size: 13px;
}

.cand {
  all: unset;
  box-sizing: border-box;
  display: flex;
  gap: 12px;
  padding: 10px;
  border: 1px solid var(--c-border);
  border-radius: var(--radius);
  cursor: pointer;
  transition:
    background-color 0.15s,
    border-color 0.15s;
}
.cand:hover {
  background: var(--c-bg-hover);
  border-color: var(--c-border-strong);
}
.cand.picked {
  border-color: var(--c-primary);
  background: var(--c-bg-hover);
}

.poster {
  width: 60px;
  height: 90px;
  flex: none;
  object-fit: cover;
  border-radius: var(--radius-sm);
  background: var(--c-bg-hover);
}
.poster-none {
  display: grid;
  place-items: center;
  color: var(--c-text-4);
}

.cand-body {
  min-width: 0;
  flex: 1;
}
.cand-head {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.cand-title {
  font-size: 14px;
  color: var(--c-text-1);
}
.cand-year,
.cand-id {
  font-size: 12px;
  color: var(--c-text-3);
}
.cand-vote {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  font-size: 12px;
  color: var(--c-warning);
}
.cand-overview {
  margin: 5px 0 0;
  font-size: 12.5px;
  line-height: 1.6;
  color: var(--c-text-2);
  display: -webkit-box;
  -webkit-line-clamp: 3;
  line-clamp: 3;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.foot {
  display: flex;
  align-items: center;
  gap: 8px;
}
.foot-note {
  margin-right: auto;
  font-size: 12px;
  color: var(--c-text-3);
}
</style>
