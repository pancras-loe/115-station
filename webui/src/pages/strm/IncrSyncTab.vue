<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HModal from '@/components/hero/HModal.vue'
import HNumberInput from '@/components/hero/HNumberInput.vue'
import HPopconfirm from '@/components/hero/HPopconfirm.vue'
import { RouterLink } from 'vue-router'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import IncrHelp from './IncrHelp.vue'
import { syncApi } from '@/api'
import type { FullSetting } from './fullSetting'
import { INCR_DEFAULTS, loadIncrCfg, patchIncrCfg } from '@/composables/incrSetting'
import { useQueueStore } from '@/stores/queue'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { UNSAVED_NOTE, confirmUnsaved } from '@/composables/confirmUnsaved'

const props = defineProps<{ full: FullSetting }>()

const { message } = useFeedback()
const queue = useQueueStore()

/**
 * 本页只管轮询间隔。同一个 setting 里的 cron 是**自动整理**的调度开关，
 * 界面在「自动整理 → 基础配置」，所以这里只 patch 自己这个字段——见 incrSetting.ts
 */
const interval = ref<number | null>(INCR_DEFAULTS.interval_sec)
/** 库里那份间隔值，用来判断输入框改过没有——incr 不走 useSetting，dirty 得自己记 */
const intervalSaved = ref(INCR_DEFAULTS.interval_sec)
const saving = ref(false)
const running = ref(false)

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

/** 媒体库配置（全量页那份，两页共用）和本页的轮询间隔，任一改过没保存都要拦 */
const dirty = computed(
  () => props.full.dirty.value || (interval.value ?? 0) !== intervalSaved.value,
)

async function saveDirty(): Promise<boolean> {
  if (props.full.dirty.value && !(await props.full.save())) return false
  if ((interval.value ?? 0) !== intervalSaved.value) return await saveInterval()
  return true
}

async function resetInterval() {
  interval.value = INCR_DEFAULTS.interval_sec
  await saveInterval()
}

/** 按钮上的 popconfirm 文案；配置改过时这个气泡不弹，由未保存确认框接管 */
const runHint = '确定立即执行一次增量同步？'

async function runIncremental() {
  // 确认框先弹：选「直接开始」时这次跑的是库里那份媒体库配置，
  // 校验和请求体都得照着那份来（定时轮询读的也是它）
  let cfg = props.full.model.value
  if (dirty.value) {
    if (!(await confirmUnsaved(UNSAVED_NOTE, saveDirty))) return
    // 保存成功后 dirty 归零，界面值即已保存值；仍然脏 = 用户选了「直接开始」
    if (dirty.value) cfg = props.full.saved.value
  }
  if (!cfg.cid || cfg.cid === '0') {
    message.error('请先到「账号与媒体库」配置并保存 115 媒体库目录')
    return
  }

  running.value = true
  try {
    const d = await syncApi.runIncremental({
      cid: cfg.cid,
      local_path: cfg.local_path,
      video_ext: cfg.video_ext,
      image_ext: cfg.image_ext,
      data_ext: cfg.data_ext,
    })
    message.success(d.message)
    await queue.submitted(d.job_id)
  } catch (e) {
    toastError(e, '提交增量同步失败')
  } finally {
    running.value = false
  }
}

// ---- 事件流状态 ----
const status = ref<syncApi.IncrStatus | null>(null)
const probing = ref(false)
const probeEvents = ref<syncApi.ProbeEvent[] | null>(null)

