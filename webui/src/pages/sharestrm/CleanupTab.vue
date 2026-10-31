<script setup lang="ts">
import { computed, ref } from 'vue'
import { Archive, Trash2 } from '@lucide/vue'
import HButton from '@/components/hero/HButton.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import { shareStrmApi } from '@/api'
import type { ShareSource } from '@/api/sharestrm'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { relTime } from '@/utils/time'
import { cleanupList } from './cleanup'

/**
 * 待清理：连续 3 次查到分享已失效的（SHARE-STRM-PLAN.md §7.2）。
 * 本地 STRM 不自动删 —— 115 偶尔对正常分享也回怪状态，删了就要重新生成；由用户看过再删，或者先留着。
 * 分享里单个文件没了的，后台隔一轮确认后自己删，不在这里。
 */
const props = defineProps<{ list: ShareSource[] }>()
const emit = defineEmits<{ changed: [] }>()
const { message, dialog } = useFeedback()

const items = computed(() => cleanupList(props.list))
const busyId = ref(0)

function why(s: ShareSource) {
  if (s.share_state === '4') return '分享已取消'
  if (s.share_state === '7') return '分享已过期'
  return s.last_error || '分享已失效'
}

async function del(s: ShareSource) {
  const ok = await dialog.confirm({
    title: `删除「${s.title || s.share_code}」`,
    content: `会删掉本地「${s.local_dir}」下本站写的 ${s.files} 个 STRM 和附属文件，并通知 Emby。115 上什么都不动。`,
    tone: 'danger',
    actions: [
      { label: '取消', value: false, variant: 'tertiary' },
      { label: '删除', value: true, variant: 'danger' },
    ],
  })
  if (!ok) return
  busyId.value = s.id
  try {
    message.success((await shareStrmApi.remove(s.id)).message)
    emit('changed')
  } catch (e) {
    toastError(e, '删除失败')
  } finally {
    busyId.value = 0
  }
}

async function keep(s: ShareSource) {
  busyId.value = s.id
  try {
    await shareStrmApi.update(s.id, { ignored: true })
    message.success('先留着：不再列在这里。之后检查又正常了会自动恢复')
    emit('changed')
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    busyId.value = 0
  }
}

async function delAll() {
  const ok = await dialog.confirm({
    title: `删除全部 ${items.value.length} 个失效分享`,
    content: '逐个删掉它们在本地的 STRM 与附属文件，并通知 Emby。115 上什么都不动。',
    tone: 'danger',
    actions: [
      { label: '取消', value: false, variant: 'tertiary' },
      { label: '全部删除', value: true, variant: 'danger' },
    ],
  })
  if (!ok) return
  let failed = 0
  for (const s of [...items.value]) {
    busyId.value = s.id
    try {
      await shareStrmApi.remove(s.id)
    } catch {
      failed++
    }
  }
  busyId.value = 0
  if (failed) message.error(`${failed} 个没删成（可能正在检查），稍后再试`)
  else message.success('已全部删除')
  emit('changed')
}
</script>

<template>
  <SectionCard title="待清理" hint="连续 3 次检查都是「分享已取消 / 过期 / 被屏蔽」的分享。它们的 STRM 已经播不了，删掉还是先留着由你决定。">
    <template v-if="items.length > 1" #extra>
      <HButton size="sm" variant="danger-soft" @click="delAll">
        <template #icon><Trash2 :size="14" /></template>
        全部删除
      </HButton>
    </template>
    <EmptyState v-if="!items.length" text="没有失效的分享。" />
    <div v-else class="rows">
      <div v-for="s in items" :key="s.id" class="row">
        <div class="main">
          <div class="title" :title="s.title">{{ s.title || s.share_code }}</div>
          <div class="meta">
            <span class="bad">{{ why(s) }}</span>
            <span>{{ s.files }} 个 STRM</span>
            <span class="mono">本地：{{ s.local_dir }}</span>
            <span>最后检查 {{ relTime(s.last_check) }}</span>
          </div>
        </div>
        <div class="acts">
          <HButton size="sm" variant="tertiary" :disabled="busyId === s.id" @click="keep(s)">
            <template #icon><Archive :size="14" /></template>
            先留着
          </HButton>
          <HButton size="sm" variant="danger-soft" :loading="busyId === s.id" @click="del(s)">
            <template #icon><Trash2 :size="14" /></template>
            删除
          </HButton>
        </div>
      </div>
    </div>
  </SectionCard>
</template>

<style scoped>
.rows {
  display: flex;
  flex-direction: column;
}
.row {
  display: flex;
  gap: 12px;
  align-items: center;
  padding: 12px 0;
  border-top: 1px solid var(--border);
}
.row:first-child {
  border-top: 0;
  padding-top: 0;
}
.main {
  flex: 1;
  min-width: 0;
}
.title {
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.meta {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 14px;
  margin-top: 4px;
  font-size: 12.5px;
  color: var(--muted);
}
.bad {
  color: var(--danger);
}
.mono {
  font-family: var(--font-mono, ui-monospace, monospace);
}
.acts {
  display: flex;
  gap: 4px;
}
@media (max-width: 720px) {
  .row {
    flex-direction: column;
    align-items: flex-start;
  }
}
</style>
