<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import HModal from '@/components/hero/HModal.vue'
import HPopconfirm from '@/components/hero/HPopconfirm.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import HTagsInput from '@/components/hero/HTagsInput.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import CronField from '@/components/ui/CronField.vue'
import FullHelp from './FullHelp.vue'
import { useRouter } from 'vue-router'
import { syncApi } from '@/api'
import type { FullSyncConfig, FullSyncMode, OrphanReport } from '@/api/sync'
import { defaultFull, type FullSetting } from './fullSetting'
import { useQueueStore } from '@/stores/queue'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { UNSAVED_NOTE, confirmUnsaved } from '@/composables/confirmUnsaved'

const props = defineProps<{ full: FullSetting }>()

const { message } = useFeedback()
const router = useRouter()
const queue = useQueueStore()
const cfg = computed(() => props.full.model.value)

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

/** 媒体库位置改为统一配置后，这里只校验是否已经完成基础配置。 */
function configuredCid(src: FullSyncConfig): string | null {
  const cid = src.cid.trim()
  if (!cid || cid === '0') {
    message.error('请先到「账号与媒体库」配置 115 媒体库目录')
    return null
  }
  return cid
}

async function saveFull() {
  await props.full.save()
}

async function resetFullOptions() {
  const cid = cfg.value.cid
  const cidPath = cfg.value.cid_path
  const localPath = cfg.value.local_path
  Object.assign(cfg.value, defaultFull(), { cid, cid_path: cidPath, local_path: localPath })
  await props.full.save()
}

/** 按钮上的 popconfirm 文案；配置改过时这个气泡不弹，由未保存确认框接管 */
const runHint = computed(() =>
  fullMode.value === 'fast'
    ? '确定用快速模式开始全量同步？'
    : '确定开始全量同步？整库扫描耗时较长。',
)

async function runFull() {
  // 确认框要先弹：选「直接开始」时这次跑的是库里那份配置，
  // 校验和请求体都得照着那份来，否则界面上填错的值会去拦一次跑得通的同步。
  let src = cfg.value
  if (props.full.dirty.value) {
    if (!(await confirmUnsaved(UNSAVED_NOTE, () => props.full.save()))) return
    // 保存成功后 dirty 归零，界面值即已保存值；仍然脏 = 用户选了「直接开始」
    if (props.full.dirty.value) src = props.full.saved.value
  }

  const cid = configuredCid(src)
  if (cid === null) return
  if (!src.video_ext.length) {
    message.warning('请至少保留一个视频文件后缀')
    return
  }
  const mode: FullSyncMode = fastAvailable.value ? src.mode : 'normal'

  running.value = true
  try {
    const d = await syncApi.runFull({
      cid,
      local_path: src.local_path,
      video_ext: src.video_ext,
      image_ext: src.image_ext,
      data_ext: src.data_ext,
      mode,
    })
    message.success(d.message)
    await queue.submitted(d.job_id)
  } catch (e) {
    toastError(e, '提交全量同步失败')
  } finally {
    running.value = false
  }
}

// 全量跑完（结果提示由任务队列统一弹）：失效 STRM 数可能变了，刷新检测结果
const offFinished = queue.onFinished((j) => {
  if (j.kind === 'full') void loadOrphans()
})
onUnmounted(offFinished)

/** 队列里已有全量（排队或执行中）：再点也只是合并成同一个任务 */
const busy = computed(() => running.value || !!queue.activeManualOf('full'))

const helpVisible = ref(false)

const MODE_OPTIONS: { label: string; value: FullSyncMode }[] = [
  { label: '标准模式（默认）', value: 'normal' },
  { label: '快速模式', value: 'fast' },
]
</script>

