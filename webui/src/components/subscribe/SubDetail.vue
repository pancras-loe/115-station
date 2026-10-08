<script setup lang="ts">
import MediaTypeChip from '@/components/MediaTypeChip.vue'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ExternalLink, Pause, Pencil, Play, Search, Trash2, X } from '@lucide/vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HDrawer from '@/components/hero/HDrawer.vue'
import HSkeleton from '@/components/hero/HSkeleton.vue'
import PosterImage from '@/components/PosterImage.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import { resourcesApi } from '@/api'
import * as subscribeApi from '@/api/subscribe'
import type { EpState, SubDetail, Subscription } from '@/api/subscribe'
import { ATTEMPT_TEXT, ATTEMPT_TONE, STATE_TEXT, STATE_TONE, scopeText } from '@/api/subscribe'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { useQueueStore } from '@/stores/queue'
import { fullTime, relTime } from '@/utils/time'

/**
 * 订阅详情（右侧抽屉，手机全屏）：概况与操作、按季的集格子、试过的资源。
 * 集格子和盘点同一套口径（台账已有 / 在路上 / 等确认 / 缺 / 没播），零 115 请求。
 */
const props = defineProps<{ subId: number | null }>()
const show = defineModel<boolean>('show', { required: true })
const emit = defineEmits<{ edit: [s: Subscription]; changed: []; removed: [id: number] }>()

const router = useRouter()
const queue = useQueueStore()
const { message, dialog } = useFeedback()

const d = ref<SubDetail | null>(null)
const loading = ref(false)
const error = ref('')
const busy = ref('')

async function load() {
  if (!props.subId) return
  loading.value = true
  error.value = ''
  try {
    d.value = await subscribeApi.detail(props.subId)
  } catch (e) {
    error.value = e instanceof Error ? e.message : '读取失败'
  } finally {
    loading.value = false
  }
}
watch(
  () => [show.value, props.subId] as const,
  ([open]) => {
    if (open) {
      d.value = null
      void load()
    }
  },
)
// 这个订阅的检查任务跑完了：刷新一遍
const off = queue.onFinished((j) => {
  if (show.value && j.kind === 'subscribe') void load()
})
onBeforeUnmount(off)

const s = computed(() => d.value?.data ?? null)
const tmdbUrl = computed(() => (s.value ? `https://www.themoviedb.org/${s.value.media_type}/${s.value.tmdb_id}` : ''))

const EP_TEXT: Record<EpState, string> = {
  have: '已有',
  inflight: '在路上',
  awaiting: '等你确认',
  missing: '缺',
  unaired: '没播',
  skipped: '不追',
}
const legend = computed(() => {
  const n: Partial<Record<EpState, number>> = {}
  for (const g of d.value?.grid ?? []) for (const e of g.eps) n[e.state] = (n[e.state] ?? 0) + 1
  return (Object.keys(EP_TEXT) as EpState[]).filter((k) => n[k]).map((k) => ({ k, text: EP_TEXT[k], n: n[k]! }))
})
function epTitle(season: number, e: { e: number; state: EpState; air?: string }) {
  const code = `S${String(season).padStart(2, '0')}E${String(e.e).padStart(2, '0')}`
  return `${code} · ${EP_TEXT[e.state]}${e.air ? ` · ${e.air} 播出` : ''}`
}

async function runNow() {
  if (!s.value) return
  busy.value = 'run'
  try {
    const r = await subscribeApi.run(s.value.id)
    message.success(r.message || '已加入队列')
    if (r.job_id) void queue.submitted(r.job_id)
  } catch (e) {
    toastError(e)
  } finally {
    busy.value = ''
  }
}
async function togglePause() {
  if (!s.value) return
  busy.value = 'pause'
  try {
    const next = s.value.state === 'paused' ? 'active' : 'paused'
    await subscribeApi.update(s.value.id, { ...subscribeApi.toForm(s.value), state: next })
    message.success(next === 'paused' ? '已暂停' : '已恢复追更')
    await load()
    emit('changed')
  } catch (e) {
    toastError(e)
  } finally {
    busy.value = ''
  }
}
async function removeSub() {
  if (!s.value) return
  const ok = await dialog.confirm({
    title: '取消订阅',
    content: `取消《${s.value.title}》的订阅？只删订阅和它的记账，已经转存、入库的不受影响。`,
    tone: 'danger',
    actions: [
      { label: '再想想', value: false, variant: 'tertiary' },
      { label: '取消订阅', value: true, variant: 'danger' },
    ],
  })
  if (!ok) return
  try {
    await subscribeApi.remove(s.value.id)
    message.success('已取消订阅')
    emit('removed', s.value.id)
    show.value = false
  } catch (e) {
    toastError(e)
  }
}
async function retry(aid: number) {
  if (!s.value) return
  try {
    const r = await subscribeApi.retryAttempt(s.value.id, aid)
    message.success(r.message || '下一轮会再试')
    await load()
  } catch (e) {
    toastError(e)
  }
}
function openRecords(linkId: number) {
  show.value = false
  void router.push({ name: 'tasks', query: { tab: 'records', link_id: String(linkId) } })
}
</script>

