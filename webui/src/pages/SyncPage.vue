<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import {
  NAlert,
  NButton,
  NDynamicTags,
  NInput,
  NPopconfirm,
  NRadioButton,
  NRadioGroup,
  NSwitch,
} from 'naive-ui'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import TaskStatusBar from '@/components/TaskStatusBar.vue'
import Cid115Input from '@/components/Cid115Input.vue'
import LocalPathInput from '@/components/LocalPathInput.vue'
import { syncApi } from '@/api'
import type { FullSyncMode, OrphanReport } from '@/api/sync'
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
  mode: 'normal' as FullSyncMode,
  detect_orphans: false,
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

/**
 * 快速模式只在 Cookie 通道可用（downfolders 没有开放平台对应端点），
 * 判定规则放在后端，这里只负责展示——前端自己按 openapi_enabled 推会和后端走偏。
 */
const fastAvailable = ref(false)
const fastReason = ref('')
onMounted(async () => {
  try {
    const d = await syncApi.capabilities()
    fastAvailable.value = d.fast_available
    fastReason.value = d.reason
  } catch {
    // 拿不到就当不可用，退回标准模式，不打断页面
  }
})

/** 不可用时强制回落，避免配置里残留 fast 却静默走标准模式 */
const fullMode = computed<FullSyncMode>(() =>
  fastAvailable.value ? full.model.value.mode : 'normal',
)

// ---- 孤儿（本地还在、网盘已删）----
const orphan = ref<OrphanReport | null>(null)
const orphanCleaning = ref(false)

async function loadOrphans() {
  try {
    orphan.value = await syncApi.orphans()
  } catch {
    orphan.value = null
  }
}
onMounted(loadOrphans)

/** 孤儿占比异常偏高 = 这次扫描很可能没取全，别让用户一键删光 */
const orphanRatioHigh = computed(() => (orphan.value?.ratio ?? 0) > 0.2)

async function cleanOrphans() {
  orphanCleaning.value = true
  try {
    const d = await syncApi.cleanOrphans()
    message.success(
      `孤儿清理完成：删除 ${d.removed} 个` +
        (d.missing ? `，本地已不存在 ${d.missing} 个` : '') +
        (d.failed ? `，失败 ${d.failed} 个` : ''),
    )
    await loadOrphans()
  } catch (e) {
    toastError(e, '孤儿清理失败')
  } finally {
    orphanCleaning.value = false
  }
}

