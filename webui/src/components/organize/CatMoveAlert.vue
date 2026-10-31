<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import { libraryApi } from '@/api'
import type { CatMoveGroup } from '@/api/library'
import CatMoveDialog from './CatMoveDialog.vue'

/**
 * 「网盘上的分类目录和规则不一致」提示（后端 catmove.go 的 /category/move/status，零 115 / TMDB 请求）：
 * 改过分类方案、已整理的片还在旧分类目录里时显示，点了打开预演。没有就什么都不显示。
 * 二级分类、媒体库、海报墙三处都挂；保存方案后由父组件调 reload / open
 */
const props = defineProps<{ schemeId?: number }>()

const count = ref(0)
const groups = ref<CatMoveGroup[]>([])
const show = ref(false)

async function reload() {
  try {
    const d = await libraryApi.catMoveStatus(props.schemeId)
    count.value = d.count
    groups.value = d.groups ?? []
  } catch {
    count.value = 0 // 只是提示，读不出就不提示
  }
}
function open() {
  show.value = true
}
defineExpose({ reload, open })

onMounted(reload)
watch(() => props.schemeId, reload)

const summary = computed(() =>
  groups.value
    .slice(0, 4)
    .map((g) => `${g.lib_name}「${g.old_cat}」${g.titles} 部${g.media_type === 'tv' ? '剧集' : '电影'}`)
    .join('、') + (groups.value.length > 4 ? ' 等' : ''),
)
</script>

<template>
  <HAlert v-if="count > 0" status="warning" class="cat-move-alert">
    <div class="row">
      <span>
        网盘上的分类目录和规则不一致：{{ count }} 部片还在改方案之前的分类目录里（{{ summary }}）。
        本地与 Emby 按新方案走，不搬的话同一类片会分在两处。
      </span>
      <HButton size="sm" variant="secondary" @click="open">按新方案搬移</HButton>
    </div>
  </HAlert>
  <CatMoveDialog v-model:show="show" :scheme-id="schemeId" @done="reload" />
</template>

<style scoped>
.row {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.row span {
  flex: 1;
  min-width: 220px;
}
</style>