<template>
  <HDrawer v-model:show="show" side="right" width="min(720px, 100vw)" :title="s?.title || '订阅详情'">
    <div class="sd">
      <header class="head">
        <PosterImage
          class="poster"
          :src="s?.poster_path ? resourcesApi.tmdbImageUrl(s.poster_path, 'w185') : null"
          :alt="s?.title || ''"
        />
        <div class="head-text">
          <template v-if="s">
            <div class="title-row">
              <h2 class="title">{{ s.title }}</h2>
              <span v-if="s.year" class="muted">{{ s.year }}</span>
            </div>
            <div class="chips">
              <HChip :color="STATE_TONE[s.state]" variant="primary">{{ STATE_TEXT[s.state] }}</HChip>
              <MediaTypeChip :type="s.media_type" size="md" />
              <HChip v-if="s.media_type === 'tv'">{{ scopeText(s) }}</HChip>
              <HChip v-if="s.running" color="accent">检查中</HChip>
              <a :href="tmdbUrl" target="_blank" rel="noopener noreferrer" class="tmdb">
                TMDB {{ s.tmdb_id }}<ExternalLink :size="11" />
              </a>
            </div>
            <p class="muted line">
              <template v-if="s.media_type === 'tv'">已有 {{ s.have }} / {{ s.total }} 集<template v-if="s.missing"> · 缺 {{ s.missing }}</template><template v-if="s.inflight"> · {{ s.inflight }} 条在路上</template></template>
              <template v-else>{{ s.have ? '已入库' : s.inflight ? '在路上' : '未入库' }}</template>
            </p>
            <p class="muted line">
              {{ s.last_result || '还没检查过' }}
              <template v-if="s.last_check_at"> · <span :title="fullTime(s.last_check_at)">{{ relTime(s.last_check_at) }}</span></template>
              <template v-if="s.next_check_at && s.state !== 'paused' && s.state !== 'done'">
                · 下次 <span :title="fullTime(s.next_check_at)">{{ fullTime(s.next_check_at) }}</span>
              </template>
            </p>
            <div class="actions">
              <HButton variant="primary" size="sm" :loading="busy === 'run'" :disabled="s.running" @click="runNow">
                <Search :size="14" />立即搜索
              </HButton>
              <HButton variant="secondary" size="sm" @click="emit('edit', s)"><Pencil :size="14" />修改</HButton>
              <HButton v-if="s.state !== 'done'" variant="secondary" size="sm" :loading="busy === 'pause'" @click="togglePause">
                <component :is="s.state === 'paused' ? Play : Pause" :size="14" />{{ s.state === 'paused' ? '恢复' : '暂停' }}
              </HButton>
              <HButton variant="danger-soft" size="sm" @click="removeSub"><Trash2 :size="14" />取消订阅</HButton>
            </div>
          </template>
          <template v-else-if="loading">
            <HSkeleton width="60%" height="20px" radius="999px" />
            <HSkeleton width="40%" height="12px" radius="999px" />
          </template>
        </div>
        <HButton class="close" variant="ghost" size="sm" icon-only aria-label="关闭" @click="show = false">
          <X :size="18" />
        </HButton>
      </header>

      <div class="body">
        <HAlert v-if="error" status="danger">
          {{ error }}
          <template #actions><HButton variant="tertiary" size="sm" @click="load">重试</HButton></template>
        </HAlert>

        <template v-if="d">
          <section v-if="s?.media_type === 'tv'" class="block">
            <h3 class="block-title">集</h3>
            <HAlert v-if="d.grid_error" status="warning">{{ d.grid_error }}</HAlert>
            <template v-else-if="d.grid?.length">
              <div class="legend">
                <span v-for="l in legend" :key="l.k" class="lg"><i :class="`ep-${l.k}`" />{{ l.text }} {{ l.n }}</span>
              </div>
              <div v-for="g in d.grid" :key="g.season" class="season">
                <span class="season-name">{{ g.season === 0 ? '特别篇' : `S${String(g.season).padStart(2, '0')}` }}</span>
                <div class="eps">
                  <span v-for="e in g.eps" :key="e.e" class="ep" :class="`ep-${e.state}`" :title="epTitle(g.season, e)">
                    {{ e.e }}
                  </span>
                </div>
              </div>
            </template>
            <p v-else class="muted">TMDB 上这个范围还没有集。</p>
          </section>

          <section class="block">
            <h3 class="block-title">试过的资源</h3>
            <EmptyState v-if="!d.attempts.length" text="还没有试过资源" />
            <ul v-else class="atts">
              <li v-for="a in d.attempts" :key="a.id" class="att">
                <div class="att-head">
                  <HChip :color="ATTEMPT_TONE[a.status] ?? 'default'" size="sm">{{ ATTEMPT_TEXT[a.status] ?? a.status }}</HChip>
                  <span class="att-title" :title="a.title">{{ a.title }}</span>
                </div>
                <p class="att-meta">
                  <span>{{ a.source }}</span>
                  <span v-if="a.episodes.length">{{ a.episodes.length > 4 ? `${a.episodes[0]} 等 ${a.episodes.length} 集` : a.episodes.join('、') }}</span>
                  <span v-if="a.points">花费 {{ a.points }} 积分</span>
                  <span :title="fullTime(a.created_at)">{{ relTime(a.created_at) }}</span>
                </p>
                <p v-if="a.reason" class="att-reason">{{ a.reason }}</p>
                <div class="att-actions">
                  <button v-if="a.records" type="button" class="link" @click="openRecords(a.link_id)">整理记录（{{ a.records }}）</button>
                  <button v-if="a.status !== 'inflight' && a.status !== 'ingested'" type="button" class="link" @click="retry(a.id)">
                    下一轮再试
                  </button>
                </div>
              </li>
            </ul>
          </section>
        </template>
        <div v-else-if="loading" class="sk">
          <HSkeleton v-for="i in 3" :key="i" width="100%" height="56px" radius="12px" />
        </div>
      </div>
    </div>
  </HDrawer>