async function loadStatus() {
  try {
    status.value = await syncApi.incrStatus()
  } catch {
    // 状态查询失败不打扰用户：页面其余部分照常可用
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

/** 队列里已有手动增量（排队或执行中）：再点也只是合并成同一个任务 */
const busy = computed(() => running.value || !!queue.activeManualOf('incr'))

/** 间隔填 0（或清空）= 关掉独立轮询，增量此时没有自己的时间表 */
const pollingOff = computed(() => (interval.value ?? 0) <= 0)

const helpVisible = ref(false)
</script>

<template>
  <div class="tab-body">
    <SectionCard title="增量同步" hint="独立轮询，只管网盘端的外部变更">
      <template #extra>
        <HButton size="sm" variant="ghost" @click="helpVisible = true">功能介绍</HButton>
      </template>

      <HAlert class="note" status="warning">
        增量同步前需开启 115 生活 APP 中的「最近」（生活事件必须开启），且必须先执行一次全量同步。
      </HAlert>

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
        <strong>那条 cron 留空的话，增量就完全不会自动执行</strong>，只能靠本页的「开始增量同步」手点。
        <div class="note-act">
          <RouterLink class="jump" to="/organize">去看自动整理的 cron →</RouterLink>
        </div>
      </HAlert>

      <FormActions>
        <HButton variant="primary" :loading="saving" @click="void saveInterval()">保存配置</HButton>
        <!-- 改过没保存时走 runIncremental 里的确认框，那里已经问过一次，别再叠一层 popconfirm -->
        <HPopconfirm v-if="!dirty" confirm-text="开始" :disabled="busy" @confirm="void runIncremental()">
          <HButton variant="secondary" :disabled="busy" :loading="running">开始增量同步</HButton>
          <template #content>{{ runHint }}</template>
        </HPopconfirm>
        <HButton v-else variant="secondary" :disabled="busy" :loading="running" @click="void runIncremental()">
          开始增量同步
        </HButton>
        <HButton variant="tertiary" :disabled="busy" @click="void resetInterval()">重置配置</HButton>
      </FormActions>
    </SectionCard>

    <SectionCard title="事件流状态" hint="「为什么没同步」先看这里">
      <template v-if="status">
        <FieldRow label="事件开关" tip="115 客户端里的「生活」事件记录。关掉之后接口会一直返回空，增量就此静默空转。">
          <span class="st-line">
            <HChip :color="status.life_gate.ok ? 'success' : 'warning'">
              {{ status.life_gate.ok ? '正常' : '异常' }}
            </HChip>
          <span class="st-note">{{ status.life_gate.message }}</span>
          <span v-if="status.life_gate.checked_at" class="st-dim">（{{ status.life_gate.checked_at }} 检查）</span>
          </span>
        </FieldRow>

        <FieldRow label="当前通道" tip="主通道更快但更容易被 115 限流；连续被拒会自动切到备用通道 24 小时。">
          <span class="st-note">{{ status.endpoint }}</span>
        </FieldRow>

        <FieldRow label="轮询间隔">
          <span class="st-note">{{ status.interval_sec > 0 ? `每 ${status.interval_sec} 秒一轮` : '已关闭（跟随自动整理串行执行）' }}</span>
        </FieldRow>

        <FieldRow
          label="当前任务"
          tip="整理、增量、全量、洗版、深删都排这一条队。「手动点整理提示有任务在跑」「转存完迟迟不入库」看的就是这里。"
        >
          <span class="st-line">
            <HChip :color="status.task_lock.busy ? 'warning' : 'success'">
              {{ status.task_lock.busy ? '占用中' : '空闲' }}
            </HChip>
          <span class="st-note">{{ status.task_lock.describe }}</span>
          <span v-if="status.task_lock.waiting?.length" class="st-warn">
            · {{ status.task_lock.waiting.join('、') }} 在排队（已等 {{ status.task_lock.waited_sec }} 秒）
          </span>
          </span>
        </FieldRow>

        <FieldRow label="上一轮" tip="每一轮都会记录，包括没找到任何变动的空转轮次。轮次号与日志里的 [同步#N] 对应。">
          <template v-if="status.last_round.at">
            <span class="st-note">{{ status.last_round.at }}</span>
            <span v-if="status.last_round.summary" class="st-dim">#{{ status.last_round.summary.round }}</span>
            <span v-if="status.last_round.error" class="st-err">{{ status.last_round.error }}</span>
            <template v-else-if="status.last_round.summary">
              <span class="st-dim">
                待处理 {{ status.last_round.summary.events_pending }} ·
                新增 STRM {{ status.last_round.summary.strm_created }}（已存在 {{ status.last_round.summary.strm_existing }}） ·
                清理 {{ status.last_round.summary.deleted }} ·
                移动/改名 {{ status.last_round.summary.moved }} ·
                列目录 {{ status.last_round.summary.list_calls }} 次 ·
                用时 {{ status.last_round.summary.elapsed }}
              </span>
              <span v-if="!status.last_round.summary.consumed" class="st-warn">
                · 未消费（{{ status.last_round.summary.not_consumed }}），下轮原样重来
              </span>
            </template>
          </template>
          <span v-else class="st-dim">还没跑过</span>
        </FieldRow>

        <FieldRow
          label="重放检测"
          tip="连续多少轮没能把事件消费掉。大于 0 时，日志里反复出现的同一批目录是重放，不是网盘上真有这么多变化。"
        >
          <template v-if="status.stall.rounds > 0">
            <span class="st-warn">已连续 {{ status.stall.rounds }} 轮未消费</span>
            <span class="st-dim">· {{ status.stall.reason }}</span>
            <span v-if="status.stall.since" class="st-dim">· 起于 {{ status.stall.since }}</span>
          </template>
          <span v-else class="st-note">正常（上一轮已消费）</span>
        </FieldRow>

        <FieldRow label="积压事件" tip="拉回来但还没处理完的事件。持续不降说明有网盘目录一直读不出来，日志里会有提示。">
          <span :class="status.pending_events > 0 ? 'st-warn' : 'st-note'">{{ status.pending_events }} 条</span>
          <span class="st-dim">· 目录路径缓存 {{ status.path_cache }} 条</span>
        </FieldRow>

        <FormActions>
          <HButton variant="tertiary" :loading="probing" @click="void probe()">测试事件流</HButton>
          <HButton variant="ghost" @click="void loadStatus()">刷新状态</HButton>
        </FormActions>

        <div v-if="probeEvents" class="probe">
          <div v-if="!probeEvents.length" class="st-dim">最近没有任何事件。</div>
          <div v-for="e in probeEvents" :key="e.id" class="probe-row" :class="{ dim: e.ignored }">
            <span class="probe-at">{{ e.at }}</span>
            <span class="probe-kind">{{ e.kind }}</span>
            <span class="probe-type">{{ e.type }}</span>
            <span class="probe-name">{{ e.name }}</span>
            <span v-if="e.ignored" class="probe-skip">不处理</span>
          </div>
          <div class="st-dim probe-tip">
            只读取不处理：不推进游标、不落库、不动本地文件。标「不处理」的是浏览/标星类事件。
          </div>
        </div>
      </template>
      <div v-else class="st-dim">正在读取状态…</div>
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
.note {
  margin-bottom: 10px;
}
.st-line {
  display: inline-flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 4px 8px;
}
.st-note {
  font-size: 13px;
  color: var(--foreground);
}
.st-dim {
  font-size: 12px;
  color: var(--muted);
  margin-left: 6px;
}
.st-warn {
  font-size: 13px;
  color: var(--warning-soft-foreground);
}
.st-err {
  font-size: 12px;
  color: var(--danger);
  margin-left: 6px;
}
.probe {
  margin-top: 14px;
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
  margin-left: 0;
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
