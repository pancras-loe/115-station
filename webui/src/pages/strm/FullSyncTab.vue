<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { ArrowRight, BookOpen, FolderTree, HardDrive, Play } from '@lucide/vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HModal from '@/components/hero/HModal.vue'
import HPopconfirm from '@/components/hero/HPopconfirm.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import HTagsInput from '@/components/hero/HTagsInput.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import CronField from '@/components/ui/CronField.vue'
import FullHelp from './FullHelp.vue'
import { syncApi } from '@/api'
import type { FullSyncMode, OrphanReport } from '@/api/sync'
import { defaultFull, type FullSetting } from './fullSetting'
import type { StrmRuns } from './useStrmRuns'
import { useQueueStore } from '@/stores/queue'
import { toastError, useFeedback } from '@/composables/useFeedback'

/**
 * 「开始全量」的逻辑（未保存确认、快速模式可用性）在 useStrmRuns，由 SyncPage 持有传进来。
 * 媒体库位置是「账号与媒体库」的统一配置，这里只读显示一行。
 */
const props = defineProps<{ full: FullSetting; runs: StrmRuns }>()

const { message } = useFeedback()
const queue = useQueueStore()
const cfg = computed(() => props.full.model.value)

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

async function resetFullOptions() {
  const cid = cfg.value.cid
  const cidPath = cfg.value.cid_path
  const localPath = cfg.value.local_path
  Object.assign(cfg.value, defaultFull(), { cid, cid_path: cidPath, local_path: localPath })
  await props.full.save()
}

// 全量跑完（结果提示由任务队列统一弹）：失效 STRM 数可能变了，刷新检测结果
const offFinished = queue.onFinished((j) => {
  if (j.kind === 'full') void loadOrphans()
})
onUnmounted(offFinished)

const busy = computed(() => props.runs.busyFull.value)
const helpVisible = ref(false)

const MODE_OPTIONS: { label: string; value: FullSyncMode }[] = [
  { label: '标准模式（默认）', value: 'normal' },
  { label: '快速模式', value: 'fast' },
]
</script>

<template>
  <div class="tab-body">
    <!-- ==== 同步的是哪里 → 哪里：只读，一行说清 ==== -->
    <div class="loc">
      <span class="loc-item" :title="cfg.cid_path || cfg.cid">
        <FolderTree :size="15" />
        <span class="loc-label">115</span>
        <span class="loc-path" :class="{ 'is-empty': !cfg.cid }">{{ cfg.cid_path || cfg.cid || '未配置' }}</span>
      </span>
      <ArrowRight :size="15" class="loc-arrow" />
      <span class="loc-item" :title="cfg.local_path">
        <HardDrive :size="15" />
        <span class="loc-label">本地</span>
        <span class="loc-path" :class="{ 'is-empty': !cfg.local_path }">{{ cfg.local_path || '未配置' }}</span>
      </span>
      <RouterLink :to="{ name: 'accounts' }" class="loc-edit">在「账号与媒体库」修改</RouterLink>
      <button type="button" class="loc-help" @click="helpVisible = true">
        <BookOpen :size="14" />功能介绍
      </button>
    </div>

    <div class="grid">
      <SectionCard title="扫描范围" hint="怎么遍历、哪些文件生成 STRM 或下载到本地">
        <FieldRow
          label="同步模式"
          tip="标准模式逐个目录遍历，兼容性最好；快速模式一次性取回整棵目录树与文件表，请求数少两个数量级。"
        >
          <template v-if="runs.fastAvailable.value">
            <HSegmented v-model="cfg.mode" :options="MODE_OPTIONS" aria-label="同步模式" />
            <HAlert v-if="runs.fullMode.value === 'fast'" class="mode-note" status="warning">
              媒体库文件数超过 20 万时，建议先用标准模式完整跑通一次，确认无异常后再切快速模式。
              快速模式依赖 115 客户端端点，若接口变动会自动降级为标准模式。
            </HAlert>
          </template>
          <span v-else class="mode-locked">标准模式{{ runs.fastReason.value ? `（${runs.fastReason.value}）` : '' }}</span>
        </FieldRow>

        <FieldRow label="视频文件后缀" tip="匹配这些后缀的文件视为视频，生成 STRM。">
          <HTagsInput v-model="cfg.video_ext" placeholder=".mkv 回车添加" />
        </FieldRow>

        <FieldRow label="媒体图片后缀" tip="匹配这些后缀的图片下载到本地（poster 等 Emby 刮削用）。">
          <HTagsInput v-model="cfg.image_ext" placeholder=".jpg 回车添加" />
        </FieldRow>

        <FieldRow label="数据文件后缀" tip="匹配这些后缀的数据文件下载到本地（.nfo 始终包含）。">
          <HTagsInput v-model="cfg.data_ext" placeholder=".nfo 回车添加" />
        </FieldRow>
      </SectionCard>

      <SectionCard title="检测与定时" hint="失效 STRM 标记、定时全量、结束后刷新 Emby">
        <FieldRow
          label="失效 STRM 检测"
          tip="每次全量同步结束时，标出「本地还有 STRM、网盘上源文件已被删除」的条目。只标记不删除，清理要你在下方确认。关掉它，下面的定时全量也一并停跑（定时全量的用途就是刷新这个标记）。注意：缩减左边的后缀配置也会让原先同步过的文件被判为失效。"
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
      </SectionCard>
    </div>

    <!-- 两张卡共用一份配置，保存按钮放在它们下面一处；改过没保存时整条高亮，免得改完直接走开 -->
    <div class="save-bar" :class="{ 'is-dirty': full.dirty.value }">
      <span class="save-note">{{ full.dirty.value ? '有未保存的改动' : '整库扫描请求量大，日常不用手动跑' }}</span>
      <HButton variant="tertiary" size="sm" :disabled="busy" @click="resetFullOptions">重置同步选项</HButton>
      <!-- 配置改过没保存时由 runFull 里的确认框接管（那个框里也带着同一句话），
           这里再弹一次 popconfirm 就成了连点两下 -->
      <HPopconfirm v-if="!full.dirty.value" confirm-text="开始" :disabled="busy" @confirm="void runs.runFull()">
        <HButton variant="secondary" size="sm" :disabled="busy" :loading="runs.runningFull.value">
          <template #icon><Play :size="14" /></template>
          开始全量同步
        </HButton>
        <template #content>
          {{ runs.fullMode.value === 'fast' ? '确定用快速模式开始全量同步？' : '确定开始全量同步？整库扫描耗时较长。' }}
        </template>
      </HPopconfirm>
      <HButton v-else variant="secondary" size="sm" :disabled="busy" :loading="runs.runningFull.value" @click="void runs.runFull()">
        <template #icon><Play :size="14" /></template>
        开始全量同步
      </HButton>
      <HButton variant="primary" size="sm" :loading="full.saving.value" @click="full.save()">保存配置</HButton>
    </div>

    <!-- 检测开着才有结果可看，没有失效条目时也留着这张卡，免得用户以为开关没生效；
         检测被关掉但台账里还留着上次标出的条目时也要显示，否则清理入口就没了 -->
    <SectionCard
      v-if="cfg.detect_orphans || (orphan && orphan.total > 0)"
      id="strm-orphans"
      title="失效 STRM"
      hint="本地还有文件，网盘上的源文件已不存在"
    >
      <template #extra>
        <HButton size="sm" variant="ghost" :disabled="busy" @click="loadOrphans">刷新</HButton>
      </template>

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

        <div class="orphan-actions">
          <HPopconfirm danger confirm-text="清理" :disabled="busy" @confirm="void cleanOrphans()">
            <HButton variant="danger-soft" :disabled="busy" :loading="orphanCleaning">
              清理全部 {{ orphan.total }} 个失效 STRM
            </HButton>
            <template #content>
              确定删除这 {{ orphan.total }} 个本地文件？此操作不可撤销（网盘不受影响）。
            </template>
          </HPopconfirm>
        </div>
      </template>

      <HAlert v-else class="note" status="success">
        当前没有检测到失效条目。标记在每次全量同步结束时刷新。
      </HAlert>
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

