<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import HDrawer from '@/components/hero/HDrawer.vue'
import HChip from '@/components/hero/HChip.vue'
import HSearchField from '@/components/hero/HSearchField.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import { shareStrmApi } from '@/api'
import type { ShareFile, ShareSource } from '@/api/sharestrm'
import { toastError } from '@/composables/useFeedback'
import { bytes } from '@/utils/format'

/** 一个分享生成了哪些本地文件：本地路径 ↔ 分享里的路径 */
const props = defineProps<{ source: ShareSource | null }>()
const show = defineModel<boolean>('show', { required: true })

const files = ref<ShareFile[]>([])
const rootAbs = ref('')
const loading = ref(false)
const kw = ref('')

watch(show, async (v) => {
  if (!v || !props.source) return
  loading.value = true
  files.value = []
  kw.value = ''
  try {
    const d = await shareStrmApi.files(props.source.id)
    files.value = d.files ?? []
    rootAbs.value = d.root_abs
  } catch (e) {
    toastError(e, '读取失败')
  } finally {
    loading.value = false
  }
})

const shown = computed(() => {
  const k = kw.value.trim().toLowerCase()
  return k ? files.value.filter((f) => (f.rel_path + ' ' + f.share_path).toLowerCase().includes(k)) : files.value
})
</script>

<template>
  <HDrawer v-model:show="show" side="right" width="620px" :title="source?.title || '文件'">
    <div class="wrap">
      <header class="head">
        <h3 class="title">{{ source?.title || source?.share_code }}</h3>
        <p class="sub">本地目录：<span class="mono">{{ rootAbs }}/{{ source?.local_dir }}</span></p>
      </header>
      <HSearchField v-model="kw" placeholder="按本地路径或分享里的路径找" />
      <EmptyState v-if="!loading && !shown.length" :text="files.length ? '没有匹配的文件' : '还没生成文件'" />
      <ul class="list">
        <li v-for="f in shown" :key="f.id" class="item">
          <div class="line">
            <HChip :color="f.kind === 'video' ? 'accent' : 'default'">{{ f.kind === 'video' ? 'STRM' : '附属' }}</HChip>
            <span class="rel mono" :title="f.rel_path">{{ f.rel_path }}</span>
            <HChip v-if="f.blocked" color="default" title="在 Emby 里删过，之后的检查不再生成">不再生成</HChip>
            <HChip v-else-if="f.gone_at" color="warning" title="分享里已经没有了，下次检查还没有就删掉本地">分享里没了</HChip>
          </div>
          <div class="from">分享里：{{ f.share_path }}<template v-if="f.size"> · {{ bytes(f.size) }}</template></div>
        </li>
      </ul>
    </div>
  </HDrawer>
</template>

<style scoped>
.wrap {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 18px;
  height: 100%;
  overflow: auto;
}
.title {
  margin: 0;
  font-size: 16px;
}
.sub {
  margin: 4px 0 0;
  font-size: 12.5px;
  color: var(--muted);
  word-break: break-all;
}
.mono {
  font-family: var(--font-mono, ui-monospace, monospace);
}
.list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.item {
  padding: 8px 0;
  border-top: 1px solid var(--border);
}
.line {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.rel {
  font-size: 12.5px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.from {
  margin-top: 3px;
  font-size: 12px;
  color: var(--muted);
  word-break: break-all;
}
</style>