</template>

<style scoped>
.sd {
  display: flex;
  flex-direction: column;
  flex: 1;
  min-height: 0;
}
.head {
  position: relative;
  display: flex;
  gap: 14px;
  padding: 20px 20px 16px;
  border-bottom: 1px solid var(--separator);
}
.poster {
  width: 92px;
  height: 138px;
  flex: none;
  border-radius: var(--r-sm);
}
.head-text {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
  flex: 1;
  padding-right: 28px;
}
.title-row {
  display: flex;
  align-items: baseline;
  gap: 8px;
}
.title {
  margin: 0;
  font-size: 18px;
  font-weight: 600;
  color: var(--foreground);
}
.chips {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.tmdb {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  font-size: 12px;
  color: var(--muted);
  text-decoration: none;
}
.tmdb:hover {
  color: var(--accent);
}
.muted {
  font-size: 12.5px;
  color: var(--muted);
}
.line {
  margin: 0;
  line-height: 1.6;
}
.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 4px;
}
.close {
  position: absolute;
  top: 12px;
  right: 12px;
}
.body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 16px 20px 24px;
  display: flex;
  flex-direction: column;
  gap: 20px;
}
.block {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.block-title {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
  color: var(--foreground);
}
.legend {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 14px;
  font-size: 12px;
  color: var(--muted);
}
.lg {
  display: inline-flex;
  align-items: center;
  gap: 5px;
}
.lg i {
  width: 10px;
  height: 10px;
  border-radius: 3px;
}
.season {
  display: flex;
  gap: 10px;
  align-items: flex-start;
}
.season-name {
  flex: none;
  width: 42px;
  padding-top: 4px;
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--muted);
}
.eps {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}
.ep {
  display: grid;
  place-items: center;
  min-width: 26px;
  height: 24px;
  padding: 0 3px;
  border-radius: 6px;
  font-family: var(--font-mono);
  font-size: 11px;
  cursor: default;
}
/* 集格子的颜色：有 = 成功色实底，缺 = 警告色描边，在路上 / 等确认 = 主题色，没播 / 不追 = 灰 */
.ep-have {
  background: color-mix(in oklab, var(--success) 75%, transparent);
  color: var(--success-foreground, var(--foreground));
}
.ep-missing {
  box-shadow: inset 0 0 0 1.5px var(--warning);
  color: var(--warning);
  background: color-mix(in oklab, var(--warning) 10%, transparent);
}
.ep-inflight {
  background: color-mix(in oklab, var(--accent) 30%, transparent);
  color: var(--foreground);
}
.ep-awaiting {
  box-shadow: inset 0 0 0 1.5px var(--accent);
  color: var(--accent);
}
.ep-unaired,
.ep-skipped {
  background: var(--surface-secondary);
  color: var(--muted);
}
.lg i.ep-have,
.lg i.ep-missing,
.lg i.ep-inflight,
.lg i.ep-awaiting,
.lg i.ep-unaired,
.lg i.ep-skipped {
  min-width: 0;
}
.atts {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.att {
  padding: 10px 12px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.att-head {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.att-title {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13px;
  color: var(--foreground);
}
.att-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 2px 12px;
  margin: 0;
  font-size: 12px;
  color: var(--muted);
}
.att-reason {
  margin: 0;
  font-size: 12px;
  line-height: 1.6;
  color: color-mix(in oklab, var(--foreground) 70%, var(--muted));
  word-break: break-all;
}
.att-actions {
  display: flex;
  gap: 14px;
}
.link {
  all: unset;
  font-size: 12px;
  color: var(--accent);
  cursor: pointer;
}
.link:hover {
  text-decoration: underline;
}
.sk {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
@media (max-width: 720px) {
  .head {
    padding: 16px;
  }
  .poster {
    width: 68px;
    height: 102px;
  }
  .body {
    padding: 14px 16px 20px;
  }
}
</style>
