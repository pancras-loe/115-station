<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ArrowRight } from '@lucide/vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HModal from '@/components/hero/HModal.vue'
import HNumberInput from '@/components/hero/HNumberInput.vue'
import HSkeleton from '@/components/hero/HSkeleton.vue'
import { filesApi } from '@/api'
import type { EpisodePick, EpisodePreview, EpisodePreviewItem, FileJobBody } from '@/api/files'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { useQueueStore } from '@/stores/queue'

/**
 * 给媒体库剧集片目里的视频指定季集（后端 internal/api/fileepisode.go）。
 *
 * 打开时后端按文件名预填（认不出集号时拿末段数字当集号，标「推测」，例如 蜡笔小新第二季-720 → S02E720），
 * 改动后重新预览：新文件名、落到哪个季目录、洗版策略会怎么判，都由后端用执行时同一套逻辑算好。
 * 提交后进任务队列：洗版判输的移「已存在」，其余在片目里改名搬到对应季目录、重写 STRM、刮削、刷新 Emby。
 */
const props = defineProps<{ body: FileJobBody | null }>()
const show = defineModel<boolean>('show', { required: true })

const { message } = useFeedback()
const queue = useQueueStore()

interface Row {
  id: string
  name: string
  season: number | null
  episode: number | null
  guessed: boolean
}

const rows = ref<Row[]>([])
const preview = ref<EpisodePreview | null>(null)
const loadError = ref('')
const loading = ref(false)
const submitting = ref(false)

/** 只认最新一次预览的结果：改得快时旧请求后回来会盖掉新的 */
let seq = 0

function picksOf(): Record<string, EpisodePick> | null {
  const out: Record<string, EpisodePick> = {}
  for (const r of rows.value) {
    if (r.season == null || r.episode == null || r.episode < 1 || r.season < 0) return null
    out[r.id] = { season: r.season, episode: r.episode }
  }
  return out
}

async function runPreview(first: boolean) {
  if (!props.body) return
  const episodes = first ? undefined : picksOf()
  if (!first && !episodes) return
  const my = ++seq
  loading.value = true
  try {
    const d = await filesApi.previewEpisodes({ ...props.body, ...(episodes ? { episodes } : {}) })
    if (my !== seq) return
    preview.value = d
    loadError.value = ''
    if (first) {
      rows.value = (d.items ?? []).map((it) => ({
        id: it.id,
        name: it.name,
        season: it.season,
        episode: it.episode > 0 ? it.episode : null,
        guessed: !!it.guessed,
      }))
    }
  } catch (e) {
    if (my !== seq) return
    preview.value = null
    loadError.value = e instanceof Error ? e.message : '预览失败'
  } finally {
    if (my === seq) loading.value = false
  }
}

watch(show, (v) => {
  if (!v) return
  rows.value = []
  preview.value = null
  loadError.value = ''
  void runPreview(true)
})

let timer: ReturnType<typeof setTimeout> | undefined
function onEdit(r: Row) {
  r.guessed = false
  clearTimeout(timer)
  timer = setTimeout(() => void runPreview(false), 400)
}

const byId = computed(() => {
  const m = new Map<string, EpisodePreviewItem>()
  for (const it of preview.value?.items ?? []) m.set(it.id, it)
  return m
})

const WASH_TONE: Record<string, 'default' | 'accent' | 'success' | 'warning' | 'danger'> = {
  new: 'success',
  replace: 'accent',
  coexist: 'default',
  unchanged: 'default',
  exists: 'warning',
  samefile: 'warning',
  conflict: 'danger',
}
const WASH_LABEL: Record<string, string> = {
  new: '新增集',
  replace: '洗版替换',
  coexist: '共存',
  unchanged: '没变',
  exists: '移已存在',
  samefile: '移已存在',
  conflict: '冲突',
}

/** 落点只显示片目下面那一段（Season 02），片目名在标题里 */
function shortRel(rel?: string) {
  const base = preview.value?.title_rel
  if (!rel || !base) return ''
  return rel === base ? '片目根' : rel.startsWith(base + '/') ? rel.slice(base.length + 1) : rel
}

const missing = computed(() => rows.value.some((r) => r.season == null || r.episode == null || r.episode < 1))
const conflicts = computed(() => (preview.value?.items ?? []).filter((it) => it.wash === 'conflict').length)
const rejects = computed(() => (preview.value?.items ?? []).filter((it) => it.wash === 'exists' || it.wash === 'samefile').length)
const guessedCount = computed(() => rows.value.filter((r) => r.guessed).length)
const canSubmit = computed(
  () => !!props.body && !!preview.value && !preview.value.error && !loading.value && !missing.value && !conflicts.value,
)

