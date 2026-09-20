<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NAlert, NButton, NInputNumber, NModal, NPopconfirm, NTag } from 'naive-ui'
import { RouterLink } from 'vue-router'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import IncrHelp from './IncrHelp.vue'
import { syncApi } from '@/api'
import type { FullSetting } from './fullSetting'
import { INCR_DEFAULTS, loadIncrCfg, patchIncrCfg } from '@/composables/incrSetting'
import { useTaskStore } from '@/stores/task'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { UNSAVED_NOTE, confirmUnsaved } from '@/composables/confirmUnsaved'

const props = defineProps<{ full: FullSetting }>()

const { message } = useFeedback()
const task = useTaskStore()

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
  message.info('增量同步进行中…')
  task.poll()
  try {
    const d = await syncApi.runIncremental({
      cid: cfg.cid,
      local_path: cfg.local_path,
      video_ext: cfg.video_ext,
      image_ext: cfg.image_ext,
      data_ext: cfg.data_ext,
    })
    const s = d.summary
    message.success(
      `增量同步完成（${s.elapsed}）：事件 ${s.events_total}，视频 ${s.videos}，` +
        `生成 STRM ${s.strm_created}，附属文件下载 ${s.assets_downloaded}`,
    )
  } catch (e) {
    toastError(e, '增量同步失败')
  } finally {
    running.value = false
    task.poll()
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

const busy = computed(() => running.value || task.status.running)

/** 间隔填 0（或清空）= 关掉独立轮询，增量此时没有自己的时间表 */
const pollingOff = computed(() => (interval.value ?? 0) <= 0)

const helpVisible = ref(false)
</script>

<template>
  <div class="tab-body">
    <SectionCard title="增量同步" hint="独立轮询，只管网盘端的外部变更">
      <template #extra>
        <NButton size="small" quaternary @click="helpVisible = true">功能介绍</NButton>
      </template>

      <NAlert class="note" type="warning" :bordered="false">
        增量同步前需开启 115 生活 APP 中的「最近」（生活事件必须开启），且必须先执行一次全量同步。
      </NAlert>

      <FieldRow
        label="增量同步间隔"
        tip="每隔这么久拉一次 115 生活事件，把网盘端的变化落到本地 STRM。一轮通常只发 1~2 个请求，没有新事件时完全静默，所以跑得勤的代价很低：30 秒一轮意味着手机上传的片子最多半分钟就能进媒体库。"
        hint="0 = 关闭独立轮询；最小 15 秒，低于 15 按 15 处理"
      >
        <NInputNumber v-model:value="interval" :min="0" :step="15" style="width: 160px">
          <template #suffix>秒</template>
        </NInputNumber>
      </FieldRow>

      <NAlert v-if="pollingOff" class="note-top" type="warning" :bordered="false" title="独立轮询已关闭">
        增量现在没有自己的时间表了：只有「自动整理」的 cron 命中时，才会在整理跑完后顺带执行一次。
        手机上传、网页端的删除与改名要等到下一次整理才会反映到本地；
        <strong>那条 cron 留空的话，增量就完全不会自动执行</strong>，只能靠本页的「开始增量同步」手点。
        <div class="note-act">
          <RouterLink class="jump" to="/organize">去看自动整理的 cron →</RouterLink>
        </div>
      </NAlert>

      <FormActions>
        <NButton type="primary" :loading="saving" @click="void saveInterval()">保存配置</NButton>
        <!-- 改过没保存时走 runIncremental 里的确认框，那里已经问过一次，别再叠一层 popconfirm -->
        <NPopconfirm v-if="!dirty" @positive-click="void runIncremental()">
          <template #trigger>
            <NButton type="primary" ghost :disabled="busy" :loading="running">开始增量同步</NButton>
          </template>
          {{ runHint }}
        </NPopconfirm>
        <NButton
          v-else
          type="primary"
          ghost
          :disabled="busy"
          :loading="running"
          @click="void runIncremental()"
        >
          开始增量同步
        </NButton>
        <NButton :disabled="busy" @click="void resetInterval()">重置配置</NButton>
      </FormActions>
    </SectionCard>

    <SectionCard title="事件流状态" hint="「为什么没同步」先看这里">
      <template v-if="status">
        <FieldRow label="事件开关" tip="115 客户端里的「生活」事件记录。关掉之后接口会一直返回空，增量就此静默空转。">
          <NTag :type="status.life_gate.ok ? 'success' : 'warning'" size="small" round>
            {{ status.life_gate.ok ? '正常' : '异常' }}
          </NTag>
          <span class="st-note">{{ status.life_gate.message }}</span>
          <span v-if="status.life_gate.checked_at" class="st-dim">（{{ status.life_gate.checked_at }} 检查）</span>
        </FieldRow>

        <FieldRow label="当前通道" tip="主通道更快但更容易被 115 限流；连续被拒会自动切到备用通道 24 小时。">
          <span class="st-note">{{ status.endpoint }}</span>
        </FieldRow>

        <FieldRow label="轮询间隔">
          <span class="st-note">{{ status.interval_sec > 0 ? `每 ${status.interval_sec} 秒一轮` : '已关闭（跟随自动整理串行执行）' }}</span>
        </FieldRow>

        <FieldRow label="上一轮" tip="每一轮都会记录，包括没找到任何变动的空转轮次。">
          <template v-if="status.last_round.at">
            <span class="st-note">{{ status.last_round.at }}</span>
            <span v-if="status.last_round.error" class="st-err">{{ status.last_round.error }}</span>
            <span v-else-if="status.last_round.summary" class="st-dim">
              新事件 {{ status.last_round.summary.events_fresh }} ·
              新增 STRM {{ status.last_round.summary.strm_created }} ·
              清理 {{ status.last_round.summary.deleted }} ·
              移动/改名 {{ status.last_round.summary.moved }}
            </span>
          </template>
          <span v-else class="st-dim">还没跑过</span>
        </FieldRow>

        <FieldRow label="积压事件" tip="拉回来但还没处理完的事件。持续不降说明有网盘目录一直读不出来，日志里会有提示。">
          <span :class="status.pending_events > 0 ? 'st-warn' : 'st-note'">{{ status.pending_events }} 条</span>
          <span class="st-dim">· 目录路径缓存 {{ status.path_cache }} 条</span>
        </FieldRow>

        <FormActions>
          <NButton :loading="probing" @click="void probe()">测试事件流</NButton>
          <NButton quaternary @click="void loadStatus()">刷新状态</NButton>
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
    <NModal v-model:show="helpVisible" preset="card" title="增量同步是怎么回事" style="width: min(860px, 92vw)">
      <IncrHelp />
    </NModal>
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
.st-note {
  font-size: 13px;
  color: var(--c-text-1);
}
.st-dim {
  font-size: 12px;
  color: var(--c-text-3);
  margin-left: 6px;
}
.st-warn {
  font-size: 13px;
  color: var(--c-warning, #d97706);
}
.st-err {
  font-size: 12px;
  color: var(--c-danger);
  margin-left: 6px;
}
.probe {
  margin-top: 12px;
  border-top: 1px solid var(--c-border, rgba(128, 128, 128, 0.2));
  padding-top: 10px;
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
  color: var(--c-text-3);
  flex-shrink: 0;
}
.probe-kind,
.probe-type {
  color: var(--c-text-3);
  flex-shrink: 0;
}
.probe-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.probe-skip {
  margin-left: auto;
  color: var(--c-text-4);
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
  color: var(--c-primary);
  text-decoration: none;
}
.jump:hover {
  text-decoration: underline;
}

.note-act {
  margin-top: 8px;
}
</style>
