<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HCheckbox from '@/components/hero/HCheckbox.vue'
import HModal from '@/components/hero/HModal.vue'
import HSkeleton from '@/components/hero/HSkeleton.vue'
import MediaTypeChip from '@/components/MediaTypeChip.vue'
import { libraryApi } from '@/api'
import type { CatMoveItem } from '@/api/library'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { useQueueStore } from '@/stores/queue'

/**
 * 改过分类方案之后，把网盘上还在旧分类目录里的片目按新方案搬过去（后端 catmove.go）。
 * 打开时预演：每部片按 TMDB 详情重新分类，列出搬到哪 / 为什么不搬；勾选后入任务队列。
 * 执行时后端重新算一遍，不拿这里的结果
 */
const props = defineProps<{ show: boolean; schemeId?: number }>()
const emit = defineEmits<{ 'update:show': [boolean]; done: [] }>()

const { message } = useFeedback()
const queue = useQueueStore()
const items = ref<CatMoveItem[]>([])
const picked = ref(new Set<string>())
const loading = ref(false)
const submitting = ref(false)

watch(
  () => props.show,
  async (v) => {
    if (!v) return
    loading.value = true
    items.value = []
    try {
      const d = await libraryApi.catMovePreview(props.schemeId)
      items.value = d.items ?? []
      picked.value = new Set(items.value.filter((i) => i.move).map((i) => i.key))
    } catch (e) {
      toastError(e, '预演失败')
    } finally {
      loading.value = false
    }
  },
)

const movable = computed(() => items.value.filter((i) => i.move))
const skipped = computed(() => items.value.filter((i) => !i.move))
const allPicked = computed(() => movable.value.length > 0 && picked.value.size === movable.value.length)

function toggle(key: string, on: boolean) {
  const s = new Set(picked.value)
  if (on) s.add(key)
  else s.delete(key)
  picked.value = s
}
function toggleAll(on: boolean) {
  picked.value = new Set(on ? movable.value.map((i) => i.key) : [])
}

function label(i: CatMoveItem) {
  return i.title ? `${i.title}${i.year ? ` (${i.year})` : ''}` : i.title_dir
}

async function submit() {
  submitting.value = true
  try {
    const keys = picked.value.size === movable.value.length ? undefined : [...picked.value]
    const d = await libraryApi.catMoveApply({ scheme_id: props.schemeId, keys })
    message.success(d.message || '已加入任务队列')
    await queue.submitted(d.job_id)
    emit('done')
    emit('update:show', false)
  } catch (e) {
    toastError(e, '提交失败')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <HModal :show="show" title="按新分类方案搬移网盘目录" width="720px" @update:show="emit('update:show', $event)">
    <div class="body">
      <p class="intro">
        这些片是按旧方案整理进来的，网盘上还在旧的分类目录里。每部按 TMDB 详情用现在的方案重新分类，整个片目目录在 115
        上挪过去，本地 STRM、台账、整理记录跟着改，再通知 Emby。文件名不改；旧分类目录挪空了会删进 115 回收站。
      </p>

      <div v-if="loading" class="list" aria-busy="true">
        <HSkeleton v-for="i in 4" :key="i" height="44px" radius="12px" />
      </div>
      <p v-else-if="!items.length" class="empty">网盘上的分类目录已经和方案一致，没有要搬的。</p>

      <template v-else>
        <div v-if="movable.length" class="head">
          <HCheckbox :checked="allPicked" :indeterminate="!allPicked && picked.size > 0" @update:checked="toggleAll" />
          <span>要搬 {{ picked.size }} / {{ movable.length }} 部</span>
        </div>
        <ul class="list">
          <li v-for="i in movable" :key="i.key" class="item">
            <HCheckbox :checked="picked.has(i.key)" @update:checked="toggle(i.key, $event)" />
            <MediaTypeChip :type="i.media_type" />
            <div class="item-main">
              <b class="item-title" :title="i.title_dir">{{ label(i) }}</b>
              <span class="item-path">{{ i.lib_name }}：{{ i.old_cat }} → {{ i.new_cat }}</span>
            </div>
          </li>
        </ul>
        <HAlert v-if="skipped.length" status="warning">
          {{ skipped.length }} 部不搬：
          <ul class="skips">
            <li v-for="i in skipped" :key="i.key">
              {{ label(i) }}（{{ i.lib_name }}：{{ i.old_cat }}）—— {{ i.reason }}
            </li>
          </ul>
        </HAlert>
      </template>
    </div>

    <template #footer>
      <div class="foot-btns">
        <HButton variant="tertiary" @click="emit('update:show', false)">取消</HButton>
        <HButton variant="primary" :disabled="!picked.size || loading" :loading="submitting" @click="submit">
          搬移 {{ picked.size || '' }} 部
        </HButton>
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
.head {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
}
.list {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin: 0;
  padding: 0;
  list-style: none;
  max-height: 46vh;
  overflow: auto;
}
.item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 12px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.item-main {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
  flex: 1;
}
.item-title,
.item-path {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.item-title {
  font-size: 13px;
}
.item-path {
  font-size: 12px;
  color: var(--muted);
}
.skips {
  margin: 6px 0 0;
  padding-left: 18px;
  font-size: 12px;
  max-height: 20vh;
  overflow: auto;
}
.foot-btns {
  display: flex;
  gap: 8px;
  margin-left: auto;
}
</style>
