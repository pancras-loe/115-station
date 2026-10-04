<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { BookOpen, Info, Play, Radio, RefreshCw } from '@lucide/vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HPopconfirm from '@/components/hero/HPopconfirm.vue'
import HModal from '@/components/hero/HModal.vue'
import HNumberInput from '@/components/hero/HNumberInput.vue'
import HHelpTip from '@/components/hero/HHelpTip.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import IncrHelp from './IncrHelp.vue'
import { syncApi } from '@/api'
import { fullTime, relTime } from '@/utils/time'
import { INCR_DEFAULTS, loadIncrCfg, patchIncrCfg } from '@/composables/incrSetting'
import { toastError, useFeedback } from '@/composables/useFeedback'
import type { StrmRuns } from './useStrmRuns'

/**
 * 增量同步页签：上面是事件流状态（「为什么没同步」先看这里），下面是轮询间隔。
 * 「立即同步」的逻辑（未保存确认）在 useStrmRuns，由 SyncPage 持有传进来。
 *
 * 本页只管轮询间隔。同一个 setting 里的 cron 是**自动整理**的调度开关，
 * 界面在「自动整理 → 基础配置」，所以这里只 patch 自己这个字段——见 incrSetting.ts
 */
defineProps<{ runs: StrmRuns }>()
const { message } = useFeedback()

const interval = ref<number | null>(INCR_DEFAULTS.interval_sec)
/** 库里那份间隔值，用来判断输入框改过没有——incr 不走 useSetting，dirty 得自己记 */
const intervalSaved = ref(INCR_DEFAULTS.interval_sec)
const saving = ref(false)
const dirty = computed(() => (interval.value ?? 0) !== intervalSaved.value)

async function saveInterval(): Promise<boolean> {
  saving.value = true
  try {
    await patchIncrCfg({ interval_sec: interval.value ?? 0 })
    intervalSaved.value = interval.value ?? 0
    message.success('保存成功')
    void loadStatus()
    return true
  } catch (e) {
    toastError(e, '保存失败')
    return false
  } finally {
    saving.value = false
  }
}

async function resetInterval() {
  interval.value = INCR_DEFAULTS.interval_sec
  await saveInterval()
}

// ---- 事件流状态 ----
const status = ref<syncApi.IncrStatus | null>(null)
const refreshing = ref(false)
const probing = ref(false)
const probeEvents = ref<syncApi.ProbeEvent[] | null>(null)

async function loadStatus() {
  refreshing.value = true
  try {
    status.value = await syncApi.incrStatus()
  } catch {
    // 状态查询失败不打扰用户：页面其余部分照常可用
  } finally {
    refreshing.value = false
  }
}

async function probe() {
  probing.value = true
  probeEvents.value = null
  try {
    const d = await syncApi.incrProbe()
    probeEvents.value = d.events
    message.success(d.message)
    void loadStatus()
  } catch (e) {
    toastError(e, '事件流测试失败')
  } finally {
    probing.value = false
  }
}

onMounted(async () => {
  void loadStatus()
  try {
    interval.value = (await loadIncrCfg()).interval_sec
    intervalSaved.value = interval.value ?? 0
  } catch {
    // 读不到就留在默认值上：保存时是 patch，不会把 cron 一起写坏
  }
})

/** 间隔填 0（或清空）= 关掉独立轮询，增量此时没有自己的时间表 */
const pollingOff = computed(() => (interval.value ?? 0) <= 0)

