<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NAlert, NButton, NSwitch } from 'naive-ui'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import { syncApi } from '@/api'
import type { DeepDeleteRecord } from '@/api/sync'
import { useDeepDelSetting } from './deepDelSetting'
import { toastError } from '@/composables/useFeedback'

const setting = useDeepDelSetting()
const cfg = computed(() => setting.model.value)
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
const reasonLabel: Record<string, string> = { local_scan: '历史扫描', emby_webhook: 'Emby 事件', manual: '历史手动执行', manual_record: '整理记录' }
</script>

<template>
  <div class="tab-body">
    <SectionCard title="深度删除" hint="收到 Emby 删除事件后，联动删除对应的网盘源文件">
      <NAlert type="info" :bordered="false">
        需先配置 Emby webhook。仅处理本次事件对应且本地已消失的文件，删除进入 115 回收站。
        仅接受电影、单集、季、剧集事件；移除媒体库和普通文件夹不会触发源文件删除。
        执行前核验 Emby 当前媒体库及本地缺失；同步任务运行时会等待其结束。
        不再定时扫描，直接在磁盘上删除 STRM 不会触发网盘删除。
      </NAlert>
      <FieldRow label="启用事件联动" tip="开启后接收原生 library.deleted 和神医助手 deep.delete。整理记录的深度删除按钮独立可用。">
        <NSwitch v-model:value="cfg.enabled" />
      </FieldRow>
      <NAlert v-if="cfg.enabled" type="warning" :bordered="false">
        请配置有效的 Emby 地址与 API 密钥，查询失败、媒体库已移除或目录不可访问时会拦截删除。
        原生事件仍可能来自扫库清理，无法仅凭事件区分主动删除与文件丢失；剧/季目录缺少台账布局证据时也会拦截。
      </NAlert>
      <FieldRow label="清理网盘空目录" tip="影片/季目录空了就跟着删，再往上只删完全为空的目录，不会进入其他影片。也适用于整理记录删除。">
        <NSwitch v-model:value="cfg.prune_pan_dirs" />
      </FieldRow>
      <FieldRow label="删除后发通知" tip="也适用于整理记录删除。">
        <NSwitch v-model:value="cfg.notify" />
      </FieldRow>
      <FormActions>
        <NButton type="primary" :loading="setting.saving.value" @click="setting.save()">保存配置</NButton>
      </FormActions>
    </SectionCard>
    <SectionCard title="删除记录" hint="最近 20 次；刷新只读取记录，不触发扫描或删除">
      <NAlert v-if="!records.length" type="info" :bordered="false">暂无删除记录</NAlert>
      <div v-for="r in records" :key="r.id" class="record">
        <div>{{ statusLabel[r.status] ?? r.status }} · {{ r.title || '事件处理' }} · {{ reasonLabel[r.reason] ?? r.reason }}</div>
        <div class="dim">视频 {{ r.video_cnt }} · 附属 {{ r.asset_cnt }} · {{ r.created_at }}</div>
        <div v-if="r.message">{{ r.message }}</div>
      </div>
      <FormActions><NButton :loading="loading" @click="load">刷新记录</NButton></FormActions>
    </SectionCard>
  </div>
</template>

<style scoped>
.tab-body { display: flex; flex-direction: column; gap: 16px; }
.record { padding: 10px 0; border-bottom: 1px solid var(--c-border); overflow-wrap: anywhere; }
.dim { color: var(--c-text-3); font-size: 12px; }
</style>