/** 字节数转人类可读 */
function humanSize(n: number): string {
  if (!n) return ''
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

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
  message.info(
    fullMode.value === 'fast'
      ? '全量同步进行中（快速模式）…'
      : '全量同步进行中（受 API 间隔限制可能持续数分钟）…',
  )
  task.poll()
  try {
    const d = await syncApi.runFull({ ...full.model.value, cid, mode: fullMode.value })
    // 快速模式失败会被后端降级，这时不能让用户以为自己跑的是快速模式
    if (fullMode.value === 'fast' && d.mode_used === 'normal') {
      message.warning('快速模式不可用，已自动降级为标准模式完成本次同步（详见日志）')
    }
    message.success(
      `全量同步完成：视频 ${d.total} 个（生成 STRM ${d.created}），` +
        `附属文件 ${d.assets_total} 个（下载 ${d.assets_downloaded}，跳过 ${d.assets_skipped}，失败 ${d.assets_failed}）`,
    )
    if (full.model.value.detect_orphans && d.scan_complete === false) {
      message.warning('本次清单不完整，已跳过孤儿标记（详见日志）')
    }
    await loadOrphans()
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
        label="同步模式"
        tip="标准模式逐个目录遍历，兼容性最好；快速模式一次性取回整棵目录树与文件表，请求数少两个数量级。"
      >
        <template v-if="fastAvailable">
          <NRadioGroup v-model:value="full.model.value.mode">
            <NRadioButton value="normal">标准模式（默认）</NRadioButton>
            <NRadioButton value="fast">快速模式</NRadioButton>
          </NRadioGroup>
          <NAlert v-if="fullMode === 'fast'" class="mode-note" type="warning" :bordered="false">
            媒体库文件数超过 20 万时，建议先用标准模式完整跑通一次，确认无异常后再切快速模式。
            快速模式依赖 115 客户端端点，若接口变动会自动降级为标准模式。
          </NAlert>
        </template>
        <span v-else class="mode-locked">标准模式{{ fastReason ? `（${fastReason}）` : '' }}</span>
      </FieldRow>

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

      <FieldRow
        label="孤儿检测"
        tip="全量同步后标出「本地还有 STRM、网盘上源文件已被删除」的条目。只标记不删除，清理需要你在下方确认。注意：缩减上面的后缀配置也会让原先同步过的文件变成孤儿。"
      >
        <NSwitch v-model:value="full.model.value.detect_orphans" />
      </FieldRow>

      <FormActions>
        <NButton type="primary" :loading="full.saving.value" @click="saveFull">保存配置</NButton>
        <NPopconfirm @positive-click="runFull">
          <template #trigger>
            <NButton type="primary" ghost :disabled="busy">开始全量同步</NButton>
          </template>
          {{
            fullMode === 'fast'
              ? '确定用快速模式开始全量同步？'
              : '确定开始全量同步？整库扫描耗时较长。'
          }}
        </NPopconfirm>
        <NButton :disabled="busy" @click="full.reset">重置配置</NButton>
      </FormActions>
    </SectionCard>

    <SectionCard
      v-if="orphan && orphan.total > 0"
      title="待清理孤儿"
      hint="本地还有文件，网盘上的源文件已不存在"
    >
      <NAlert v-if="orphanRatioHigh" class="note" type="error" :bordered="false">
        孤儿占台账总数的 {{ (orphan.ratio * 100).toFixed(1) }}%（{{ orphan.total }} /
        {{ orphan.ledger_total }}），比例异常偏高。这通常意味着上次扫描没取全，或者同步的不是平时那个媒体库
        —— 清理前请先重跑一次全量同步确认。
      </NAlert>
      <NAlert v-else class="note" type="warning" :bordered="false">
        共 {{ orphan.total }} 个（台账 {{ orphan.ledger_total }} 条）。清理会删除这些本地文件与对应台账记录，
        并顺带删掉因此变空的目录。网盘不受影响。
      </NAlert>

      <div class="orphan-list">
        <div v-for="it in orphan.sample" :key="it.rel_path" class="orphan-row">
          <span class="orphan-kind">{{ it.kind === 'video' ? 'STRM' : '附属' }}</span>
          <span class="orphan-path">{{ it.rel_path }}</span>
          <span class="orphan-meta">{{ humanSize(it.size) }} · {{ it.marked_at }}</span>
        </div>
        <div v-if="orphan.total > orphan.sample.length" class="orphan-more">
          仅显示前 {{ orphan.sample_limit }} 条，其余 {{ orphan.total - orphan.sample.length }} 条未列出
        </div>
      </div>

      <FormActions>
        <NPopconfirm @positive-click="cleanOrphans">
          <template #trigger>
            <NButton type="error" ghost :disabled="busy" :loading="orphanCleaning">
              清理全部 {{ orphan.total }} 个孤儿
            </NButton>
          </template>
          确定删除这 {{ orphan.total }} 个本地文件？此操作不可撤销（网盘不受影响）。
        </NPopconfirm>
        <NButton :disabled="busy" @click="loadOrphans">刷新</NButton>
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
.mode-note {
  margin-top: 8px;
}
.mode-locked {
  font-size: 13px;
  color: var(--c-text-3);
}
.orphan-list {
  max-height: 260px;
  overflow-y: auto;
  padding: 8px 11px;
  border-radius: var(--radius);
  background: var(--c-bg-raised);
  border: 1px solid var(--c-border);
  font-size: 12px;
  line-height: 1.9;
}
.orphan-row {
  display: flex;
  gap: 8px;
  align-items: baseline;
}
.orphan-kind {
  flex: none;
  width: 34px;
  color: var(--c-text-3);
}
.orphan-path {
  flex: 1;
  overflow-wrap: anywhere;
  color: var(--c-text-1);
}
.orphan-meta {
  flex: none;
  color: var(--c-text-3);
  font-variant-numeric: tabular-nums;
}
.orphan-more {
  margin-top: 4px;
  color: var(--c-text-3);
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
