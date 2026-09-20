<script setup lang="ts">
/**
 * 深度删除：本地 STRM 没了 → 把网盘上的源文件也删掉。
 *
 * 独立成页签而不是挂在「全量同步」下面：它与全量同步**没有依赖关系**。
 * 失效 STRM 检测放在全量页是因为标记就是在全量同步末尾打的；深度删除是
 * 独立的本地扫描 + Emby webhook，跟全量唯一的交集只是抢同一把锁。
 */
import { computed, onMounted, ref } from 'vue'
import { NAlert, NButton, NInputNumber, NPopconfirm, NRadioButton, NRadioGroup, NSwitch } from 'naive-ui'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import { syncApi } from '@/api'
import type { DeepDeleteRecord, DeepDeleteReport } from '@/api/sync'
import { defaultDeepDel, useDeepDelSetting } from './deepDelSetting'
import { useTaskStore } from '@/stores/task'
import { toastError, useFeedback } from '@/composables/useFeedback'

const { message } = useFeedback()
const task = useTaskStore()
const setting = useDeepDelSetting()
const cfg = computed(() => setting.model.value)

const report = ref<DeepDeleteReport | null>(null)
const records = ref<DeepDeleteRecord[]>([])
const running = ref(false)
const busy = computed(() => running.value || task.status.running)

async function load() {
  try {
    report.value = await syncApi.deepDelete()
  } catch {
    report.value = null
  }
  try {
    records.value = (await syncApi.deepDeleteRecords(1, 10)).data
  } catch {
    records.value = []
  }
}
onMounted(load)

/** 占比上限在界面上按百分比填，存进配置的是 0~1 的小数 */
const ratioPct = computed({
  get: () => Math.round((cfg.value.max_ratio ?? 0.1) * 100),
  set: (v: number) => {
    cfg.value.max_ratio = (v || 0) / 100
  },
})

async function run(dryRun: boolean) {
  running.value = true
  try {
    const d = await syncApi.runDeepDelete(dryRun)
    message.success(d.message ?? (dryRun ? '预演完成' : '深度删除完成'))
    await load()
  } catch (e) {
    toastError(e, dryRun ? '预演失败' : '深度删除失败')
  } finally {
    running.value = false
  }
}

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

const statusLabel: Record<string, string> = {
  done: '已删',
  dry_run: '预演',
  rejected: '拦下',
  failed: '失败',
}
const reasonLabel: Record<string, string> = {
  local_scan: '本地扫描',
  emby_webhook: 'Emby 事件',
  manual: '手动执行',
  manual_record: '整理记录',
}
</script>

