<script setup lang="ts">
import { computed, ref } from 'vue'
import { EyeOff, RotateCcw } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HPopconfirm from '@/components/hero/HPopconfirm.vue'
import { tasksApi } from '@/api'
import type { ProbeFailRow, ProbeStatus } from '@/api/tasks'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { untilText, useNow } from '@/composables/useNow'
import { useQueueStore } from '@/stores/queue'
import { fullTime, relTime } from '@/utils/time'

/**
 * 任务中心 · Emby 提前探测页签（GET /tasks/probe，状态由 TasksPage 统一轮询后传进来）。
 * 入库确认排进来的自动探测没有任务可挂，失败了只能在这里（和片目详情里）看到，也在这里手动重试或忽略。
 * 手动探测（片目详情、刮削、重新整理）各自是一个「Emby 探测」任务，进度在「进行中」页签。
 *
 * 忽略不删记账（后端只打 ignored_at）：删了自动入口的次数就清零，下次入库确认又会再请求两次 115 直链
 */
const props = defineProps<{ st: ProbeStatus | null }>()
const emit = defineEmits<{ changed: [] }>()
const queue = useQueueStore()
const { message, dialog } = useFeedback()
const now = useNow()

const paused = computed(() => {
  const t = props.st?.paused_until
  return t && new Date(t).getTime() > now.value ? t : ''
})

const stateText = computed(() => {
  const s = props.st
  if (!s) return ''
  if (!s.emby) return '没有配置 Emby'
  if (paused.value) return `连续失败，暂停到 ${fullTime(paused.value)}（可能是 115 风控或 Emby 异常），之后自动继续`
  if (s.running) return `正在探测：${s.running}${s.queue ? ` · 还有 ${s.queue} 个片目排队` : ''}`
  if (s.queue) return `${s.queue} 个片目排队中`
  return s.enabled ? '空闲' : '入库后自动探测未开启（手动探测照常可用）'
})
const stateWarn = computed(() => !!paused.value || props.st?.emby === false)

const fails = computed(() => props.st?.fails ?? [])
const stopped = computed(() => fails.value.filter((f) => f.auto_stopped).length)
/** 现在就能手动重试的（不在探、不在排、防抖已过） */
const retryIds = computed(() =>
  fails.value.filter((f) => !f.running && !f.queued && !untilText(f.manual_at, now.value)).map((f) => f.item_id),
)
const idle = (f: ProbeFailRow) => !f.running && !f.queued

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

const busy = ref(new Set<string>())
const bulkBusy = ref(false)
function mark(ids: string[], on: boolean) {
  const s = new Set(busy.value)
  ids.forEach((id) => (on ? s.add(id) : s.delete(id)))
  busy.value = s
}

async function retry(ids: string[]) {
  mark(ids, true)
  try {
    const d = await tasksApi.retryProbe(ids)
    message.success(`${d.message}，进度在「进行中」`)
    await queue.submitted(d.job_id)
    emit('changed')
  } catch (e) {
    toastError(e, '重试失败')
  } finally {
    mark(ids, false)
  }
}

async function retryAll() {
  const ids = retryIds.value
  if (!ids.length) return
  const ok = await dialog.confirm({
    title: '全部重试',
    content: `为 ${ids.length} 个视频各请求一次 Emby 探测（建一个探测任务，逐个进行、间隔 3 秒）。每次都要取一次 115 直链，数量多时建议分批或先在 Emby 里播放确认能读。`,
    tone: 'warning',
    actions: [
      { label: '取消', value: false, variant: 'tertiary' },
      { label: `重试 ${ids.length} 个`, value: true, variant: 'primary' },
    ],
  })
  if (!ok) return
  bulkBusy.value = true
  try {
    await retry(ids)
  } finally {
    bulkBusy.value = false
  }
}

async function ignore(body: { item_ids?: string[]; all?: boolean }) {
  const ids = body.item_ids ?? []
  mark(ids, true)
  if (body.all) bulkBusy.value = true
  try {
    const d = await tasksApi.ignoreProbe(body)
    message.success(d.message)
    emit('changed')
  } catch (e) {
    toastError(e, '忽略失败')
  } finally {
    mark(ids, false)
    bulkBusy.value = false
  }
}

async function unignore() {
  try {
    const d = await tasksApi.unignoreProbe()
    message.success(d.message)
    emit('changed')
  } catch (e) {
    toastError(e, '撤销失败')
  }
}
</script>

