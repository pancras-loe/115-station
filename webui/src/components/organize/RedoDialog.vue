<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Clapperboard, Star } from '@lucide/vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HModal from '@/components/hero/HModal.vue'
import HSearchField from '@/components/hero/HSearchField.vue'
import HSkeleton from '@/components/hero/HSkeleton.vue'
import { resourcesApi } from '@/api'
import type { TmdbCandidate } from '@/api/resources'
import type { OrganizeRecord } from '@/api/organize'

/**
 * 指定 TMDB 条目。两种用途共用一个弹窗：
 *   - redo：已整理过的条目识别错了，后端按记录里的 fid 原地重做；
 *   - confirm：人工确认模式下还在待整理里的条目，按指定条目走完整流水线入库。
 * 搜索框既吃片名也吃纯数字的 TMDB ID（后端 /tmdb/search 已经区分处理）。
 */
const props = withDefaults(
  defineProps<{ show: boolean; record: OrganizeRecord | null; mode?: 'redo' | 'confirm' }>(),
  { mode: 'redo' },
)
const TEXT = {
  redo: {
    title: '重新整理',
    note: '加入队列后会把这些文件从当前位置改名并搬到正确目录，旧的 STRM 与元数据一并清理。',
    ok: '加入队列重新整理',
  },
  confirm: {
    title: '重新指定 TMDB 条目',
    note: '加入队列后按这个条目继续整理：洗版、重命名、搬入媒体库、写 STRM 与刮削。',
    ok: '加入队列入库',
  },
}
const text = computed(() => TEXT[props.mode])
const emit = defineEmits<{
  'update:show': [boolean]
  /** 用户确认了某个条目：立即加入任务队列 */
  confirm: [TmdbCandidate]
  /** 只暂存这个指定，稍后和别的记录一起「提交到队列」 */
  stage: [TmdbCandidate]
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
  <HModal :show="show" :title="text.title" width="660px" @update:show="emit('update:show', $event)">
    <div class="body">
      <p class="src">
        原条目：<b>{{ record?.source }}</b>
        <span v-if="record?.title"> · 当前识别为 {{ record.title }} {{ record.year }}</span>
        <span v-else-if="mode === 'confirm'"> · 未能自动识别</span>
      </p>

      <div class="search">
        <HSearchField
          v-model="keyword"
          class="search-input"
          placeholder="输入片名，或直接填 TMDB ID（纯数字）"
          @search="search"
        />
        <HButton variant="primary" :loading="loading" @click="search">搜索</HButton>
      </div>

      <div v-if="loading" class="cands" aria-busy="true">
        <div v-for="i in 3" :key="i" class="cand cand-skel">
          <HSkeleton width="60px" height="90px" radius="10px" />
          <div class="skel-lines">
            <HSkeleton width="50%" height="14px" radius="999px" />
            <HSkeleton width="90%" height="11px" radius="999px" />
            <HSkeleton width="75%" height="11px" radius="999px" />
          </div>
        </div>
      </div>

      <template v-else>
        <p v-if="hint" class="hint">{{ hint }}</p>

        <div class="cands" role="radiogroup" aria-label="TMDB 候选">
          <button
            v-for="it in items"
            :key="`${it.media_type}-${it.id}`"
            type="button"
            role="radio"
            class="cand"
            :aria-checked="picked?.id === it.id && picked?.media_type === it.media_type"
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
                <HChip :color="it.media_type === 'tv' ? 'accent' : 'warning'">
                  {{ it.media_type === 'tv' ? '剧集' : '电影' }}
                </HChip>
                <b class="cand-title">{{ it.title }}</b>
                <span class="cand-year">{{ it.year }}</span>
                <span class="cand-id">tmdb={{ it.id }}</span>
                <span v-if="it.vote" class="cand-vote"><Star :size="12" />{{ it.vote.toFixed(1) }}</span>
              </div>
              <p v-if="it.overview" class="cand-overview">{{ it.overview }}</p>
            </div>
          </button>
        </div>
      </template>
    </div>

    <template #footer>
      <span class="foot-note">{{ text.note }}</span>
      <div class="foot-btns">
        <HButton variant="tertiary" @click="emit('update:show', false)">取消</HButton>
        <HButton
          variant="secondary"
          :disabled="!picked"
          title="先记在这条记录上，改好多条后在列表里勾选统一提交"
          @click="picked && emit('stage', picked)"
        >
          暂存
        </HButton>
        <HButton variant="primary" :disabled="!picked" @click="picked && emit('confirm', picked)">
          {{ text.ok }}
        </HButton>
      </div>
    </template>
  </HModal>
</template>

<style scoped>
.body {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.src {
  margin: 0;
  font-size: 12.5px;
  color: var(--muted);
  word-break: break-all;
}
.src b {
  color: var(--foreground);
  font-weight: 500;
}
/* 搜索栏在弹窗正文里吸顶：候选一长，往下翻也随时能改关键词 */
.search {
  display: flex;
  gap: 8px;
  position: sticky;
  top: 0;
  z-index: 1;
  background: var(--overlay);
  padding-bottom: 4px;
}
.search-input {
  flex: 1;
  min-width: 0;
}
.hint {
  margin: 0;
  padding: 24px 0;
  text-align: center;
  color: var(--muted);
  font-size: 13px;
}

.cands {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.cand {
  all: unset;
  box-sizing: border-box;
  display: flex;
  gap: 12px;
  padding: 10px;
  border-radius: 18px;
  background: var(--surface-secondary);
  cursor: pointer;
  transition:
    background-color 150ms ease,
    box-shadow 150ms ease,
    transform 150ms ease;
}
@media (hover: hover) {
  .cand:hover {
    background: color-mix(in oklab, var(--surface-secondary) 70%, var(--surface-tertiary));
  }
}
.cand:active {
  transform: scale(0.99);
}
.cand:focus-visible {
  box-shadow: 0 0 0 2px var(--focus);
}
.cand.picked {
  background: var(--accent-soft);
  box-shadow: inset 0 0 0 2px var(--accent);
}
.cand-skel {
  cursor: default;
  align-items: center;
}
.skel-lines {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.poster {
  width: 60px;
  height: 90px;
  flex: none;
  object-fit: cover;
  border-radius: 10px;
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
  gap: 8px;
  flex-wrap: wrap;
}
.cand-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--foreground);
}
.cand-year,
.cand-id {
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
  margin: 6px 0 0;
  font-size: 12.5px;
  line-height: 1.6;
  color: color-mix(in oklab, var(--foreground) 70%, var(--muted));
  display: -webkit-box;
  -webkit-line-clamp: 3;
  line-clamp: 3;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.foot-note {
  margin-right: auto;
  flex: 1 1 240px;
  font-size: 12px;
  color: var(--muted);
}
.foot-btns {
  display: flex;
  gap: 8px;
}

/* 手机：说明文字单独一行，两个按钮平分宽度 */
@media (max-width: 639px) {
  .foot-note {
    flex-basis: 100%;
  }
  .foot-btns {
    width: 100%;
  }
  .foot-btns > * {
    flex: 1;
  }
}
</style>
