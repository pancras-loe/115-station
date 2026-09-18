<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NAlert, NButton, NDynamicTags, NInput, NPopconfirm } from 'naive-ui'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import TaskStatusBar from '@/components/TaskStatusBar.vue'
import Cid115Input from '@/components/Cid115Input.vue'
import LocalPathInput from '@/components/LocalPathInput.vue'
import { syncApi } from '@/api'
import { useSetting } from '@/composables/useSetting'
import { useTaskStore } from '@/stores/task'
import { toastError, useFeedback } from '@/composables/useFeedback'

const { message } = useFeedback()
const task = useTaskStore()

const full = useSetting('full', {
  cid: '',
  local_path: '/media',
  video_ext: ['mp4', 'mkv', 'ts', 'avi', 'mov', 'rmvb', 'webm', 'flv', 'm2ts', 'wmv', 'mpg', 'iso'],
  image_ext: ['jpg', 'png', 'jpeg', 'webp'],
  data_ext: ['ass', 'srt', 'ssa', 'sub'],
})

const incr = useSetting('incr', { cron: '*/10 8-23 * * *' })

const cidInput = ref<InstanceType<typeof Cid115Input> | null>(null)
/**
 * 配置里存的是裸 cid（没有路径），所以初始 path 直接显示 cid。
 * 用户点「选择目录」后 path 会变成可读路径，cid 仍然是真正提交的值。
 */
const cidValue = ref({ cid: '', path: '' })
watch(
  () => full.model.value.cid,
  (v) => {
    if (v && v !== cidValue.value.cid) cidValue.value = { cid: v, path: v }
  },
  { immediate: true },
)

const running = ref(false)

/** 保存前先确认 cid 可信，否则会把空 cid 或失配的旧 cid 存进配置 */
async function ensureCid(): Promise<string | null> {
  const cid = (await cidInput.value?.ensureCid()) ?? ''
  if (!cid || cid === '0') {
    message.error('目录路径无法识别：请点「选择目录」重新选择，或输入纯数字 cid')
    return null
  }
  return cid
}

async function saveFull() {
  const cid = await ensureCid()
  if (cid === null) return
  full.model.value.cid = cid
  await full.save()
}

async function runFull() {
  const cid = await ensureCid()
  if (cid === null) return
  if (!full.model.value.video_ext.length) {
    message.warning('请至少保留一个视频文件后缀')
    return
  }
  running.value = true
  message.info('全量同步进行中（受 API 间隔限制可能持续数分钟）…')
  task.poll()
  try {
    const d = await syncApi.runFull({ ...full.model.value, cid })
    message.success(
      `全量同步完成：视频 ${d.total} 个（生成 STRM ${d.created}），` +
        `附属文件 ${d.assets_total} 个（下载 ${d.assets_downloaded}，跳过 ${d.assets_skipped}，失败 ${d.assets_failed}）`,
    )
  } catch (e) {
    toastError(e, '全量同步失败')
  } finally {
    running.value = false
    task.poll()
  }
}

async function runIncremental() {
  const cid = await ensureCid()
  if (cid === null) return
  running.value = true
  message.info('增量同步进行中…')
  task.poll()
  try {
    const d = await syncApi.runIncremental({ ...full.model.value, cid })
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
  <div class="page">
    <TaskStatusBar />

    <SectionCard title="全量同步" hint="115 媒体库目录 → 本地 STRM 文件">
      <FieldRow
        label="115 媒体库 cid"
        required
        tip="全量同步的根目录。增量同步、整理入库、洗版判定均锚定此目录；STRM 本地路径保存其镜像结构。"
      >
        <Cid115Input ref="cidInput" v-model="cidValue" />
      </FieldRow>

      <FieldRow
        label="本地媒体库目录"
        tip="STRM 与附属文件（字幕/NFO/图片）的本地保存根目录。需与 Emby 媒体库路径一致（配合 EMBY 管理卡的路径映射）。"
      >
        <LocalPathInput v-model="full.model.value.local_path" />
      </FieldRow>

      <FieldRow label="视频文件后缀" tip="匹配这些后缀的文件视为视频，生成 STRM。">
        <NDynamicTags v-model:value="full.model.value.video_ext" size="small" />
      </FieldRow>

      <FieldRow label="媒体图片后缀" tip="匹配这些后缀的图片下载到本地（poster 等 Emby 刮削用）。">
        <NDynamicTags v-model:value="full.model.value.image_ext" size="small" />
      </FieldRow>

      <FieldRow label="数据文件后缀" tip="匹配这些后缀的数据文件下载到本地（.nfo 始终包含）。">
        <NDynamicTags v-model:value="full.model.value.data_ext" size="small" />
      </FieldRow>

      <FormActions>
        <NButton type="primary" :loading="full.saving.value" @click="saveFull">保存配置</NButton>
        <NPopconfirm @positive-click="runFull">
          <template #trigger>
            <NButton type="primary" ghost :disabled="busy">开始全量同步</NButton>
          </template>
          确定开始全量同步？整库扫描耗时较长。
        </NPopconfirm>
        <NButton :disabled="busy" @click="full.reset">重置配置</NButton>
      </FormActions>
    </SectionCard>

    <SectionCard title="增量同步" hint="基于 115 生活事件的定时增量">
      <NAlert class="note" type="warning" :bordered="false">
        增量同步前需开启 115 生活 APP 中的「最近」（生活事件必须开启），且必须先执行一次全量同步。
      </NAlert>

      <FieldRow
        label="增量同步 Cron"
        tip="标准 5 字段 cron（分 时 日 月 周）。命中时执行「自动整理 → 增量同步」流水线；全量同步仅手动触发。"
      >
        <NInput v-model:value="incr.model.value.cron" placeholder="*/10 8-23 * * *" />
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
        <NPopconfirm @positive-click="runIncremental">
          <template #trigger>
            <NButton type="primary" ghost :disabled="busy">开始增量同步</NButton>
          </template>
          确定立即执行一次增量同步？
        </NPopconfirm>
        <NButton :disabled="busy" @click="incr.reset">重置配置</NButton>
      </FormActions>
    </SectionCard>
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.note {
  margin-bottom: 10px;
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
</style>