<template>
  <div class="tab-body">
    <SectionCard title="全量同步" hint="115 媒体库目录 → 本地 STRM 文件">
      <template #extra>
        <HButton size="sm" variant="ghost" @click="helpVisible = true">功能介绍</HButton>
      </template>

      <FieldRow
        label="同步模式"
        tip="标准模式逐个目录遍历，兼容性最好；快速模式一次性取回整棵目录树与文件表，请求数少两个数量级。"
      >
        <template v-if="fastAvailable">
          <HSegmented v-model="cfg.mode" :options="MODE_OPTIONS" aria-label="同步模式" />
          <HAlert v-if="fullMode === 'fast'" class="mode-note" status="warning">
            媒体库文件数超过 20 万时，建议先用标准模式完整跑通一次，确认无异常后再切快速模式。
            快速模式依赖 115 客户端端点，若接口变动会自动降级为标准模式。
          </HAlert>
        </template>
        <span v-else class="mode-locked">标准模式{{ fastReason ? `（${fastReason}）` : '' }}</span>
      </FieldRow>

      <FieldRow
        label="115 媒体库目录"
        tip="统一位置配置；全量同步、增量同步、整理入库与洗版判定共同使用。"
      >
        <HInput :model-value="cfg.cid_path || cfg.cid || '未配置'" readonly />
      </FieldRow>

      <FieldRow
        label="本地媒体库根目录"
        tip="统一位置配置；STRM、附属文件与影视刮削共同使用。"
      >
        <HInput :model-value="cfg.local_path || '未配置'" readonly mono />
      </FieldRow>

      <HAlert class="location-note" status="accent">
        媒体库位置已统一到「账号与媒体库」页面配置。
        <button type="button" class="text-link" @click="router.push({ name: 'accounts' })">前往配置 →</button>
      </HAlert>

      <FieldRow label="视频文件后缀" tip="匹配这些后缀的文件视为视频，生成 STRM。">
        <HTagsInput v-model="cfg.video_ext" placeholder=".mkv 回车添加" />
      </FieldRow>

      <FieldRow label="媒体图片后缀" tip="匹配这些后缀的图片下载到本地（poster 等 Emby 刮削用）。">
        <HTagsInput v-model="cfg.image_ext" placeholder=".jpg 回车添加" />
      </FieldRow>

      <FieldRow label="数据文件后缀" tip="匹配这些后缀的数据文件下载到本地（.nfo 始终包含）。">
        <HTagsInput v-model="cfg.data_ext" placeholder=".nfo 回车添加" />
      </FieldRow>

      <FieldRow
        label="失效 STRM 检测"
        tip="每次全量同步结束时，标出「本地还有 STRM、网盘上源文件已被删除」的条目。只标记不删除，清理要你在下方确认。关掉它，下面的定时全量也一并停跑（定时全量的用途就是刷新这个标记）。注意：缩减上面的后缀配置也会让原先同步过的文件被判为失效。"
      >
        <HSwitch v-model="cfg.detect_orphans" aria-label="失效 STRM 检测" />
      </FieldRow>

      <!-- 定时全量只服务于失效检测：检测关着时整库扫描白跑一趟（115 风控敏感），
           所以开关整个不显示，后端也按同样规则当没开 -->
      <template v-if="cfg.detect_orphans">
        <FieldRow
          label="定时全量同步"
          tip="按 cron 定期跑一次全量同步来刷新失效标记。生活事件有窗口，网页版批量删除、停机期间的删除增量同步都收不到，只有整库扫描才查得出来。"
        >
          <HSwitch v-model="cfg.cron_enabled" aria-label="定时全量同步" />
        </FieldRow>

        <FieldRow
          v-if="cfg.cron_enabled"
          label="全量同步 Cron"
          tip="标准 5 字段 cron（分 时 日 月 周）。整库扫描请求量大，建议每天最多一次、放在夜间。定时同样只负责标记，删除仍然要你手动确认。"
        >
          <CronField v-model="cfg.cron" placeholder="0 4 * * *" />
        </FieldRow>
      </template>

      <FieldRow
        label="全量后刷新 Emby"
        tip="全量同步结束时通知 Emby 扫描入库。全量给出的范围是整个媒体库根，等于把根下面每个媒体库都整库扫一遍，万级库很慢，所以默认关着。增量同步不受这个开关影响——它只刷本轮真正变动的那个目录，删除条目也靠它通知。"
      >
        <HSwitch v-model="cfg.refresh_emby" aria-label="全量后刷新 Emby" />
      </FieldRow>

      <FormActions>
        <HButton variant="primary" :loading="full.saving.value" @click="saveFull">保存配置</HButton>
        <!-- 配置改过没保存时由 runFull 里的确认框接管（那个框里也带着同一句话），
             这里再弹一次 popconfirm 就成了连点两下 -->
        <HPopconfirm v-if="!full.dirty.value" confirm-text="开始" :disabled="busy" @confirm="void runFull()">
          <HButton variant="secondary" :disabled="busy" :loading="running">开始全量同步</HButton>
          <template #content>{{ runHint }}</template>
        </HPopconfirm>
        <HButton v-else variant="secondary" :disabled="busy" :loading="running" @click="void runFull()">
          开始全量同步
        </HButton>
        <HButton variant="tertiary" :disabled="busy" @click="resetFullOptions">重置同步选项</HButton>
      </FormActions>
    </SectionCard>

    <!-- 检测开着才有结果可看，没有失效条目时也留着这张卡，免得用户以为开关没生效；
         检测被关掉但台账里还留着上次标出的条目时也要显示，否则清理入口就没了 -->
    <SectionCard
      v-if="cfg.detect_orphans || (orphan && orphan.total > 0)"
      title="失效 STRM"
      hint="本地还有文件，网盘上的源文件已不存在"
    >
      <template v-if="orphan && orphan.total > 0">
        <HAlert v-if="orphanRatioHigh" class="note" status="danger">
          失效条目占台账总数的 {{ (orphan.ratio * 100).toFixed(1) }}%（{{ orphan.total }} /
          {{ orphan.ledger_total }}），比例异常偏高。这通常意味着上次扫描没取全，或者同步的不是平时那个媒体库
          —— 清理前请先重跑一次全量同步确认。
        </HAlert>
        <HAlert v-else class="note" status="warning">
          共 {{ orphan.total }} 个（台账 {{ orphan.ledger_total }} 条）。清理会删除这些本地文件与对应台账记录，
          并顺带删掉因此变空的目录。网盘不受影响。
        </HAlert>

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
          <HPopconfirm danger confirm-text="清理" :disabled="busy" @confirm="void cleanOrphans()">
            <HButton variant="danger-soft" :disabled="busy" :loading="orphanCleaning">
              清理全部 {{ orphan.total }} 个失效 STRM
            </HButton>
            <template #content>
              确定删除这 {{ orphan.total }} 个本地文件？此操作不可撤销（网盘不受影响）。
            </template>
          </HPopconfirm>
          <HButton variant="tertiary" :disabled="busy" @click="loadOrphans">刷新</HButton>
        </FormActions>
      </template>

      <template v-else>
        <HAlert class="note" status="success">
          当前没有检测到失效条目。标记在每次全量同步结束时刷新。
        </HAlert>
        <FormActions>
          <HButton variant="tertiary" :disabled="busy" @click="loadOrphans">刷新检测结果</HButton>
        </FormActions>
      </template>
    </SectionCard>

    <!-- 弹窗必须留在这个根元素里：本页整体被 SyncPage 的 <Transition> 包着，
         多个根节点会让 Transition 找不到唯一子元素，整页渲染成空白 -->
    <HModal v-model:show="helpVisible" title="全量同步是怎么回事" width="860px">
      <FullHelp />
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
.mode-note {
  margin-top: 8px;
}
.location-note {
  margin-bottom: 10px;
}
.mode-locked {
  font-size: 13px;
  color: var(--muted);
}
.text-link {
  border: 0;
  padding: 0;
  margin-left: 4px;
  background: none;
  font: inherit;
  font-weight: 500;
  color: var(--accent);
  cursor: pointer;
}
.text-link:hover {
  text-decoration: underline;
}
.orphan-list {
  max-height: 260px;
  overflow-y: auto;
  padding: 10px 14px;
  border-radius: 16px;
  background: var(--surface-secondary);
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
  color: var(--muted);
}
.orphan-path {
  flex: 1;
  overflow-wrap: anywhere;
  color: var(--foreground);
}
.orphan-meta {
  flex: none;
  color: var(--muted);
  font-variant-numeric: tabular-nums;
}
.orphan-more {
  margin-top: 4px;
  color: var(--muted);
}
/* 手机：路径独占一行，大小与时间折到下一行 */
@media (max-width: 720px) {
  .orphan-row {
    flex-wrap: wrap;
  }
  .orphan-path {
    flex-basis: calc(100% - 42px);
  }
  .orphan-meta {
    padding-left: 42px;
  }
}
</style>