/* ---- 位置条 ---- */
.loc {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px 12px;
  padding: 10px 16px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  font-size: 13px;
}
.loc-item {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  color: var(--muted);
}
.loc-label {
  font-size: 12px;
}
.loc-path {
  font-family: var(--font-mono);
  font-size: 12.5px;
  color: var(--foreground);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.loc-path.is-empty {
  font-family: inherit;
  color: var(--warning);
}
.loc-arrow {
  flex-shrink: 0;
  color: var(--muted);
}
.loc-edit {
  margin-left: auto;
  font-size: 12.5px;
  color: var(--accent);
  text-decoration: none;
}
.loc-edit:hover {
  text-decoration: underline;
}
.loc-help {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 0;
  border: 0;
  background: none;
  font: inherit;
  font-size: 12.5px;
  color: var(--muted);
  cursor: pointer;
}
.loc-help:hover {
  color: var(--foreground);
}

/* ---- 两张配置卡并排 ---- */
.grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
  align-items: start;
}
.mode-note {
  margin-top: 8px;
}
.mode-locked {
  font-size: 13px;
  color: var(--muted);
}

/* ---- 保存条 ---- */
.save-bar {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  padding: 8px 8px 8px 16px;
  border-radius: var(--r-lg);
  box-shadow: inset 0 0 0 1px var(--border);
  transition:
    background-color 150ms ease,
    box-shadow 150ms ease;
}
.save-bar.is-dirty {
  background: color-mix(in oklab, var(--accent) 8%, transparent);
  box-shadow: inset 0 0 0 1px color-mix(in oklab, var(--accent) 40%, transparent);
}
.save-note {
  margin-right: auto;
  font-size: 12.5px;
  color: var(--muted);
}
.is-dirty .save-note {
  color: var(--accent);
  font-weight: 500;
}

/* ---- 失效 STRM ---- */
.note {
  margin-bottom: 10px;
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
.orphan-actions {
  display: flex;
  justify-content: flex-end;
  margin-top: 14px;
}

@media (max-width: 1280px) {
  .grid {
    grid-template-columns: 1fr;
  }
}
/* 手机：路径独占一行，大小与时间折到下一行 */
@media (max-width: 720px) {
  .loc-edit {
    margin-left: 0;
  }
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