<template>
  <div v-if="st" class="stack">
    <SectionCard
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
        <div class="state-item">
          <dt>已忽略</dt>
          <dd>
            <template v-if="st.ignored_total">
              {{ st.ignored_total }} 个，不再自动探
              <HPopconfirm confirm-text="撤销" @confirm="unignore">
                <button type="button" class="link">撤销全部</button>
                <template #content>把 {{ st.ignored_total }} 个已忽略的视频放回失败清单，自动探测按原来的次数与间隔照常判定。</template>
              </HPopconfirm>
            </template>
            <template v-else>没有</template>
          </dd>
        </div>
      </dl>
    </SectionCard>

    <SectionCard title="失败清单" :hint="fails.length ? '条目在 Emby 里已经删掉的，点重试会顺手清掉它的记录；也可以先在 Emby 里播放一次，确认能不能正常读取' : ''">
      <template v-if="fails.length" #extra>
        <div class="extra">
          <HButton size="sm" variant="secondary" :disabled="!retryIds.length" :loading="bulkBusy" @click="retryAll">
            <template #icon><RotateCcw /></template>
            全部重试<span v-if="retryIds.length" class="n">{{ retryIds.length }}</span>
          </HButton>
          <HPopconfirm confirm-text="忽略" @confirm="ignore({ all: true })">
            <HButton size="sm" variant="ghost" :disabled="bulkBusy">
              <template #icon><EyeOff /></template>
              全部忽略
            </HButton>
            <template #content>
              把 {{ st.fail_total }} 个失败的视频从清单里拿掉，之后也不再自动探测（正在探测和排队的不动）。片目详情里仍可手动探测，手动再失败会重新出现在这里。
            </template>
          </HPopconfirm>
        </div>
      </template>

      <EmptyState v-if="!fails.length" text="没有探测失败的视频" />
      <ul v-else class="fails">
        <li v-for="f in fails" :key="f.item_id" class="fail">
          <div class="f-main">
            <div class="f-head">
              <HChip size="sm" :color="chip(f).color">{{ chip(f).text }}</HChip>
              <span class="f-label" :title="f.item_id">{{ f.label || `Emby 条目 ${f.item_id}` }}</span>
              <span class="dim" :title="fullTime(f.last_at)">{{ relTime(f.last_at) }} · 已请求 {{ f.attempts }} 次</span>
            </div>
            <template v-if="idle(f)">
              <p v-if="f.last_err" class="f-line" :title="f.last_err">原因：{{ f.last_err }}</p>
              <p class="f-line dim">{{ autoText(f) }}</p>
            </template>
          </div>
          <div v-if="idle(f)" class="f-act">
            <HButton
              size="sm"
              variant="ghost"
              :disabled="!!untilText(f.manual_at, now)"
              :loading="busy.has(f.item_id)"
              :title="untilText(f.manual_at, now) ? '刚请求过，防抖中' : '手动请求一次探测（建一个探测任务）'"
              @click="retry([f.item_id])"
            >
              <template #icon><RotateCcw /></template>
              {{ untilText(f.manual_at, now) ? `${untilText(f.manual_at, now)}后可重试` : '重试' }}
            </HButton>
            <HButton
              size="sm"
              variant="ghost"
              title="从清单里拿掉，不再自动探测；片目详情里仍可手动探测"
              :disabled="busy.has(f.item_id)"
              @click="ignore({ item_ids: [f.item_id] })"
            >
              <template #icon><EyeOff /></template>
              忽略
            </HButton>
          </div>
        </li>
      </ul>
      <p v-if="st.fail_total > fails.length" class="foot dim">只列最近 {{ fails.length }} 条，处理掉这些后会列出更早的。</p>
    </SectionCard>
  </div>
  <EmptyState v-else text="正在读取探测状态…" />
</template>

<style scoped>
.stack {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.state {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
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
.link {
  margin-left: 6px;
  padding: 0;
  border: 0;
  background: none;
  cursor: pointer;
  font: inherit;
  font-size: 12.5px;
  color: var(--accent);
}
.link:hover {
  text-decoration: underline;
}
.n {
  margin-left: 4px;
  opacity: 0.75;
}
.extra {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
}
.fails {
  list-style: none;
  margin: 0;
  padding: 0;
}
.fail {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  min-width: 0;
  padding: 10px 4px;
  border-top: 1px solid var(--separator);
}
.fail:first-child {
  border-top: 0;
  padding-top: 0;
}
.f-main {
  flex: 1;
  min-width: 0;
}
.f-act {
  flex-shrink: 0;
  display: flex;
  gap: 2px;
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
  margin: 10px 0 0;
}
@media (max-width: 720px) {
  .fail {
    flex-direction: column;
    gap: 4px;
  }
  .f-act {
    align-self: flex-end;
  }
}
</style>
