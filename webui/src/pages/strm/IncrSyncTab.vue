<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NAlert, NButton, NInput, NInputNumber, NPopconfirm } from 'naive-ui'
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

    <SectionCard title="这条 cron 管什么" hint="定时流水线的执行顺序">
      <div class="pipeline">
        <div class="step">
          <span class="step-idx">1</span>
          <div>
            <div class="step-title">自动整理</div>
            <div class="step-desc">识别 → 二级分类 → 洗版 → 重命名 → 搬入媒体库（规则见「自动整理」页）</div>
          </div>
        </div>
        <div class="step">
          <span class="step-idx">2</span>
          <div>
            <div class="step-title">增量同步</div>
            <div class="step-desc">拉生活事件，把上一步产生的移动 / 改名 / 新增精确应用到本地 STRM</div>
          </div>
        </div>
        <div class="step">
          <span class="step-idx">3</span>
          <div>
            <div class="step-title">Emby 入库通知</div>
            <div class="step-desc">有新增时触发媒体库刷新与入库通知（在「系统配置 → EMBY 管理」「消息配置」里开）</div>
          </div>
        </div>
      </div>
      <NAlert class="note-top" type="info" :bordered="false">
        整理与增量绑在同一条 cron 上：整理会在网盘里搬文件、改名，这些动作只有紧接着跑一次增量同步
        才能落到本地 STRM 上，拆成两条时间表会出现「网盘已整理、本地还是旧路径」的窗口期。
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
