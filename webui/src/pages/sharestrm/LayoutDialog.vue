<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HModal from '@/components/hero/HModal.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import TmdbPicker from '@/components/files/TmdbPicker.vue'
import { shareStrmApi } from '@/api'
import { LAYOUT_OPTIONS, type ShareLayout, type ShareSource } from '@/api/sharestrm'
import type { TmdbCandidate } from '@/api/resources'
import { toastError, useFeedback } from '@/composables/useFeedback'

/**
 * 一个分享怎么放：原样 / 本地整理，以及手动指定它是哪一部（认错了、认不出时用）。
 * 改完后端马上重新生成一次，本地文件挪到新位置（只动本地，115 不动）
 */
const props = defineProps<{ source: ShareSource | null }>()
const show = defineModel<boolean>('show', { required: true })
const emit = defineEmits<{ saved: [] }>()
const { message } = useFeedback()

const layout = ref<ShareLayout>('keep')
const pickMode = ref<'auto' | 'pick'>('auto')
const picked = ref<TmdbCandidate | null>(null)
const saving = ref(false)

watch(show, (v) => {
  if (!v || !props.source) return
  layout.value = props.source.layout === 'organize' ? 'organize' : 'keep'
  pickMode.value = props.source.manual ? 'pick' : 'auto'
  picked.value = null
})

const pickOptions = [
  { label: '自动认', value: 'auto' as const },
  { label: '手动指定', value: 'pick' as const },
]
const initial = computed(() => props.source?.media_title || props.source?.title || '')

async function save() {
  const s = props.source
  if (!s) return
  if (layout.value === 'organize' && pickMode.value === 'pick' && !picked.value && !s.manual) {
    message.warning('选一部片，或者改成「自动认」')
    return
  }
  saving.value = true
  try {
    const body: shareStrmApi.ShareUpdate = { layout: layout.value }
    if (layout.value === 'organize') {
      if (pickMode.value === 'pick' && picked.value) body.pick = { tmdb_id: picked.value.id, media_type: picked.value.media_type }
      else if (pickMode.value === 'auto' && (s.manual || s.tmdb_id)) body.pick = { tmdb_id: 0, media_type: '' } // 清掉，重新按名字认
    }
    const d = await shareStrmApi.update(s.id, body)
    message.success(d.message)
    show.value = false
    emit('saved')
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <HModal v-model:show="show" :title="`放法：${source?.title || source?.share_code || ''}`" width="620px">
    <FieldRow
      label="放法"
      tip="原样：按分享里的目录放在分享目录下。本地整理：认出是哪部片，按「自动整理」的命名模板与分类方案放（只改本地的名字和位置，115 不动）。"
    >
      <HSegmented v-model="layout" :options="LAYOUT_OPTIONS" size="sm" />
    </FieldRow>

    <template v-if="layout === 'organize'">
      <p v-if="source?.media_title" class="now">
        现在认成：<b>{{ source.media_title }}</b><span v-if="source.manual">（手动指定）</span>
        <span v-if="source.category"> · 放在 {{ source.category }}</span>
      </p>
      <p v-else-if="source?.recog_note" class="now warn">没认出：{{ source.recog_note }}</p>

      <FieldRow label="是哪部" tip="自动认：按文件名和目录名搜 TMDB（同整理）；认错了、认不出时手动指定。">
        <HSegmented v-model="pickMode" :options="pickOptions" size="sm" />
      </FieldRow>
      <TmdbPicker v-if="pickMode === 'pick'" v-model="picked" :initial="initial" />
    </template>

    <template #footer>
      <div class="foot-right">
        <HButton variant="tertiary" @click="show = false">取消</HButton>
        <HButton variant="primary" :loading="saving" @click="save">保存并重新生成</HButton>
      </div>
    </template>
  </HModal>
</template>

<style scoped>
.now {
  margin: 4px 0 10px;
  font-size: 13px;
  color: var(--muted);
}
.now.warn {
  color: var(--warning);
}
.foot-right {
  display: flex;
  gap: 8px;
  justify-content: flex-end;
}
</style>
