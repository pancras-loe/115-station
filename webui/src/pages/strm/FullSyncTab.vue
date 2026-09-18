<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import {
  NAlert,
  NButton,
  NDynamicTags,
  NPopconfirm,
  NRadioButton,
  NRadioGroup,
  NSwitch,
} from 'naive-ui'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import CronField from '@/components/ui/CronField.vue'
import Cid115Input from '@/components/Cid115Input.vue'
import LocalPathInput from '@/components/LocalPathInput.vue'
import { syncApi } from '@/api'
import type { FullSyncMode, OrphanReport } from '@/api/sync'
import type { FullSetting } from './fullSetting'
import { useTaskStore } from '@/stores/task'
import { toastError, useFeedback } from '@/composables/useFeedback'

const props = defineProps<{ full: FullSetting }>()

const { message } = useFeedback()
const task = useTaskStore()
const cfg = computed(() => props.full.model.value)

const cidInput = ref<InstanceType<typeof Cid115Input> | null>(null)
/**
 * 配置里存的是裸 cid（没有路径），所以初始 path 直接显示 cid。
 * 用户点「选择目录」后 path 会变成可读路径，cid 仍然是真正提交的值。
 */
const cidValue = ref({ cid: '', path: '' })
watch(
  () => cfg.value.cid,
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
const fullMode = computed<FullSyncMode>(() => (fastAvailable.value ? cfg.value.mode : 'normal'))

// ---- 失效 STRM（本地还在、网盘已删）----
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

/** 失效占比异常偏高 = 这次扫描很可能没取全，别让用户一键删光 */
const orphanRatioHigh = computed(() => (orphan.value?.ratio ?? 0) > 0.2)

async function cleanOrphans() {
  orphanCleaning.value = true
  try {
    const d = await syncApi.cleanOrphans()
    message.success(
      `失效 STRM 清理完成：删除 ${d.removed} 个` +
        (d.missing ? `，本地已不存在 ${d.missing} 个` : '') +
        (d.failed ? `，失败 ${d.failed} 个` : ''),
    )
    await loadOrphans()
  } catch (e) {
    toastError(e, '失效 STRM 清理失败')
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
  cfg.value.cid = cid
  await props.full.save()
}

async function runFull() {
  const cid = await ensureCid()
  if (cid === null) return
  if (!cfg.value.video_ext.length) {
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
    const d = await syncApi.runFull({
      cid,
      local_path: cfg.value.local_path,
      video_ext: cfg.value.video_ext,
      image_ext: cfg.value.image_ext,
      data_ext: cfg.value.data_ext,
      mode: fullMode.value,
    })
    // 快速模式失败会被后端降级，这时不能让用户以为自己跑的是快速模式
    if (fullMode.value === 'fast' && d.mode_used === 'normal') {
      message.warning('快速模式不可用，已自动降级为标准模式完成本次同步（详见日志）')
    }
    message.success(
      `全量同步完成：视频 ${d.total} 个（生成 STRM ${d.created}），` +
        `附属文件 ${d.assets_total} 个（下载 ${d.assets_downloaded}，跳过 ${d.assets_skipped}，失败 ${d.assets_failed}）`,
    )
    if (cfg.value.detect_orphans && d.scan_complete === false) {
      message.warning('本次清单不完整，已跳过失效 STRM 标记（详见日志）')
    }
    await loadOrphans()
  } catch (e) {
    toastError(e, '全量同步失败')
  } finally {
    running.value = false
    task.poll()
  }
}

const busy = computed(() => running.value || task.status.running)
</script>

<template>
  <div class="tab-body">
    <SectionCard title="全量同步" hint="115 媒体库目录 → 本地 STRM 文件">
      <FieldRow
        label="同步模式"
        tip="标准模式逐个目录遍历，兼容性最好；快速模式一次性取回整棵目录树与文件表，请求数少两个数量级。"
      >
        <template v-if="fastAvailable">
          <NRadioGroup v-model:value="cfg.mode">
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
        <LocalPathInput v-model="cfg.local_path" />
      </FieldRow>

      <FieldRow label="视频文件后缀" tip="匹配这些后缀的文件视为视频，生成 STRM。">
        <NDynamicTags v-model:value="cfg.video_ext" size="small" />
      </FieldRow>

      <FieldRow label="媒体图片后缀" tip="匹配这些后缀的图片下载到本地（poster 等 Emby 刮削用）。">
        <NDynamicTags v-model:value="cfg.image_ext" size="small" />
      </FieldRow>

      <FieldRow label="数据文件后缀" tip="匹配这些后缀的数据文件下载到本地（.nfo 始终包含）。">
        <NDynamicTags v-model:value="cfg.data_ext" size="small" />
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

    <SectionCard title="失效 STRM 检测" hint="本地还有 STRM、网盘上源文件已删除">
      <FieldRow
        label="失效 STRM 检测"
        tip="全量同步后标出「本地还有 STRM、网盘上源文件已被删除」的条目。只标记不删除，清理需要你在下方确认。注意：缩减上面的后缀配置也会让原先同步过的文件被判为失效。"
      >
        <NSwitch v-model:value="cfg.detect_orphans" />
      </FieldRow>

      <!-- 定时全量只服务于失效检测：检测关着时整库扫描白跑一趟（115 风控敏感），
           所以开关整个不显示，后端也按同样规则当没开 -->
      <template v-if="cfg.detect_orphans">
        <FieldRow
          label="定时全量同步"
          tip="按 cron 定期跑一次全量同步来刷新失效标记。生活事件有窗口，网页版批量删除、停机期间的删除增量同步都收不到，只有整库扫描才查得出来。"
        >
          <NSwitch v-model:value="cfg.cron_enabled" />
        </FieldRow>

        <FieldRow
          v-if="cfg.cron_enabled"
          label="全量同步 Cron"
          tip="标准 5 字段 cron（分 时 日 月 周）。整库扫描请求量大，建议每天最多一次、放在夜间。定时只负责标记，删除仍然要你手动确认。"
        >
          <CronField v-model="cfg.cron" placeholder="0 4 * * *" />
        </FieldRow>
      </template>

      <NAlert v-if="cfg.detect_orphans && orphan && orphan.total === 0" class="note" type="success" :bordered="false">
        当前没有检测到失效条目。
      </NAlert>

      <FormActions>
        <NButton type="primary" :loading="full.saving.value" @click="saveFull">保存配置</NButton>
        <NButton :disabled="busy" @click="loadOrphans">刷新检测结果</NButton>
      </FormActions>
    </SectionCard>

    <SectionCard
      v-if="orphan && orphan.total > 0"
      title="待清理失效 STRM"
      hint="本地还有文件，网盘上的源文件已不存在"
    >
      <NAlert v-if="orphanRatioHigh" class="note" type="error" :bordered="false">
        失效条目占台账总数的 {{ (orphan.ratio * 100).toFixed(1) }}%（{{ orphan.total }} /
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
              清理全部 {{ orphan.total }} 个失效 STRM
            </NButton>
          </template>
          确定删除这 {{ orphan.total }} 个本地文件？此操作不可撤销（网盘不受影响）。
        </NPopconfirm>
        <NButton :disabled="busy" @click="loadOrphans">刷新</NButton>
      </FormActions>
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
</style>
