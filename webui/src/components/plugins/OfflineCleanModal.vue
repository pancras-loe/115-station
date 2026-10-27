<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HModal from '@/components/hero/HModal.vue'
import HTabs from '@/components/hero/HTabs.vue'
import PluginLogPanel from './PluginLogPanel.vue'
import { usePluginLogTab } from './logTab'
import HChip from '@/components/hero/HChip.vue'
import HNumberInput from '@/components/hero/HNumberInput.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import { heroTone } from '@/components/hero/tone'
import FieldRow from '@/components/ui/FieldRow.vue'
import SchedulePicker from '@/components/ui/SchedulePicker.vue'
import TestBanner, { type BannerState } from '@/components/ui/TestBanner.vue'
import { pluginsApi } from '@/api'
import type { OffCleanReq, OffCleanResult } from '@/api/plugins'
import { toastError, useFeedback } from '@/composables/useFeedback'

const show = defineModel<boolean>('show', { default: false })
const { tab, logTabs } = usePluginLogTab(show)
const { message } = useFeedback()

const DEFAULT_CRON = '0 * * * *'
const SCOPE_OPTS = [
  { label: '只清本站提交的', value: false },
  { label: '全部任务', value: true },
]

interface Row {
  id: number
  name: string
  main: boolean
  loggedIn: boolean
  on: boolean
}

const enabled = ref(false)
const cron = ref(DEFAULT_CRON)
const failed = ref(true)
const stuckOn = ref(true)
const stuckHours = ref(24)
const all = ref(false)
const rows = ref<Row[]>([])
const loading = ref(false)
const saving = ref(false)
const preview = ref<BannerState | null>(null)
const previewData = ref<OffCleanResult[]>([])
const previewing = ref(false)

const onCount = computed(() => rows.value.filter((r) => r.on).length)

watch(show, async (v) => {
  if (!v) return
  preview.value = null
  previewData.value = []
  loading.value = true
  try {
    const d = await pluginsApi.offCleanConfig()
    enabled.value = !!d.enabled
    cron.value = d.cron || DEFAULT_CRON
    failed.value = !!d.failed
    stuckOn.value = d.stuck_hours > 0
    stuckHours.value = d.stuck_hours > 0 ? d.stuck_hours : 24
    all.value = !!d.all
    rows.value = (d.accounts ?? []).map((a) => ({ id: a.id, name: a.name, main: a.main, loggedIn: a.logged_in, on: a.on }))
  } catch (e) {
    toastError(e, '读取配置失败')
  } finally {
    loading.value = false
  }
})

function body(ids: number[]): OffCleanReq {
  return {
    enabled: enabled.value,
    cron: cron.value.trim() || DEFAULT_CRON,
    failed: failed.value,
    stuck_hours: stuckOn.value ? stuckHours.value || 24 : 0,
    all: all.value,
    accounts: ids,
  }
}

async function runPreview() {
  // 开着的账号预览开着的；一个都没开时看所有登录了的，打开之前先看一眼
  const pick = rows.value.filter((r) => r.loggedIn && (onCount.value === 0 || r.on))
  if (!pick.length) {
    preview.value = { status: 'err', title: '没有已登录的 115 账号' }
    return
  }
  previewing.value = true
  previewData.value = []
  preview.value = { status: 'pending', title: '读离线任务列表中…' }
  try {
    const d = await pluginsApi.previewOffClean(body(pick.map((r) => r.id)))
    previewData.value = (d.data ?? []).map((a) => ({ ...a, tasks: a.tasks ?? [] }))
    const errs = previewData.value.some((a) => a.error)
    preview.value = { status: errs ? 'err' : 'ok', title: '预览（没有删除任何东西）', detail: d.message }
  } catch (e) {
    preview.value = { status: 'err', title: '预览失败', detail: e instanceof Error ? e.message : '' }
  } finally {
    previewing.value = false
  }
}

