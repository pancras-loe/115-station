<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RefreshCw, ShieldCheck } from '@lucide/vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import HChip from '@/components/hero/HChip.vue'
import { syncApi } from '@/api'
import type { DeepDeleteRecord } from '@/api/sync'
import type { DeepDelSetting } from './deepDelSetting'
import { toastError } from '@/composables/useFeedback'
import { fullTime, relTime } from '@/utils/time'

/** 配置由 SyncPage 持有一份（顶部状态总览也读它），见 deepDelSetting.ts */
const props = defineProps<{ setting: DeepDelSetting }>()
const cfg = computed(() => props.setting.model.value)

const records = ref<DeepDeleteRecord[]>([])
const loading = ref(false)
async function load() {
  loading.value = true
  try { records.value = (await syncApi.deepDeleteRecords(1, 20)).data }
  catch (e) { toastError(e, '删除记录读取失败') }
  finally { loading.value = false }
}
onMounted(load)
// 历史预演记录仅作回显，新版本不再提供预演操作。
const statusLabel: Record<string, string> = { done: '已删', dry_run: '历史预演', rejected: '拦下', failed: '失败' }
const STATUS_COLOR: Record<string, 'success' | 'warning' | 'danger' | 'default'> = {
  done: 'success',
  dry_run: 'default',
  rejected: 'warning',
  failed: 'danger',
}
const reasonLabel: Record<string, string> = { local_scan: '历史扫描', emby_webhook: 'Emby 事件', manual: '历史手动执行', manual_record: '整理记录' }

/**
 * 原来是一整段密密麻麻的提示框，关键的「什么时候会删、什么时候不删」被淹在里面。
 * 拆成条目：哪些会触发、哪些不会、删之前核验什么 —— 行为本身没变（见 AGENTS.md §6.10）
 */
const RULES = {
  trigger: [
    '需要先在 Emby 配好 webhook，接收原生 library.deleted 与神医助手 deep.delete',
    '只处理电影、单集、季、剧集事件，且只动本次事件对应、本地已消失的文件',
    '删除进入 115 回收站，可以还原',
  ],
  skip: [
    '移除媒体库、普通文件夹事件不会触发',
    '直接在磁盘上删 STRM 不会触发（不再定时扫描）',
  ],
  guard: [
    '执行前实时查询 Emby 当前的媒体库，确认本地文件确实缺失',
    'Emby 查询失败、媒体库已移除、目录不可访问时一律拦下',
    '剧 / 季目录缺少台账布局证据时拦下',
    '同步、整理任务运行时会等它结束再执行',
  ],
}
</script>

