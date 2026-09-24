<script setup lang="ts">
import { ref, watch } from 'vue'
import HSpinner from '@/components/hero/HSpinner.vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HModal from '@/components/hero/HModal.vue'
import { heroTone } from '@/components/hero/tone'
import { Clapperboard, Star } from '@lucide/vue'
import { resourcesApi } from '@/api'
import type { TmdbCandidate } from '@/api/resources'

/**
 * TMDB 选片弹窗，四个资源站共用。
 * 流程：用户输入的关键词先经 TMDB 匹配成规范标题，再拿这个标题去站内搜 ——
 * 站内搜索对标题格式很敏感，直接用用户随手打的关键词命中率低很多。
 */
const props = defineProps<{ show: boolean; query: string; skipLabel?: string }>()
const emit = defineEmits<{
  'update:show': [boolean]
  /** 选中某个条目，回传规范标题与完整条目 */
  pick: [string, TmdbCandidate]
  /** 跳过 TMDB，直接用原始关键词搜 */
  skip: []
}>()

const items = ref<TmdbCandidate[]>([])
const hint = ref('')
const loading = ref(false)

async function search() {
  loading.value = true
  items.value = []
  hint.value = ''
  try {
    const d = await resourcesApi.tmdbSearch(props.query)
    items.value = d.data ?? []
    if (!items.value.length) hint.value = d.hint || '未找到匹配的影视条目'
  } catch (e) {
    hint.value = e instanceof Error ? e.message : '匹配失败'
  } finally {
    loading.value = false
  }
}

watch(() => props.show, (v) => v && search())
</script>

<template>
  <HModal :show="show" :title="`选择影视${items.length ? `（${items.length} 个结果）` : ''}`" width="640px" @update:show="emit('update:show', $event)">
    <div class="body">
      <div v-if="loading" class="state"><HSpinner size="sm" /><span>TMDB 匹配中…</span></div>

      <template v-else>
        <p v-if="hint" class="hint">{{ hint }}</p>

        <button
          v-for="it in items"
          :key="`${it.media_type}-${it.id}`"
          class="cand"
          @click="emit('pick', it.title, it), emit('update:show', false)"
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
              <HChip :color="heroTone(it.media_type === 'tv' ? 'info' : 'warning')">
                {{ it.media_type === 'tv' ? '剧集' : '电影' }}
              </HChip>
              <b class="cand-title">{{ it.title }}</b>
              <span class="cand-year">{{ it.year }}</span>
              <span v-if="it.vote" class="cand-vote"><Star :size="12" />{{ it.vote.toFixed(1) }}</span>
            </div>
            <p v-if="it.overview" class="cand-overview">{{ it.overview }}</p>
          </div>
        </button>
      </template>
    </div>

    <template #footer>
      <div class="foot">
        <HButton variant="ghost" class="text-btn" @click="emit('skip'), emit('update:show', false)">
          {{ skipLabel || '跳过 TMDB，直接用关键词搜索' }}
        </HButton>
        <HButton variant="tertiary" @click="emit('update:show', false)">关闭</HButton>
      </div>
    </template>
  </HModal>
</template>

<style scoped>
.body {
  display: flex;
  flex-direction: column;
  gap: 10px;
  max-height: 60vh;
  overflow-y: auto;
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
  transition: background-color 0.15s, border-color 0.15s;
}
.cand:hover {
  background: var(--c-bg-hover);
  border-color: var(--c-border-strong);
}

.poster {
  width: 60px;
  height: 90px;
  flex: none;
  object-fit: cover;
  border-radius: var(--r-sm);
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
.cand-year {
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
.foot > :first-child {
  margin-right: auto;
}
</style>