/** 状态格：每格一个结论 + 一句补充，tone 决定颜色 */
type Tone = 'ok' | 'warn' | 'danger' | 'muted'
const cells = computed(() => {
  const s = status.value
  if (!s) return []
  const out: { key: string; label: string; tip: string; value: string; sub?: string; tone: Tone }[] = [
    {
      key: 'gate',
      label: '事件开关',
      tip: '115 客户端里的「生活」事件记录。关掉之后接口会一直返回空，增量就此静默空转。',
      value: s.life_gate.ok ? '正常' : '异常',
      sub: [s.life_gate.message, s.life_gate.checked_at && `${s.life_gate.checked_at} 检查`].filter(Boolean).join(' · '),
      tone: s.life_gate.ok ? 'ok' : 'warn',
    },
    {
      key: 'endpoint',
      label: '当前通道',
      tip: '主通道更快但更容易被 115 限流；连续被拒会自动切到备用通道 24 小时。',
      value: s.endpoint || '—',
      tone: 'muted',
    },
    {
      key: 'interval',
      label: '轮询间隔',
      tip: '保存后的实际间隔。0 = 关闭独立轮询，只跟随自动整理执行。',
      value: s.interval_sec > 0 ? `每 ${s.interval_sec} 秒` : '已关闭',
      sub: s.interval_sec > 0 ? undefined : '跟随自动整理串行执行',
      tone: s.interval_sec > 0 ? 'ok' : 'warn',
    },
    {
      key: 'lock',
      label: '当前任务',
      tip: '整理、增量、全量、洗版、深删都排这一条队。「手动点整理提示有任务在跑」「转存完迟迟不入库」看的就是这里。',
      value: s.task_lock.busy ? '占用中' : '空闲',
      sub: [
        s.task_lock.busy ? s.task_lock.describe : '',
        s.task_lock.waiting?.length ? `${s.task_lock.waiting.join('、')} 在排队（已等 ${s.task_lock.waited_sec} 秒）` : '',
      ]
        .filter(Boolean)
        .join(' · '),
      tone: s.task_lock.busy ? 'warn' : 'ok',
    },
    {
      key: 'pending',
      label: '积压事件',
      tip: '拉回来但还没处理完的事件。持续不降说明有网盘目录一直读不出来，日志里会有提示。',
      value: `${s.pending_events} 条`,
      sub: `目录路径缓存 ${s.path_cache} 条`,
      tone: s.pending_events > 0 ? 'warn' : 'ok',
    },
    {
      key: 'stall',
      label: '重放检测',
      tip: '连续多少轮没能把事件消费掉。大于 0 时，日志里反复出现的同一批目录是重放，不是网盘上真有这么多变化。',
      value: s.stall.rounds > 0 ? `连续 ${s.stall.rounds} 轮未消费` : '正常',
      sub: s.stall.rounds > 0 ? [s.stall.reason, s.stall.since && `起于 ${s.stall.since}`].filter(Boolean).join(' · ') : '上一轮已消费',
      tone: s.stall.rounds > 0 ? 'warn' : 'ok',
    },
  ]
  return out
})

const helpVisible = ref(false)
</script>