async function save() {
  saving.value = true
  try {
    await pluginsApi.saveOffClean(body(rows.value.filter((r) => r.on).map((r) => r.id)))
    message.success('保存成功')
    show.value = false
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}

function sizeText(n: number): string {
  if (!n) return ''
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = n
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(i >= 3 ? 1 : 0)} ${u[i]}`
}

function stateText(t: { status: number; percent: number; hours: number }): string {
  if (t.status === -1) return '下载失败'
  const what = t.status === 0 ? '排队' : `下载中 ${t.percent}%`
  return `${what} · 已 ${t.hours} 小时`
}
</script>

<template>
  <HModal v-model:show="show" title="清理 115 离线任务" width="600px">
    <HTabs v-model="tab" :items="logTabs" variant="secondary" class="plog-tabs" />
    <template v-if="tab === 'set'">
    <p class="lead">
      115 离线大多是秒传，个别磁力会下载失败或卡在百分之几一直不动。这里按计划把它们从 115 的离线列表里删掉，只删任务、不删已经下进网盘的文件；下载完成的不动。
    </p>

    <FieldRow label="执行时间" wide tip="所有打开的账号在同一时间依次清理，有删除或出错时推送一条汇总通知。">
      <SchedulePicker v-model="cron" v-model:enabled="enabled" toggle :placeholder="DEFAULT_CRON" />
    </FieldRow>

    <FieldRow label="下载失败的" tip="115 报「下载失败」的任务一律删掉。">
      <HSwitch v-model="failed" aria-label="删下载失败的任务" />
    </FieldRow>

    <FieldRow label="卡住的" tip="排队或下载中、添加超过这么多小时还没下完的删掉。真在下大文件的也会被删，别设得太短。">
      <div class="inline">
        <HSwitch v-model="stuckOn" aria-label="删卡住的任务" />
        <HNumberInput v-if="stuckOn" v-model="stuckHours" :min="1" :max="2160" aria-label="卡住多少小时算要删" class="num">
          <template #suffix>小时</template>
        </HNumberInput>
      </div>
    </FieldRow>

    <FieldRow label="范围" wide tip="「只清本站提交的」按来源链接认（订阅、找资源、链接转存、机器人提交的），你在 115 App 里自己加的任务不碰。">
      <HSegmented :model-value="all" :options="SCOPE_OPTS" size="sm" aria-label="清理范围" @update:model-value="(v) => (all = v as boolean)" />
    </FieldRow>

    <FieldRow label="账号" wide tip="每个账号的离线列表各清各的。新加的账号默认不清。">
      <div v-if="!rows.length && !loading" class="empty">还没登录 115 账号</div>
      <div class="accts">
        <div v-for="r in rows" :key="r.id" class="acct" :class="{ off: !r.on }">
          <HSwitch v-model="r.on" size="sm" :aria-label="`清理 ${r.name} 的离线任务`" />
          <span class="acct-name">{{ r.name }}</span>
          <HChip v-if="r.main" :color="heroTone('accent')">主号</HChip>
          <HChip v-if="!r.loggedIn" :color="heroTone('warning')">未登录</HChip>
        </div>
      </div>
    </FieldRow>

    <TestBanner :state="preview" />
    <div v-for="a in previewData.filter((x) => x.tasks.length)" :key="a.id" class="pv">
      <div v-if="previewData.length > 1" class="pv-acct">{{ a.account }}</div>
      <div v-for="(t, i) in a.tasks" :key="i" class="pv-row">
        <span class="pv-name" :title="t.name">{{ t.name }}</span>
        <span class="pv-meta" :class="{ bad: t.status === -1 }">{{ stateText(t) }}</span>
        <span v-if="t.size" class="pv-meta">{{ sizeText(t.size) }}</span>
        <HChip v-if="!t.mine" :color="heroTone('warning')">非本站</HChip>
      </div>
      <div v-if="a.matched > a.tasks.length" class="pv-more">…还有 {{ a.matched - a.tasks.length }} 条</div>
    </div>

    </template>
    <PluginLogPanel v-else plugin-key="offclean" />

    <template v-if="tab === 'set'" #footer>
      <div class="foot">
        <HButton variant="tertiary" :loading="previewing" :disabled="!rows.length || (!failed && !stuckOn)" @click="runPreview">
          预览会删哪些
        </HButton>
        <span class="grow" />
        <HButton variant="tertiary" @click="show = false">取消</HButton>
        <HButton variant="primary" :loading="saving" @click="save">保存</HButton>
      </div>
    </template>
  </HModal>
</template>

<style scoped>
.plog-tabs {
  margin-bottom: 14px;
}
.lead {
  margin: 0 0 14px;
  font-size: 12.5px;
  line-height: 1.7;
  color: var(--muted);
}
.inline {
  display: flex;
  align-items: center;
  gap: 10px;
}
.num {
  width: 150px;
}
.empty {
  font-size: 12.5px;
  color: var(--muted);
}
.accts {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.acct {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 12px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.acct-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13px;
  font-weight: 600;
  color: var(--foreground);
}
.acct.off .acct-name {
  color: var(--muted);
}
.pv {
  margin-top: 10px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.pv-acct {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--foreground);
}
.pv-row {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
}
.pv-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--foreground);
}
.pv-meta {
  flex-shrink: 0;
  color: var(--muted);
}
.pv-meta.bad {
  color: var(--danger);
}
.pv-more {
  font-size: 12px;
  color: var(--muted);
}
.foot {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
}
.grow {
  flex: 1;
}
</style>
