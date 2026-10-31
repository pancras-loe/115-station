<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HTabs from '@/components/hero/HTabs.vue'
import StatusStrip, { type StripItem } from '@/components/ui/StatusStrip.vue'
import PluginLogPanel from '@/components/plugins/PluginLogPanel.vue'
import SourcesTab from './sharestrm/SourcesTab.vue'
import SettingsTab from './sharestrm/SettingsTab.vue'
import CleanupTab from './sharestrm/CleanupTab.vue'
import { cleanupList } from './sharestrm/cleanup'
import { shareStrmApi } from '@/api'
import type { ShareSource } from '@/api/sharestrm'
import { useShareStrm } from '@/composables/shareStrm'
import { useTabQuery } from '@/composables/useTabQuery'
import { useQueueStore } from '@/stores/queue'
import { relTime } from '@/utils/time'

/**
 * 分享 STRM（后端 sharestrm.go，设计见 docs/115-station-notes/SHARE-STRM-PLAN.md）：
 * 不转存、不占网盘空间，本地 STRM 直接指向 115 分享，播放时按分享换直链。
 * 总开关在「设置」页签；关着时侧栏不显示这一页，从扩展功能的卡片进来。
 */
const tab = useTabQuery('sources')
const router = useRouter()
const queue = useQueueStore()
const ss = useShareStrm()

const list = ref<ShareSource[]>([])
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    list.value = (await shareStrmApi.sources()).sources ?? []
  } catch {
    // 列表读不到时保留上一次的
  } finally {
    loading.value = false
  }
}

const off = queue.onFinished((j) => {
  if (j.kind === 'sharestrm') void load()
})
onMounted(() => {
  void load()
  void ss.reload()
})
onUnmounted(off)

const cleanupCount = computed(() => cleanupList(list.value).length)
const tabs = computed(() => [
  { value: 'sources', label: '分享', count: list.value.length },
  { value: 'cleanup', label: '待清理', count: cleanupCount.value, countTone: 'danger' as const },
  { value: 'settings', label: '设置' },
  { value: 'log', label: '日志' },
])

const strip = computed<StripItem[]>(() => {
  const d = ss.resp.value
  const ok = list.value.filter((s) => s.state === 'ok').length
  const bad = list.value.filter((s) => s.state === 'gone' || s.state === 'badcode' || s.state === 'error').length
  const strm = list.value.reduce((n, s) => n + (s.files || 0), 0)
  const last = list.value.map((s) => s.last_check).filter(Boolean).sort().pop()
  return [
    {
      key: 'switch',
      title: '开关',
      tone: !d ? 'loading' : !d.config.enabled ? 'off' : ss.blocked.value ? 'warn' : 'ok',
      text: !d ? '' : !d.config.enabled ? '没开' : ss.blocked.value ? ss.blocked.value : '已开',
      action: '设置',
    },
    {
      key: 'shares',
      title: '分享',
      tone: cleanupCount.value ? 'danger' : bad ? 'warn' : list.value.length ? 'ok' : 'off',
      text: list.value.length ? `${list.value.length} 个，正常 ${ok}${bad ? `，有问题 ${bad}` : ''}` : '还没有',
      action: cleanupCount.value ? `${cleanupCount.value} 个待清理` : undefined,
    },
    { key: 'strm', title: 'STRM', tone: strm ? 'ok' : 'off', text: `${strm} 个`, tip: d ? `本地目录：${d.root_abs}` : undefined },
    { key: 'last', title: '最近检查', tone: last ? 'ok' : 'off', text: last ? relTime(last) : '没有' },
  ]
})

function onStrip(key: string) {
  if (key === 'switch') tab.value = 'settings'
  if (key === 'shares') tab.value = 'cleanup'
}
</script>

<template>
  <div class="page">
    <StatusStrip :items="strip" @act="onStrip" />

    <HAlert v-if="ss.resp.value && !ss.enabled.value && tab !== 'settings'" status="warning" title="分享 STRM 没开">
      打开后，贴 115 分享链接就能不转存、直接生成 STRM 播放（不占网盘空间）。已经生成的 STRM 不受开关影响，照常能播。
      <template #actions>
        <HButton size="sm" variant="secondary" @click="tab = 'settings'">去打开</HButton>
      </template>
    </HAlert>

    <HTabs v-model="tab" :items="tabs" variant="secondary" />

    <SourcesTab v-if="tab === 'sources'" :list="list" :loading="loading" @changed="load" @settings="tab = 'settings'" />
    <CleanupTab v-else-if="tab === 'cleanup'" :list="list" @changed="load" />
    <SettingsTab v-else-if="tab === 'settings'" :sources="list.length" @saved="ss.reload()" @go-strm="router.push({ name: 'sync' })" />
    <section v-else class="card card--default log-card">
      <PluginLogPanel plugin-key="sharestrm" />
    </section>
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.log-card {
  padding: 16px;
}
</style>
