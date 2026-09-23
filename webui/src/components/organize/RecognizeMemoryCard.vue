<script setup lang="ts">
/**
 * 识别记忆：人工在「待确认」里改指定、或重新整理选了别的条目时记下的
 * 「片名 + 年份 → TMDB 条目」。同名内容下次直接采用，所以记错了要能在这里删掉。
 * 后端量级几十到几百条，一次全拉，筛选在本地做。
 */
import { computed, onMounted, ref } from 'vue'
import { NButton, NInput, NPopconfirm, NTag, NTooltip } from 'naive-ui'
import { RefreshCw, Search, Trash2 } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import { organizeApi } from '@/api'
import type { RecognizeMemory } from '@/api/organize'
import { toastError, useFeedback } from '@/composables/useFeedback'

const { message } = useFeedback()
const rows = ref<RecognizeMemory[]>([])
const loading = ref(false)
const keyword = ref('')

async function reload() {
  loading.value = true
  try {
    rows.value = (await organizeApi.listRecognizeMemory()).data ?? []
  } catch (e) {
    toastError(e, '读取识别记忆失败')
  } finally {
    loading.value = false
  }
}
onMounted(reload)

/** 按原名、识别名、TMDB 片名、条目 id 任一命中 */
const shown = computed(() => {
  const k = keyword.value.trim().toLowerCase()
  if (!k) return rows.value
  return rows.value.filter((r) =>
    [r.sample, r.title_key, r.title, String(r.tmdb_id)].some((v) => (v ?? '').toLowerCase().includes(k)),
  )
})

async function remove(r: RecognizeMemory) {
  try {
    await organizeApi.deleteRecognizeMemory(r.id)
    rows.value = rows.value.filter((x) => x.id !== r.id)
    message.success('已删除，同名内容下次按正常流程识别')
  } catch (e) {
    toastError(e, '删除失败')
  }
}

async function clearAll() {
  try {
    const res = await organizeApi.clearRecognizeMemory()
    message.success(res.message ?? '已清空')
    rows.value = []
  } catch (e) {
    toastError(e, '清空失败')
  }
}

function fullTime(s: string) {
  const d = new Date(s)
  return Number.isNaN(d.getTime()) ? s : d.toLocaleString('zh-CN', { hour12: false })
}
function shortDate(s: string) {
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return s
  const md = `${d.getMonth() + 1}-${String(d.getDate()).padStart(2, '0')}`
  return d.getFullYear() === new Date().getFullYear() ? md : `${d.getFullYear()}-${md}`
}
</script>

<template>
  <SectionCard
    title="识别记忆"
    hint="在待确认里改过指定、或重新整理选了别的条目时自动记下；同名同年的内容以后直接采用"
  >
    <template #extra>
      <NTooltip>
        <template #trigger>
          <NButton size="small" quaternary :loading="loading" @click="reload">
            <template #icon><RefreshCw :size="14" /></template>
          </NButton>
        </template>
        刷新
      </NTooltip>
      <NPopconfirm @positive-click="void clearAll()">
        <template #trigger>
          <NButton size="small" quaternary type="error" :disabled="!rows.length">
            <template #icon><Trash2 :size="14" /></template>
            清空
          </NButton>
        </template>
        清空全部 {{ rows.length }} 条识别记忆？之后同名内容都按正常流程重新识别。
      </NPopconfirm>
    </template>

    <div v-if="rows.length > 8" class="toolbar">
      <NInput v-model:value="keyword" size="small" clearable placeholder="按原名、片名或 TMDB id 筛选">
        <template #prefix><Search :size="14" /></template>
      </NInput>
    </div>

    <EmptyState
      v-if="!rows.length && !loading"
      text="还没有识别记忆。在整理记录里改指定或重新整理选了别的条目后，会出现在这里。"
    />
    <EmptyState v-else-if="rows.length && !shown.length" text="没有匹配的记忆。" />

    <ul v-else class="list">
      <li v-for="r in shown" :key="r.id" class="item">
        <div class="main">
          <div class="source" :title="r.sample || r.title_key">{{ r.sample || r.title_key }}</div>
          <div class="meta">
            识别名 <code>{{ r.title_key }}</code>
            <template v-if="r.year"> · {{ r.year }}</template>
            <template v-else> · 不限年份</template>
          </div>
        </div>
        <div class="target">
          <span class="arrow">→</span>
          <NTag size="small" :bordered="false" :type="r.media_type === 'tv' ? 'info' : 'default'">
            {{ r.media_type === 'tv' ? '剧集' : '电影' }}
          </NTag>
          <a
            class="title"
            :href="`https://www.themoviedb.org/${r.media_type}/${r.tmdb_id}`"
            target="_blank"
            rel="noopener noreferrer"
            :title="`在 TMDB 查看 ${r.media_type}/${r.tmdb_id}`"
          >
            {{ r.title || `tmdb ${r.tmdb_id}` }}
          </a>
        </div>
        <div class="stats">
          <span :title="`已按这条记忆识别 ${r.hits} 次`">命中 {{ r.hits }}</span>
          <span :title="fullTime(r.updated_at)">{{ shortDate(r.updated_at) }}</span>
        </div>
        <NPopconfirm @positive-click="void remove(r)">
          <template #trigger>
            <NButton size="tiny" quaternary title="删除这条记忆">
              <Trash2 :size="13" />
            </NButton>
          </template>
          删除后，同名内容下次按正常流程识别。
        </NPopconfirm>
      </li>
    </ul>
    <p v-if="rows.length" class="note">
      记错了也可以不删：在整理记录里对那条内容「重新整理」选对的条目，会直接覆盖这条记忆。
    </p>
  </SectionCard>
</template>

<style scoped>
.toolbar {
  margin-bottom: 10px;
  max-width: 320px;
}
.list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
}
.item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 9px 4px;
  border-bottom: 1px solid var(--c-border);
}
.item:last-child {
  border-bottom: none;
}
.main {
  flex: 1;
  min-width: 0;
}
.source {
  font-size: 13px;
  color: var(--c-text-1);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.meta {
  margin-top: 2px;
  font-size: 11.5px;
  color: var(--c-text-3);
}
.meta code {
  font-family: var(--font-mono);
  color: var(--c-text-2);
}
.target {
  display: flex;
  align-items: center;
  gap: 6px;
  flex: 0 1 38%;
  min-width: 0;
}
.arrow {
  color: var(--c-text-4);
}
.title {
  font-size: 13px;
  color: var(--c-primary);
  text-decoration: none;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.title:hover {
  text-decoration: underline;
}
.stats {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 2px;
  font-size: 11.5px;
  color: var(--c-text-3);
  white-space: nowrap;
}
.note {
  margin: 10px 0 0;
  font-size: 12px;
  color: var(--c-text-3);
}
@media (max-width: 640px) {
  .item {
    flex-wrap: wrap;
  }
  .main {
    flex-basis: 100%;
  }
  .target {
    flex: 1;
  }
}
</style>
