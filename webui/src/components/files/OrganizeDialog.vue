<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HModal from '@/components/hero/HModal.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import TmdbPicker from './TmdbPicker.vue'
import { configApi, filesApi } from '@/api'
import type { FileJobBody } from '@/api/files'
import type { TmdbCandidate } from '@/api/resources'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { useQueueStore } from '@/stores/queue'

/**
 * 整理所选条目：与自动整理同一条流水线（识别 → 洗版 → 重命名 → 搬进媒体库 → 写 STRM → 刮削 → 刷 Emby），
 * 只处理勾选的这几项。指定 TMDB 条目时跳过识别，也不会停下来等人工确认。
 */
const props = defineProps<{ body: FileJobBody | null }>()
const show = defineModel<boolean>('show', { required: true })

const { message } = useFeedback()
const queue = useQueueStore()

const mode = ref<'auto' | 'pick'>('auto')
const picked = ref<TmdbCandidate | null>(null)
const submitting = ref(false)
const manualConfirm = ref(false)

const count = computed(() => props.body?.items.length ?? 0)
const firstName = computed(() => props.body?.items[0]?.name ?? '')

watch(show, async (v) => {
  if (!v) return
  mode.value = 'auto'
  picked.value = null
  try {
    const b = await configApi.getSetting<{ manual_confirm?: boolean }>('org-basic', {})
    manualConfirm.value = !!b.manual_confirm
  } catch {
    manualConfirm.value = false
  }
})

const canSubmit = computed(() => !!props.body && (mode.value === 'auto' || !!picked.value))

async function submit() {
  if (!props.body || !canSubmit.value) return
  submitting.value = true
  try {
    const pick = mode.value === 'pick' ? picked.value : null
    const d = await filesApi.organize({
      ...props.body,
      ...(pick ? { tmdb_id: pick.id, media_type: pick.media_type, label: `${pick.title} (${pick.year ?? ''})` } : {}),
    })
    message.success(d.message || '整理已加入任务队列')
    await queue.submitted(d.job_id)
    show.value = false
  } catch (e) {
    toastError(e, '提交失败')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <HModal v-model:show="show" title="整理" width="620px">
    <div class="body">
      <p class="src">
        所选：<b>{{ firstName }}</b>
        <span v-if="count > 1"> 等 {{ count }} 项</span>
      </p>

      <FieldRow label="识别方式">
        <HSegmented
          v-model="mode"
          :options="[
            { label: '自动识别', value: 'auto' },
            { label: '指定 TMDB 条目', value: 'pick' },
          ]"
        />
      </FieldRow>
      <template v-if="mode === 'auto'">
        <p class="note">按整理配置识别：识别规则、识别记忆、AI 增强识别照常生效。</p>
        <HAlert v-if="manualConfirm" status="accent">
          已开启「人工确认」：识别完会先停在「任务中心 → 整理记录」的待确认里，确认后才搬进媒体库。
          想直接入库，请改用「指定 TMDB 条目」。
        </HAlert>
      </template>
      <template v-else>
        <TmdbPicker v-model="picked" :initial="firstName" />
        <HAlert v-if="count > 1" status="warning">
          勾选的 {{ count }} 项都会按这一个条目入库 —— 适合同一部剧分散在几个文件夹里的季，别把不同的片混在一起选。
        </HAlert>
      </template>

      <p class="note">
        文件会被改名并搬进媒体库，写 STRM、刮削、刷新 Emby 一次做完；识别不出来的进冗余目录，
        洗版判输的进已存在目录。结果见「任务中心 → 整理记录」。
      </p>
    </div>

    <template #footer>
      <div class="foot-btns">
        <HButton variant="tertiary" @click="show = false">取消</HButton>
        <HButton variant="primary" :disabled="!canSubmit" :loading="submitting" @click="submit">加入队列整理</HButton>
      </div>
    </template>
  </HModal>
</template>

<style scoped>
.body {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.src {
  margin: 0;
  font-size: 12.5px;
  color: var(--muted);
  word-break: break-all;
}
.src b {
  color: var(--foreground);
  font-weight: 500;
}
.note {
  margin: 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--muted);
}
.foot-btns {
  display: flex;
  gap: 8px;
  margin-left: auto;
}
@media (max-width: 639px) {
  .foot-btns {
    width: 100%;
  }
  .foot-btns > * {
    flex: 1;
  }
}
</style>
