<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { NAlert, NButton, NInput, NInputNumber, NPopconfirm, NTag } from 'naive-ui'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import { syncApi } from '@/api'
import type { FullSetting } from './fullSetting'
import { useSetting } from '@/composables/useSetting'
import { useTaskStore } from '@/stores/task'
import { toastError, useFeedback } from '@/composables/useFeedback'

const props = defineProps<{ full: FullSetting }>()

const { message } = useFeedback()
const task = useTaskStore()

const incr = useSetting('incr', { cron: '*/10 8-23 * * *', interval_sec: 30 })
const running = ref(false)

async function runIncremental() {
  const cfg = props.full.model.value
  // 增量走的是已保存的媒体库配置（定时任务也读同一份），没保存过就没得跑
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

onMounted(() => void loadStatus())

// ---- cron 预览 ----
const cronNext = ref<string[]>([])
const cronError = ref('')
let cronTimer: number | undefined

watch(
  () => incr.model.value.cron,
  (expr) => {
    clearTimeout(cronTimer)
    cronNext.value = []
    cronError.value = ''
    if (!expr.trim()) return
    cronTimer = window.setTimeout(async () => {
      try {
        const d = await syncApi.cronPreview(expr.trim())
        cronNext.value = d.next ?? []
        if (!cronNext.value.length) cronError.value = '未来一年内不会触发，请检查表达式'
      } catch (e) {
        cronError.value = e instanceof Error ? e.message : '表达式无效'
      }
    }, 500)
  },
  { immediate: true },
)

const busy = computed(() => running.value || task.status.running)
</script>

<template>
  <div class="tab-body">
    <SectionCard title="增量同步" hint="基于 115 生活事件的定时增量">
      <NAlert class="note" type="warning" :bordered="false">
        增量同步前需开启 115 生活 APP 中的「最近」（生活事件必须开启），且必须先执行一次全量同步。
      </NAlert>

      <FieldRow
        label="自动整理 Cron"
        tip="标准 5 字段 cron（分 时 日 月 周）。命中时执行自动整理（识别 → 搬移 → 写 STRM → 刮削 → 刷 Emby）。留空则整理不再定时执行。"
      >
        <NInput v-model:value="incr.model.value.cron" placeholder="*/10 8-23 * * *" />
      </FieldRow>

      <FieldRow
        label="增量同步间隔"
        tip="增量同步独立轮询，只处理网盘端的外部变更（手机上传、离线下载、网页端删改），与自动整理互不影响。一轮通常只发 1~2 个请求，30 秒一轮的请求频率比旧版「10 分钟一轮、每轮 34 个请求」更低。填 0 可关闭独立轮询，退回跟着整理串行跑的旧行为。"
        hint="0 = 关闭独立轮询；最小 15 秒，低于 15 按 15 处理"
      >
        <NInputNumber
          v-model:value="incr.model.value.interval_sec"
          :min="0"
          :step="15"
          style="width: 160px"
        >
          <template #suffix>秒</template>
        </NInputNumber>
      </FieldRow>

      <FieldRow label="接下来运行" tip="按当前表达式推算的未来 5 次触发时间。">
        <div class="cron-preview">
          <span v-if="cronError" class="cron-err">{{ cronError }}</span>
          <template v-else-if="cronNext.length">
            <div v-for="(t, i) in cronNext" :key="i" class="cron-row">
              <span class="cron-idx">第 {{ i + 1 }} 次</span>{{ t }}
            </div>
          </template>
          <span v-else class="cron-idle">修改表达式后显示接下来 5 次运行时间</span>
        </div>
      </FieldRow>

      <FormActions>
        <NButton type="primary" :loading="incr.saving.value" @click="incr.save()">保存配置</NButton>
        <NPopconfirm @positive-click="void runIncremental()">
          <template #trigger>
            <NButton type="primary" ghost :disabled="busy" :loading="running">开始增量同步</NButton>
          </template>
          确定立即执行一次增量同步？
        </NPopconfirm>
        <NButton :disabled="busy" @click="incr.reset">重置配置</NButton>
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

    <SectionCard title="两条时间表各管什么" hint="整理与增量已经拆开">
      <div class="pipeline">
        <div class="step">
          <span class="step-idx">1</span>
          <div>
            <div class="step-title">自动整理（上面的 cron）</div>
            <div class="step-desc">
              识别 → 二级分类 → 洗版 → 重命名 → 搬入媒体库 → 写 STRM → 刮削 → 刷 Emby。
              一条龙自己落盘，不依赖增量（规则见「自动整理」页）
            </div>
          </div>
        </div>
        <div class="step">
          <span class="step-idx">2</span>
          <div>
            <div class="step-title">增量同步（上面的间隔）</div>
            <div class="step-desc">
              独立轮询，只管网盘端的外部变更：手机/客户端上传、离线下载完成、网页端的删除与改名
            </div>
          </div>
        </div>
      </div>
      <NAlert class="note-top" type="info" :bordered="false">
        两者互不依赖，所以可以各走各的节奏：整理是重操作，低频合适；增量一轮通常只发 1~2 个请求，
        跑得勤才能让手机上传的片子尽快出现在媒体库里。整理在网盘上做的搬移与改名会被登记下来，
        绕回事件流时增量会跳过，不会重复处理。
      </NAlert>
    </SectionCard>
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

.cron-preview {
  padding: 8px 11px;
  border-radius: var(--radius);
  background: var(--c-bg-raised);
  border: 1px solid var(--c-border);
  font-size: 12px;
  line-height: 1.8;
}
.cron-row {
  font-variant-numeric: tabular-nums;
  color: var(--c-text-1);
}
.cron-idx {
  display: inline-block;
  width: 62px;
  color: var(--c-text-3);
}
.cron-err {
  color: var(--c-danger);
}
.cron-idle {
  color: var(--c-text-3);
}

.pipeline {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.step {
  display: flex;
  align-items: flex-start;
  gap: 11px;
}
.step-idx {
  flex: none;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  border-radius: 50%;
  background: var(--c-primary-soft);
  border: 1px solid var(--c-primary-border);
  color: var(--c-primary);
  font-size: 11.5px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}
.step-title {
  font-size: 13.5px;
  font-weight: 600;
  color: var(--c-text-1);
  line-height: 22px;
}
.step-desc {
  margin-top: 1px;
  font-size: 12px;
  line-height: 1.7;
  color: var(--c-text-3);
}
</style>