<template>
  <div class="tab-body">
    <SectionCard title="深度删除" hint="本地 STRM 没了 → 把网盘上的源文件也删掉">
      <NAlert class="note" type="info" :bordered="false">
        在 Emby 里删掉一部片子，Emby 会连带删掉本地的 .strm / .nfo / 海报，但
        <b>网盘上的源文件纹丝不动</b> —— 下一次全量同步又把 STRM 生成回来，条目原地复活。
        打开这个开关就是补这个缺口。<b>删除进 115 回收站，可还原。</b>
        扫描是纯本地检查，不消耗任何 115 请求。
      </NAlert>

      <FieldRow
        label="启用深度删除"
        tip="关掉之后后台不再扫描，Emby 的删除事件也不会触发任何动作。已经打上的标记会留着。"
      >
        <NSwitch v-model:value="cfg.enabled" />
      </FieldRow>

      <template v-if="cfg.enabled">
        <FieldRow
          label="删除方式"
          tip="只标记：扫出来放在下面等你确认，什么都不删（推荐先用这个跑几天）。自动删除：扫到就删，但仍要连续两轮都确认缺失，且受下面的阈值保护。"
        >
          <NRadioGroup v-model:value="cfg.mode">
            <NRadioButton value="mark">只标记</NRadioButton>
            <NRadioButton value="auto">自动删除</NRadioButton>
          </NRadioGroup>
        </FieldRow>

        <FieldRow
          label="预演模式"
          tip="只把「将要删哪些」打进日志，不做任何改动。第一次启用务必先开着跑一轮，确认日志里的路径就是你想删的那些，再关掉。"
        >
          <NSwitch v-model:value="cfg.dry_run" />
        </FieldRow>

        <FieldRow
          label="扫描间隔"
          tip="多久检查一次本地文件还在不在。纯本地 os.Stat，不发 115 请求。填 0 = 关掉后台扫描，只保留下面的手动按钮与 Emby 事件触发。"
        >
          <NInputNumber v-model:value="cfg.scan_interval_sec" :min="0" :max="86400" :step="60">
            <template #suffix>秒</template>
          </NInputNumber>
        </FieldRow>

        <template v-if="cfg.mode === 'auto'">
          <FieldRow
            label="单轮上限（视频数）"
            tip="一轮扫出的待删视频超过这个数就拒绝自动删除，只留标记并发告警。挂载掉线、路径配置改错会让整库「消失」，量级和正常删一部片子差着数量级，这个阈值卡的就是那个差距。你在下面手动确认时不受此限制。"
          >
            <NInputNumber v-model:value="cfg.max_batch" :min="1" :max="100000" />
          </FieldRow>

          <FieldRow
            label="单轮占比上限"
            tip="同上，按「待删文件数占台账总数的比例」再卡一道。小媒体库里几十个文件就可能是整库，用绝对条数拦不住。"
          >
            <NInputNumber v-model:value="ratioPct" :min="1" :max="100">
              <template #suffix>%</template>
            </NInputNumber>
          </FieldRow>
        </template>

        <FieldRow
          label="清理网盘空目录"
          tip="删完源文件后，把因此变空的网盘目录一并删掉（季目录、标题目录等），免得 Emby 扫出一堆没有剧集的空条目。媒体库根与整理工作区目录永远不会被删。"
        >
          <NSwitch v-model:value="cfg.prune_pan_dirs" />
        </FieldRow>

        <FieldRow label="删除后发通知" tip="删了什么、删了多少，推到已配置的消息通道。">
          <NSwitch v-model:value="cfg.notify" />
        </FieldRow>
      </template>

      <FormActions>
        <NButton type="primary" :loading="setting.saving.value" @click="setting.save()">
          保存配置
        </NButton>
        <NButton :disabled="busy" @click="Object.assign(cfg, defaultDeepDel())">
          重置为默认值
        </NButton>
      </FormActions>
    </SectionCard>

    <SectionCard
      v-if="cfg.enabled || (report && report.total > 0)"
      title="待删清单"
      hint="本地文件已被删除，网盘上的源文件还在"
    >
      <NAlert v-if="report?.scan_error" class="note" type="error" :bordered="false">
        本轮扫描已放弃：{{ report.scan_error }}
        <br />
        这通常是媒体库目录没挂上或本地路径配置被改过。在确认之前不会删除任何东西。
      </NAlert>

      <template v-if="report && report.total > 0">
        <NAlert class="note" type="warning" :bordered="false">
          共 {{ report.total }} 个（台账 {{ report.ledger_total }} 条，占
          {{ (report.ratio * 100).toFixed(1) }}%）。执行会删除这些文件在
          <b>115 网盘上的源文件</b>，同时清掉本地残留与台账记录。
          删除进 115 回收站，可还原。刚标记的条目要等下一轮扫描再次确认缺失后才会被删。
        </NAlert>

        <div class="path-list">
          <div v-for="it in report.sample" :key="it.rel_path" class="path-row">
            <span class="path-kind">{{ it.kind === 'video' ? 'STRM' : '附属' }}</span>
            <span class="path-main">{{ it.rel_path }}</span>
            <span class="path-meta">{{ humanSize(it.size) }} · {{ it.marked_at }}</span>
          </div>
          <div v-if="report.total > report.sample.length" class="path-more">
            仅显示前 {{ report.sample_limit }} 条，其余
            {{ report.total - report.sample.length }} 条未列出
          </div>
        </div>

        <FormActions>
          <NButton :disabled="busy" :loading="running" @click="void run(true)">
            预演（只看不删）
          </NButton>
          <NPopconfirm @positive-click="void run(false)">
            <template #trigger>
              <NButton type="error" ghost :disabled="busy" :loading="running">
                删除网盘源文件
              </NButton>
            </template>
            确定删除这些文件在 115 网盘上的源文件？文件会进入 115 回收站，可以还原。
          </NPopconfirm>
          <NButton :disabled="busy" @click="load">刷新</NButton>
        </FormActions>
      </template>

      <template v-else>
        <NAlert class="note" type="success" :bordered="false">
          当前没有「本地已删、网盘还在」的条目。<template v-if="cfg.scan_interval_sec > 0">
            标记每 {{ Math.round(cfg.scan_interval_sec / 60) }} 分钟刷新一次，</template
          >打开本页也会顺带扫一遍。
        </NAlert>
        <FormActions>
          <NButton :disabled="busy" @click="load">重新扫描</NButton>
        </FormActions>
      </template>
    </SectionCard>

    <!-- 删除记录：删的是回收站，用户事后要还原时得知道当时删了什么 -->
    <SectionCard v-if="records.length" title="删除记录" hint="删掉的东西都在 115 回收站里">
      <div class="path-list">
        <div v-for="r in records" :key="r.id" class="path-row">
          <span class="path-kind">{{ statusLabel[r.status] ?? r.status }}</span>
          <span class="path-main">
            {{ r.title }}
            <span class="dim">· {{ reasonLabel[r.reason] ?? r.reason }}</span>
            <template v-if="r.message"> —— {{ r.message }}</template>
          </span>
          <span class="path-meta">
            视频 {{ r.video_cnt }} · 附属 {{ r.asset_cnt }} · {{ r.created_at }}
          </span>
        </div>
      </div>
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
.path-list {
  max-height: 300px;
  overflow-y: auto;
  padding: 8px 11px;
  border-radius: var(--radius);
  background: var(--c-bg-raised);
  border: 1px solid var(--c-border);
  font-size: 12px;
  line-height: 1.9;
}
.path-row {
  display: flex;
  gap: 8px;
  align-items: baseline;
}
.path-kind {
  flex: none;
  width: 34px;
  color: var(--c-text-3);
}
.path-main {
  flex: 1;
  overflow-wrap: anywhere;
  color: var(--c-text-1);
}
.dim {
  color: var(--c-text-3);
}
.path-meta {
  flex: none;
  color: var(--c-text-3);
  font-variant-numeric: tabular-nums;
}
.path-more {
  margin-top: 4px;
  color: var(--c-text-3);
}
</style>