async function submit() {
  const episodes = picksOf()
  if (!props.body || !episodes || !canSubmit.value) return
  submitting.value = true
  try {
    const d = await filesApi.submitEpisodes({ ...props.body, episodes })
    message.success(d.message || '指定季集已加入任务队列')
    await queue.submitted(d.job_id)
    show.value = false
  } catch (e) {
    toastError(e, '提交失败')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <HModal v-model:show="show" title="指定季集" width="760px" persistent>
    <div class="body">
      <p v-if="preview" class="src">
        片目：<b>{{ preview.title }}<template v-if="preview.year"> ({{ preview.year }})</template></b>
        <span class="path">{{ preview.title_rel }}</span>
      </p>

      <HAlert v-if="loadError" status="danger">{{ loadError }}</HAlert>
      <template v-else>
        <div v-if="!rows.length" class="rows">
          <div v-for="i in Math.min(body?.items.length ?? 1, 4)" :key="i" class="row">
            <HSkeleton width="60%" height="13px" radius="999px" />
          </div>
        </div>
        <div v-else class="rows">
          <div v-for="r in rows" :key="r.id" class="row">
            <div class="orig" :title="r.name">{{ r.name }}</div>
            <div class="edit">
              <span class="lbl">季</span>
              <HNumberInput
                v-model="r.season"
                class="num"
                :min="0"
                :max="99"
                :aria-label="`${r.name} 的季号`"
                @update:model-value="onEdit(r)"
              />
              <span class="lbl">集</span>
              <HNumberInput
                v-model="r.episode"
                class="num num-ep"
                :min="1"
                :max="9999"
                :aria-label="`${r.name} 的集号`"
                @update:model-value="onEdit(r)"
              />
              <HChip v-if="r.guessed" color="warning" size="sm" title="文件名里认不出集号，拿末段数字推测的，请核对">推测</HChip>
            </div>
            <div v-if="byId.get(r.id)?.new_name" class="result">
              <ArrowRight :size="13" class="arrow" />
              <span class="new" :title="byId.get(r.id)!.new_name">
                <span class="dir">{{ shortRel(byId.get(r.id)!.target_rel) }}/</span>{{ byId.get(r.id)!.new_name }}
              </span>
              <HChip
                v-if="byId.get(r.id)!.wash"
                :color="WASH_TONE[byId.get(r.id)!.wash!]"
                size="sm"
                :title="byId.get(r.id)!.wash_text"
              >
                {{ WASH_LABEL[byId.get(r.id)!.wash!] }}
              </HChip>
            </div>
            <div v-if="byId.get(r.id)?.wash_text && byId.get(r.id)?.wash !== 'new'" class="wash-text">
              {{ byId.get(r.id)!.wash_text }}
            </div>
          </div>
        </div>
      </template>

      <HAlert v-if="preview?.error" status="danger">{{ preview.error }}</HAlert>
      <HAlert v-if="guessedCount" status="warning">
        有 {{ guessedCount }} 个视频的集号是按文件名末段数字推测的，请核对后再提交。
      </HAlert>
      <HAlert v-if="conflicts" status="danger">
        有 {{ conflicts }} 个视频的目标位置已有同名文件，改一下季集，或先把库里那一集移走。
      </HAlert>

      <p class="note">
        同一集库里已有版本时按「洗版策略」判定<template v-if="preview && !preview.strategy">（这个分类没配策略：同一份文件按已存在处理，其余共存）</template>：
        新版赢了旧版让位，判输的移到「已存在」<template v-if="rejects">（本次 {{ rejects }} 个）</template>。
        其余在片目里改名、搬进对应的季目录，旧的 STRM 与元数据清掉后重写，再刮削、刷新 Emby；搬空的目录会删掉。
      </p>
    </div>

    <template #footer>
      <div class="foot-btns">
        <HButton variant="tertiary" @click="show = false">取消</HButton>
        <HButton variant="primary" :disabled="!canSubmit" :loading="submitting" @click="submit">加入队列</HButton>
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
  display: flex;
  flex-wrap: wrap;
  gap: 4px 10px;
  word-break: break-all;
}
.src b {
  color: var(--foreground);
  font-weight: 500;
}
.rows {
  display: flex;
  flex-direction: column;
  border: 1px solid var(--border);
  border-radius: var(--r-lg);
  overflow: hidden;
}
.row {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 10px 12px;
}
.row + .row {
  border-top: 1px solid var(--border);
}
.orig {
  font-size: 13px;
  color: var(--foreground);
  word-break: break-all;
}
.edit {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.lbl {
  font-size: 12px;
  color: var(--muted);
}
.num {
  width: 104px;
}
.num-ep {
  width: 120px;
}
.result {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  font-size: 12px;
}
.arrow {
  flex: none;
  color: var(--muted);
}
.new {
  min-width: 0;
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--foreground);
}
.new .dir {
  color: var(--muted);
}
.wash-text {
  font-size: 12px;
  color: var(--muted);
  padding-left: 19px;
}
.note {
  margin: 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--muted);
}
.foot-btns {
  display: flex;
  gap: 8px;
  margin-left: auto;
}
@media (max-width: 639px) {
  .foot-btns {
    width: 100%;
  }
  .foot-btns > * {
    flex: 1;
  }
}
</style>