<template>
  <div class="tab-body">
    <SectionCard title="事件流状态" hint="「为什么没同步」先看这里；顶部状态条每 15 秒自动刷新">
      <template #extra>
        <div class="head-btns">
          <HButton size="sm" variant="tertiary" :loading="probing" @click="void probe()">
            <template #icon><Radio :size="14" /></template>
            测试事件流
          </HButton>
          <HButton size="sm" variant="ghost" :loading="refreshing" aria-label="刷新状态" @click="void loadStatus()">
            <template #icon><RefreshCw :size="14" /></template>
            刷新
          </HButton>
        </div>
      </template>

      <template v-if="status">
        <div class="cells">
          <div v-for="c in cells" :key="c.key" class="cell" :class="`tone-${c.tone}`">
            <div class="cell-label">{{ c.label }}<HHelpTip :text="c.tip" /></div>
            <div class="cell-value"><span class="cell-dot" />{{ c.value }}</div>
            <div v-if="c.sub" class="cell-sub" :title="c.sub">{{ c.sub }}</div>
          </div>
        </div>

        <!-- 上一轮：信息最多，单独一整行 -->
        <div class="round" :class="{ 'is-error': status.last_round.error }">
          <div class="round-head">
            <span class="round-label">上一轮</span>
            <template v-if="status.last_round.at">
              <span class="round-at" :title="fullTime(status.last_round.at)">{{ relTime(status.last_round.at) }}</span>
              <span v-if="status.last_round.summary" class="round-no">#{{ status.last_round.summary.round }}</span>
              <span class="round-tip">轮次号与日志里的 [同步#N] 对应</span>
            </template>
            <span v-else class="round-at">还没跑过</span>
          </div>
          <div v-if="status.last_round.error" class="round-err">{{ status.last_round.error }}</div>
          <div v-else-if="status.last_round.summary" class="round-stats">
            <span><b>{{ status.last_round.summary.events_pending }}</b>待处理</span>
            <span><b>{{ status.last_round.summary.strm_created }}</b>新增 STRM</span>
            <span><b>{{ status.last_round.summary.strm_existing }}</b>已存在</span>
            <span><b>{{ status.last_round.summary.deleted }}</b>清理</span>
            <span><b>{{ status.last_round.summary.moved }}</b>移动 / 改名</span>
            <span><b>{{ status.last_round.summary.list_calls }}</b>次列目录</span>
            <span><b>{{ status.last_round.summary.elapsed }}</b>用时</span>
          </div>
          <div v-if="status.last_round.summary && !status.last_round.summary.consumed" class="round-warn">
            未消费（{{ status.last_round.summary.not_consumed }}），下轮原样重来
          </div>
        </div>

        <div v-if="probeEvents" class="probe">
          <div v-if="!probeEvents.length" class="dim">最近没有任何事件。</div>
          <div v-for="e in probeEvents" :key="e.id" class="probe-row" :class="{ dim: e.ignored }">
            <span class="probe-at">{{ e.at }}</span>
            <span class="probe-kind">{{ e.kind }}</span>
            <span class="probe-type">{{ e.type }}</span>
            <span class="probe-name">{{ e.name }}</span>
            <span v-if="e.ignored" class="probe-skip">不处理</span>
          </div>
          <div class="dim probe-tip">
            只读取不处理：不推进游标、不落库、不动本地文件。标「不处理」的是浏览/标星类事件。
          </div>
        </div>
      </template>
      <div v-else class="dim">正在读取状态…</div>
    </SectionCard>

    <SectionCard title="轮询设置" hint="独立轮询，只管网盘端的外部变更">
      <template #extra>
        <HButton size="sm" variant="ghost" @click="helpVisible = true">
          <template #icon><BookOpen :size="14" /></template>
          功能介绍
        </HButton>
      </template>

      <p class="pre">
        <Info :size="14" />
        前提：115 生活 App 里开启「最近」（生活事件），并且先完整执行过一次全量同步。
      </p>
      <p class="pre">
        <Info :size="14" />
        <span>
          增量新增的片目要自动刮削 NFO / 海报，到
          <RouterLink class="jump" :to="{ name: 'scrape' }">影视刮削</RouterLink>
          打开「同步后自动刮削」。
        </span>
      </p>

      <FieldRow
        label="增量同步间隔"
        tip="每隔这么久拉一次 115 生活事件，把网盘端的变化落到本地 STRM。一轮通常只发 1~2 个请求，没有新事件时完全静默，所以跑得勤的代价很低：30 秒一轮意味着手机上传的片子最多半分钟就能进媒体库。"
        hint="0 = 关闭独立轮询；最小 15 秒，低于 15 按 15 处理"
      >
        <HNumberInput v-model="interval" :min="0" :step="15" aria-label="增量同步间隔">
          <template #suffix>秒</template>
        </HNumberInput>
      </FieldRow>

      <HAlert v-if="pollingOff" class="note-top" status="warning" title="独立轮询已关闭">
        增量现在没有自己的时间表了：只有「自动整理」的 cron 命中时，才会在整理跑完后顺带执行一次。
        手机上传、网页端的删除与改名要等到下一次整理才会反映到本地；
        <strong>那条 cron 留空的话，增量就完全不会自动执行</strong>，只能靠下面的「立即同步」手点。
        <div class="note-act">
          <RouterLink class="jump" to="/organize">去看自动整理的 cron →</RouterLink>
        </div>
      </HAlert>

      <FormActions>
        <HButton variant="primary" :loading="saving" :disabled="!dirty && !saving" @click="void saveInterval()">保存配置</HButton>
        <HPopconfirm confirm-text="开始" :disabled="runs.busyIncr.value" @confirm="void runs.runIncr()">
          <HButton variant="secondary" :disabled="runs.busyIncr.value" :loading="runs.runningIncr.value">
            <template #icon><Play :size="14" /></template>
            立即同步
          </HButton>
          <template #content>立即执行一次增量同步？</template>
        </HPopconfirm>
        <HButton variant="tertiary" @click="void resetInterval()">重置为 {{ INCR_DEFAULTS.interval_sec }} 秒</HButton>
      </FormActions>
    </SectionCard>

    <!-- 弹窗必须留在这个根元素里：本页整体被 SyncPage 的 <Transition> 包着，
         多个根节点会让 Transition 找不到唯一子元素，整页渲染成空白 -->
    <HModal v-model:show="helpVisible" title="增量同步是怎么回事" width="860px">
      <IncrHelp />
    </HModal>
  </div>
</template>

<style scoped>
.tab-body {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.head-btns {
  display: flex;
  gap: 6px;
}
.dim {
  font-size: 12px;
  color: var(--muted);
}

/* ---- 状态格 ---- */
.cells {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 10px;
}
.cell {
  --tone: var(--success);
  min-width: 0;
  padding: 12px 14px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.tone-warn {
  --tone: var(--warning);
}
.tone-danger {
  --tone: var(--danger);
}
.tone-muted {
  --tone: var(--muted);
}
.cell-label {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
  color: var(--muted);
}
.cell-value {
  display: flex;
  align-items: center;
  gap: 7px;
  margin-top: 4px;
  font-size: 15px;
  font-weight: 600;
  color: var(--foreground);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.tone-warn .cell-value {
  color: var(--warning-soft-foreground);
}
.cell-dot {
  width: 7px;
  height: 7px;
  flex-shrink: 0;
  border-radius: 999px;
  background: var(--tone);
}
.tone-muted .cell-dot {
  display: none;
}
.cell-sub {
  margin-top: 2px;
  font-size: 12px;
  color: var(--muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* ---- 上一轮 ---- */
.round {
  margin-top: 10px;
  padding: 12px 14px;
  border-radius: var(--r-lg);
  box-shadow: inset 0 0 0 1px var(--border);
}
.round.is-error {
  box-shadow: inset 0 0 0 1px color-mix(in oklab, var(--danger) 45%, transparent);
}
.round-head {
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: 4px 10px;
}
.round-label {
  font-size: 12px;
  color: var(--muted);
}
.round-at {
  font-size: 14px;
  font-weight: 600;
  color: var(--foreground);
}
.round-no {
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--muted);
}
.round-tip {
  margin-left: auto;
  font-size: 11.5px;
  color: var(--muted);
}
.round-stats {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 20px;
  margin-top: 8px;
  font-size: 12px;
  color: var(--muted);
}
.round-stats b {
  margin-right: 4px;
  font-size: 14px;
  font-weight: 600;
  color: var(--foreground);
  font-variant-numeric: tabular-nums;
}
.round-err {
  margin-top: 6px;
  font-size: 12.5px;
  color: var(--danger);
}
.round-warn {
  margin-top: 6px;
  font-size: 12.5px;
  color: var(--warning-soft-foreground);
}

/* ---- 事件探针 ---- */
.probe {
  margin-top: 10px;
  padding: 10px 14px;
  border-radius: 16px;
  background: var(--surface-secondary);
}
.probe-row {
  display: flex;
  gap: 10px;
  align-items: baseline;
  font-size: 12px;
  padding: 3px 0;
}
.probe-row.dim {
  opacity: 0.5;
}
.probe-at {
  color: var(--muted);
  flex-shrink: 0;
  font-variant-numeric: tabular-nums;
}
.probe-kind,
.probe-type {
  color: var(--muted);
  flex-shrink: 0;
}
.probe-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.probe-skip {
  margin-left: auto;
  color: var(--muted);
  flex-shrink: 0;
}
.probe-tip {
  margin-top: 8px;
}

/* ---- 轮询设置 ---- */
.pre {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: 0 0 12px;
  font-size: 12.5px;
  line-height: 1.6;
  color: var(--muted);
}
.pre :deep(svg) {
  flex-shrink: 0;
  margin-top: 3px;
}
.note-top {
  margin-top: 14px;
}
.jump {
  font-size: 13px;
  font-weight: 500;
  color: var(--accent);
  text-decoration: none;
}
.jump:hover {
  text-decoration: underline;
}
.note-act {
  margin-top: 8px;
}

@media (max-width: 1080px) {
  .cells {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .round-tip {
    display: none;
  }
}
/* 手机：事件探针一行放不下时间 + 类型 + 名字，名字折到下一行 */
@media (max-width: 720px) {
  .probe-row {
    flex-wrap: wrap;
  }
  .probe-name {
    flex-basis: 100%;
    white-space: normal;
    word-break: break-all;
  }
}
</style>