<template>
  <div class="tab-body">
    <div class="grid">
      <SectionCard title="事件联动" hint="Emby 里删片后，联动删除对应的网盘源文件">
        <FieldRow label="启用事件联动" tip="开启后接收原生 library.deleted 和神医助手 deep.delete。整理记录的深度删除按钮独立可用，不受这个开关影响。">
          <HSwitch v-model="cfg.enabled" aria-label="启用事件联动" />
        </FieldRow>
        <FieldRow label="清理网盘空目录" tip="影片/季目录空了就跟着删，再往上只删完全为空的目录，不会进入其他影片。也适用于整理记录删除。">
          <HSwitch v-model="cfg.prune_pan_dirs" aria-label="清理网盘空目录" />
        </FieldRow>
        <FieldRow label="删除后发通知" tip="一次删除只推一条消息，写明删了几个网盘文件或被拦下的原因。也适用于整理记录删除。">
          <HSwitch v-model="cfg.notify" aria-label="删除后发通知" />
        </FieldRow>

        <HAlert v-if="cfg.enabled" status="warning" class="warn">
          原生删除事件也可能来自 Emby 扫库清理，单凭事件分不清是主动删除还是文件丢失。
          请确认「系统配置 → Emby」里的地址与 API 密钥有效。
        </HAlert>

        <div class="rules">
          <div class="rules-col">
            <div class="rules-title">会删</div>
            <ul><li v-for="r in RULES.trigger" :key="r">{{ r }}</li></ul>
          </div>
          <div class="rules-col">
            <div class="rules-title">不会删</div>
            <ul><li v-for="r in RULES.skip" :key="r">{{ r }}</li></ul>
          </div>
          <div class="rules-col">
            <div class="rules-title"><ShieldCheck :size="13" />删之前核验</div>
            <ul><li v-for="r in RULES.guard" :key="r">{{ r }}</li></ul>
          </div>
        </div>

        <FormActions>
          <HButton variant="primary" :loading="setting.saving.value" :disabled="!setting.dirty.value && !setting.saving.value" @click="setting.save()">保存配置</HButton>
        </FormActions>
      </SectionCard>

      <SectionCard title="删除记录" hint="最近 20 次；刷新只读取记录，不触发扫描或删除">
        <template #extra>
          <HButton size="sm" variant="ghost" :loading="loading" @click="load">
            <template #icon><RefreshCw :size="14" /></template>
            刷新
          </HButton>
        </template>
        <EmptyState v-if="!records.length" text="暂无删除记录" />
        <ul v-else class="records">
          <li v-for="r in records" :key="r.id" class="record">
            <div class="record-head">
              <HChip size="sm" :color="STATUS_COLOR[r.status] ?? 'default'">{{ statusLabel[r.status] ?? r.status }}</HChip>
              <b class="record-title">{{ r.title || '事件处理' }}</b>
              <span class="record-at" :title="fullTime(r.created_at)">{{ relTime(r.created_at) }}</span>
            </div>
            <div class="dim">{{ reasonLabel[r.reason] ?? r.reason }} · 视频 {{ r.video_cnt }} · 附属 {{ r.asset_cnt }}</div>
            <div v-if="r.message" class="record-msg">{{ r.message }}</div>
          </li>
        </ul>
      </SectionCard>
    </div>
  </div>
</template>

<style scoped>
.tab-body { display: flex; flex-direction: column; gap: 16px; }
.grid {
  display: grid;
  grid-template-columns: minmax(0, 1.15fr) minmax(0, 1fr);
  gap: 16px;
  align-items: start;
}
.warn { margin: 6px 0 4px; }

/* 三组规则上下排，每组「标题 | 条目」两栏：卡片只有半屏宽，三列并排会把每条挤成好几行 */
.rules {
  display: flex;
  flex-direction: column;
  margin-top: 12px;
  padding: 4px 14px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.rules-col {
  display: grid;
  grid-template-columns: 88px minmax(0, 1fr);
  gap: 10px;
  padding: 8px 0;
}
.rules-col + .rules-col { border-top: 1px solid var(--separator); }
.rules-title {
  display: flex;
  align-items: flex-start;
  gap: 4px;
  padding-top: 1px;
  font-size: 12px;
  font-weight: 600;
  color: var(--foreground);
}
.rules-title :deep(svg) { margin-top: 2px; }
.rules ul {
  margin: 0;
  padding-left: 14px;
  font-size: 12px;
  line-height: 1.6;
  color: var(--muted);
}
.rules li + li { margin-top: 2px; }

/* 记录多了（每条还可能带长长的拦截原因）会把整页撑得没完没了，限高后在卡片里自己滚 */
.records {
  list-style: none;
  margin: 0;
  padding: 0 6px 0 0;
  display: flex;
  flex-direction: column;
  max-height: min(560px, 65vh);
  overflow-y: auto;
  overscroll-behavior: contain;
}
.record {
  padding: 10px 0;
  border-top: 1px solid var(--separator);
  overflow-wrap: anywhere;
  font-size: 13px;
}
.record:first-child { border-top: 0; padding-top: 0; }
.record-head { display: flex; align-items: center; gap: 8px; margin-bottom: 3px; min-width: 0; }
.record-title {
  min-width: 0;
  font-weight: 600;
  color: var(--foreground);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.record-at { margin-left: auto; flex-shrink: 0; font-size: 12px; color: var(--muted); }
.record-msg { margin-top: 4px; font-size: 12.5px; color: color-mix(in oklab, var(--foreground) 75%, var(--muted)); }
.dim { color: var(--muted); font-size: 12px; }

@media (max-width: 1280px) {
  .grid { grid-template-columns: 1fr; }
}
@media (max-width: 720px) {
  .rules-col { grid-template-columns: 1fr; gap: 4px; }
  .records { max-height: 60vh; }
}
</style>
