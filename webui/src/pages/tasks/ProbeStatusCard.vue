<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import { tasksApi } from '@/api'
import type { ProbeStatus } from '@/api/tasks'
import { probeRetryText } from '@/utils/jobStatus'
import { fullTime, relTime } from '@/utils/time'

/**
 * 任务中心 · Emby 提前探测的全局状态（GET /tasks/probe）。
 * 探测不在任务队列里跑：入库确认排进来的没有任务可挂，失败了只能在这里（和片目详情里）看到。
 * 手动刮削排进来的，结果另外写回那个刮削任务的详情
 */
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

/** 没开、没配、也没有历史失败的：整张卡片不显示，别占地方 */
const visible = computed(() => {
  const s = st.value
  return !!s && (s.enabled || s.queue > 0 || !!s.running || s.fail_total > 0)
})

const stateText = computed(() => {
  const s = st.value
  if (!s) return ''
  if (!s.emby) return '没有配置 Emby'
  if (s.paused_until && new Date(s.paused_until).getTime() > Date.now())
    return `连续失败，暂停到 ${fullTime(s.paused_until)}（可能是 115 风控或 Emby 异常）`
  if (s.running) return `正在探测：${s.running}${s.queue ? ` · 还有 ${s.queue} 个片目排队` : ''}`
  if (s.queue) return `${s.queue} 个片目排队中`
  return s.enabled ? '空闲' : '入库后自动探测未开启（手动刮削勾了「轨道探测」时照样会探）'
})
const stateWarn = computed(() => !!st.value?.paused_until || st.value?.emby === false)

const rows = computed(() => {
  const all = st.value?.fails ?? []
  return showAll.value ? all : all.slice(0, FOLDED)
})
const waiting = computed(() => (st.value?.fails ?? []).filter((f) => !f.final && !f.running).length)
const exhausted = computed(() => (st.value?.fails ?? []).filter((f) => f.final).length)

let timer: number | undefined
onMounted(() => {
  void load()
  timer = window.setInterval(load, 15_000)
})
onUnmounted(() => {
  if (timer !== undefined) clearInterval(timer)
})
</script>

<template>
  <SectionCard
    v-if="visible && st"
    title="Emby 提前探测"
    :hint="`入库后请 Emby 先把音视频信息探好；每个视频最多请求 ${st.limits.max_attempts} 次、两次至少隔 ${st.limits.retry_hours} 小时，每次都要取一次 115 直链`"
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
            共 {{ st.fail_total }} 个<span v-if="waiting"> · 冷却中 {{ waiting }}</span
            ><span v-if="exhausted"> · 已放弃 {{ exhausted }}</span>
          </template>
          <template v-else>没有</template>
        </dd>
      </div>
    </dl>

    <template v-if="st.fails.length">
      <ul class="fails">
        <li v-for="f in rows" :key="f.item_id" class="fail">
          <div class="f-head">
            <HChip size="sm" :color="f.running ? 'accent' : f.final ? 'danger' : 'warning'">
              {{ f.running ? '探测中' : f.final ? '已放弃' : '冷却中' }}
            </HChip>
            <span class="f-label" :title="f.item_id">{{ f.label || `Emby 条目 ${f.item_id}` }}</span>
            <span class="dim" :title="fullTime(f.last_at)">{{ relTime(f.last_at) }} · 已请求 {{ f.attempts }} 次</span>
          </div>
          <p v-if="f.last_err && !f.running" class="f-line">原因：{{ f.last_err }}</p>
          <p v-if="!f.running" class="f-line dim">{{ probeRetryText(f) }}</p>
        </li>
      </ul>
      <div class="foot">
        <HButton v-if="st.fails.length > FOLDED" size="sm" variant="ghost" @click="showAll = !showAll">
          {{ showAll ? '收起' : `展开全部 ${st.fails.length} 条` }}
        </HButton>
        <span v-if="st.fail_total > st.fails.length" class="dim">只列最近 {{ st.fails.length }} 条</span>
        <span class="dim">
          失败记录 {{ st.limits.prune_days }} 天后自动清掉，之后会再给一次机会；想马上确认能不能读，可以先在 Emby 里播放一次
        </span>
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
  min-width: 0;
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
</style>
