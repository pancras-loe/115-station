<script setup lang="ts">
import { ref, watch } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HModal from '@/components/hero/HModal.vue'
import HSkeleton from '@/components/hero/HSkeleton.vue'
import { organizeApi } from '@/api'
import type { DupScanItem } from '@/api/organize'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { useQueueStore } from '@/stores/queue'
import { bytes } from '@/utils/format'

/**
 * 库内同集多份体检（orgdupscan.go）：只读台账，找出库里已经并排躺着的同一集几份 ——
 * 两份同名的、被 115 自动改成 xxx(1) 的。有整理记录的可以发起一次严格重新整理：
 * 任务会停下，记录上出现「选择保留」，和整理时撞名同一套。
 */
const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ 'update:show': [boolean] }>()

const { message } = useFeedback()
const queue = useQueueStore()
const items = ref<DupScanItem[]>([])
const loading = ref(false)
const acting = ref(0)

watch(
  () => props.show,
  async (v) => {
    if (!v) return
    loading.value = true
    try {
      items.value = (await organizeApi.dupScan()).data ?? []
    } catch (e) {
      toastError(e, '体检失败')
    } finally {
      loading.value = false
    }
  },
)

const KIND_TEXT: Record<string, string> = { same_name: '两份同名', auto_renamed: '115 自动改名' }

/** 目录只显示片目以下那段（片名 / Season 4），太长的从左边截掉 */
function shortDir(it: DupScanItem) {
  const i = it.title ? it.dir.indexOf(it.title) : -1
  return i >= 0 ? it.dir.slice(i) : it.dir
}

async function handle(it: DupScanItem) {
  if (!it.record_id) return
  acting.value = it.record_id
  try {
    const d = await organizeApi.dupScanRedo(it.record_id)
    message.success(`${d.message}：任务停下后到整理记录上「选择保留」`)
    await queue.submitted(d.job_id)
  } catch (e) {
    toastError(e, '提交失败')
  } finally {
    acting.value = 0
  }
}
</script>

<template>
  <HModal :show="show" title="库内同集多份" width="680px" @update:show="emit('update:show', $event)">
    <div class="body">
      <p class="intro">
        只看本地台账，不请求 115。列出库里同一个目录下疑似同一集的几份：两份同名的，或被 115 自动改成
        <code>xxx(1)</code> 的。「处理」会按整理记录重新整理一次，任务停下后在那条记录上选择保留哪份。
      </p>

      <div v-if="loading" class="list" aria-busy="true">
        <HSkeleton v-for="i in 3" :key="i" height="56px" radius="12px" />
      </div>
      <p v-else-if="!items.length" class="empty">没有发现疑似同集多份。</p>

      <ul v-else class="list">
        <li v-for="it in items" :key="it.dir + it.files[0]?.file_id" class="item">
          <div class="item-main">
            <div class="item-head">
              <HChip :color="it.kind === 'auto_renamed' ? 'danger' : 'warning'">{{ KIND_TEXT[it.kind] ?? it.kind }}</HChip>
              <b class="item-dir" :title="it.dir">{{ shortDir(it) }}</b>
            </div>
            <span v-for="f in it.files" :key="f.file_id" class="item-file" :title="f.name">
              {{ f.name }}<template v-if="f.size"> · {{ bytes(f.size) }}</template>
            </span>
            <span v-if="!it.record_id" class="item-hint">
              没有登记了这几份的整理记录：到「网盘文件」进到这个片目，用「整理」处理
            </span>
          </div>
          <HButton
            v-if="it.record_id"
            size="sm"
            variant="secondary"
            :loading="acting === it.record_id"
            :disabled="!!acting"
            @click="handle(it)"
          >
            处理
          </HButton>
        </li>
      </ul>
    </div>

    <template #footer>
      <div class="foot-btns">
        <HButton variant="tertiary" @click="emit('update:show', false)">关闭</HButton>
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
.intro,
.empty {
  margin: 0;
  font-size: 12.5px;
  color: var(--muted);
}
.intro code {
  color: var(--foreground);
}
.list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.item-main {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
  flex: 1;
}
.item-head {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.item-dir {
  font-size: 13px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.item-file,
.item-hint {
  font-size: 12px;
  color: var(--muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.item-hint {
  white-space: normal;
}
.foot-btns {
  display: flex;
  margin-left: auto;
}
</style>
