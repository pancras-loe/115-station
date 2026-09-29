<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { RotateCcw } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import { tasksApi } from '@/api'
import type { ProbeFailRow, ProbeStatus } from '@/api/tasks'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { untilText, useNow } from '@/composables/useNow'
import { useQueueStore } from '@/stores/queue'
import { fullTime, relTime } from '@/utils/time'

/**
 * 任务中心 · Emby 提前探测的全局状态（GET /tasks/probe）。
 * 入库确认排进来的自动探测没有任务可挂，失败了只能在这里（和片目详情里）看到，也在这里手动重试。
 * 手动探测（片目详情、刮削、重新整理）各自是一个「Emby 探测」任务，进度在下面的「进行中」里
 */
const queue = useQueueStore()
const { message } = useFeedback()
const now = useNow()

const st = ref<ProbeStatus | null>(null)
const showAll = ref(false)
const FOLDED = 5

async function load() {
  try {
    st.value = await tasksApi.probeStatus()
  } catch {
    // 拉不到不影响页面其余部分
  }
}

/** 没开、没配、也没有失败记录的：整张卡片不显示，别占地方 */
const visible = computed(() => {
  const s = st.value
  return !!s && (s.enabled || s.queue > 0 || !!s.running || s.fail_total > 0)
})

const paused = computed(() => {
  const t = st.value?.paused_until
  return t && new Date(t).getTime() > now.value ? t : ''
})

const stateText = computed(() => {
  const s = st.value
  if (!s) return ''
  if (!s.emby) return '没有配置 Emby'
  if (paused.value) return `连续失败，暂停到 ${fullTime(paused.value)}（可能是 115 风控或 Emby 异常），之后自动继续`
  if (s.running) return `正在探测：${s.running}${s.queue ? ` · 还有 ${s.queue} 个片目排队` : ''}`
  if (s.queue) return `${s.queue} 个片目排队中`
  return s.enabled ? '空闲' : '入库后自动探测未开启（手动探测照常可用）'
})
const stateWarn = computed(() => !!paused.value || st.value?.emby === false)

const rows = computed(() => {
  const all = st.value?.fails ?? []
  return showAll.value ? all : all.slice(0, FOLDED)
})
const stopped = computed(() => (st.value?.fails ?? []).filter((f) => f.auto_stopped).length)

function chip(f: ProbeFailRow): { text: string; color: 'accent' | 'danger' | 'warning' } {
  if (f.running) return { text: '探测中', color: 'accent' }
  if (f.queued) return { text: '已排队', color: 'accent' }
  if (f.auto_stopped) return { text: '自动已停', color: 'danger' }
  return { text: '自动冷却中', color: 'warning' }
}

/** 这一条的「自动」那一半会怎样 */
function autoText(f: ProbeFailRow) {
  if (f.auto_stopped) return `自动探测已请求 ${f.attempts} 次都失败，不再自动探；需要时手动重试`
  if (f.auto_retry_at) return `再次入库确认时自动再试一次（${fullTime(f.auto_retry_at)} 之后）`
  return '再次入库确认时自动再试一次'
}

const busyIds = ref(new Set<string>())
async function retry(ids: string[]) {
  const s = new Set(busyIds.value)
  ids.forEach((id) => s.add(id))
  busyIds.value = s
  try {
    const d = await tasksApi.retryProbe(ids)
    message.success(`${d.message}，进度在下方「进行中」`)
    await queue.submitted(d.job_id)
    await load()
  } catch (e) {
    toastError(e, '重试失败')
  } finally {
    const s2 = new Set(busyIds.value)
    ids.forEach((id) => s2.delete(id))
    busyIds.value = s2
  }
}

let timer: number | undefined
onMounted(() => {
  void load()
  timer = window.setInterval(load, 15_000)
})
onUnmounted(() => {
  if (timer !== undefined) clearInterval(timer)
})
// 探测任务跑完，失败清单会变
const offFinished = queue.onFinished((j) => {
  if (j.kind === 'probe') void load()
})
onUnmounted(offFinished)
</script>

<template>
  <SectionCard
    v-if="visible && st"
    title="Emby 提前探测"
    :hint="`自动探测（入库后）同一视频最多 ${st.limits.max_attempts} 次、间隔 ${st.limits.retry_hours} 小时，用完不再自动探；手动探测不限次数，同一视频 ${st.limits.debounce_minutes} 分钟内不重复请求。每次都要取一次 115 直链`"
  >
    <dl class="state">
      <div class="state-item">
        <dt>状态</dt>
        <dd :class="{ 'is-warn': stateWarn, 'is-busy': !stateWarn && (st.running || st.queue) }">{{ stateText }}</dd>
      </div>
      <div class="state-item">
        <dt>没成功的视频</dt>
        <dd :class="{ 'is-warn': st.fail_total > 0 }">
          <template v-if="st.fail_total">
            共 {{ st.fail_total }} 个<span v-if="stopped"> · 自动探测已停 {{ stopped }}</span>
          </template>
          <template v-else>没有</template>
        </dd>
      </div>
    </dl>

    <template v-if="st.fails.length">
      <ul class="fails">
        <li v-for="f in rows" :key="f.item_id" class="fail">
          <div class="f-main">
            <div class="f-head">
              <HChip size="sm" :color="chip(f).color">{{ chip(f).text }}</HChip>
              <span class="f-label" :title="f.item_id">{{ f.label || `Emby 条目 ${f.item_id}` }}</span>
              <span class="dim" :title="fullTime(f.last_at)">{{ relTime(f.last_at) }} · 已请求 {{ f.attempts }} 次</span>
            </div>
            <template v-if="!f.running && !f.queued">
              <p v-if="f.last_err" class="f-line">原因：{{ f.last_err }}</p>
              <p class="f-line dim">{{ autoText(f) }}</p>
            </template>
          </div>
          <div v-if="!f.running && !f.queued" class="f-act">
            <HButton
              size="sm"
              variant="ghost"
              :disabled="!!untilText(f.manual_at, now)"
              :loading="busyIds.has(f.item_id)"
              :title="untilText(f.manual_at, now) ? '刚请求过，防抖中' : '手动请求一次探测（建一个探测任务）'"
              @click="retry([f.item_id])"
            >
              <template #icon><RotateCcw /></template>
              {{ untilText(f.manual_at, now) ? `${untilText(f.manual_at, now)}后可重试` : '重试' }}
            </HButton>
          </div>
        </li>
      </ul>
      <div class="foot">
        <HButton v-if="st.fails.length > FOLDED" size="sm" variant="ghost" @click="showAll = !showAll">
          {{ showAll ? '收起' : `展开全部 ${st.fails.length} 条` }}
        </HButton>
        <span v-if="st.fail_total > st.fails.length" class="dim">只列最近 {{ st.fails.length }} 条</span>
        <span class="dim">条目在 Emby 里已经删掉的，点重试会顺手清掉它的记录；也可以先在 Emby 里播放一次，确认能不能正常读取。</span>
      </div>
    </template>
  </SectionCard>
</template>

<style scoped>
.state {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 10px;
  margin: 0;
}
.state-item {
  padding: 10px 12px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  min-width: 0;
}
.state-item dt {
  font-size: 12px;
  color: var(--muted);
}
.state-item dd {
  margin: 4px 0 0;
  font-size: 13.5px;
  color: var(--foreground);
  word-break: break-all;
}
.state-item dd.is-busy {
  color: var(--accent);
}
.state-item dd.is-warn {
  color: var(--warning);
}
.fails {
  list-style: none;
  margin: 12px 0 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.fail {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  min-width: 0;
}
.f-main {
  flex: 1;
  min-width: 0;
}
.f-act {
  flex-shrink: 0;
}
.f-head {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 4px 8px;
  min-width: 0;
}
.f-label {
  min-width: 0;
  flex: 1 1 12em;
  font-size: 13px;
  color: var(--foreground);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.f-line {
  margin: 2px 0 0;
  font-size: 12.5px;
  color: var(--foreground);
  word-break: break-all;
}
.dim {
  font-size: 12px;
  color: var(--muted);
}
.f-line.dim {
  font-size: 12.5px;
}
.foot {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px 12px;
  margin-top: 10px;
}
@media (max-width: 720px) {
  .fail {
    flex-direction: column;
    gap: 4px;
  }
}
</style>
