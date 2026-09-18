<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NButton, NInput, NInputGroup, NModal, NSpin } from 'naive-ui'
import { Search } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import TmdbPicker from '@/components/transfer/TmdbPicker.vue'
import ResourceRow from '@/components/transfer/ResourceRow.vue'
import { resourcesApi, transferApi } from '@/api'
import type { PansouItem } from '@/api/resources'
import { toastError, useFeedback } from '@/composables/useFeedback'

const { message } = useFeedback()
const DEFAULT_BASE = 'https://pansou.app'

const baseUrl = ref(DEFAULT_BASE)
const saving = ref(false)
const query = ref('')

const pickerShow = ref(false)
const listShow = ref(false)
const listTitle = ref('')
const items = ref<PansouItem[]>([])
const loading = ref(false)
const error = ref('')
const filter = ref('')

const TYPE_LABEL: Record<string, string> = {
  '115': '115 网盘',
  baidu: '百度网盘',
  uc: 'UC 网盘',
  xunlei: '迅雷云盘',
}

async function load() {
  try {
    baseUrl.value = (await resourcesApi.pansouConfig()).base_url || DEFAULT_BASE
  } catch {
    // 首次使用尚无配置
  }
}

async function save() {
  saving.value = true
  try {
    const d = await resourcesApi.savePansou(baseUrl.value.trim())
    baseUrl.value = d.base_url || baseUrl.value
    message.success('保存成功')
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}

async function reset() {
  baseUrl.value = DEFAULT_BASE
  await save()
}

function startSearch() {
  if (!query.value.trim()) {
    message.warning('请输入影视名称或 TMDB ID')
    return
  }
  pickerShow.value = true
}

async function searchSite(kw: string) {
  listShow.value = true
  listTitle.value = `盘搜 · ${kw}`
  loading.value = true
  error.value = ''
  items.value = []
  filter.value = ''
  try {
    items.value = (await resourcesApi.pansouSearch(kw)).data ?? []
    if (!items.value.length) error.value = `没有搜索到「${kw}」的网盘分享`
  } catch (e) {
    error.value = e instanceof Error ? e.message : '搜索失败'
  } finally {
    loading.value = false
  }
}

const types = computed(() => {
  const counts: Record<string, number> = {}
  for (const it of items.value) counts[it.cloud_type] = (counts[it.cloud_type] || 0) + 1
  return [...new Set(items.value.map((it) => it.cloud_type))].map((t) => ({
    key: t,
    label: TYPE_LABEL[t] || t,
    count: counts[t],
  }))
})

const shown = computed(() =>
  filter.value ? items.value.filter((it) => it.cloud_type === filter.value) : items.value,
)

function metaOf(it: PansouItem) {
  return [
    it.password ? `提取码 ${it.password}` : '',
    (it.datetime || '').replace('T', ' ').slice(0, 16),
  ].filter(Boolean)
}

function runOf(it: PansouItem) {
  return async () => {
    if (it.action === 'open') {
      window.open(it.url, '_blank', 'noopener')
      return ''
    }
    if (it.action === 'transfer') {
      const r = await transferApi.shareReceive({
        url: it.url,
        code: it.password || '',
        target_cid: '',
        organize: true,
      })
      return r.message || '已转存，完成后自动整理入库'
    }
    await transferApi.offlineAdd({ url: it.url, code: '', target_cid: '', organize: true })
    return '已提交离线下载，完成后自动整理入库'
  }
}

onMounted(load)
</script>

<template>
  <div class="stack">
    <SectionCard title="盘搜 PanSou" hint="聚合全网网盘分享">
      <FieldRow
        label="站点地址"
        tip="盘搜（开源项目 PanSou）聚合搜索实例地址，默认 https://pansou.app。自建实例可改为此处，保存即生效。"
      >
        <NInputGroup>
          <NInput v-model:value="baseUrl" placeholder="PanSou 实例地址" />
          <NButton type="primary" :loading="saving" @click="save">保存</NButton>
          <NButton @click="reset">重置</NButton>
        </NInputGroup>
      </FieldRow>

      <FieldRow
        label="搜索网盘资源"
        tip="先经 TMDB 匹配条目，选定后用规范标题聚合搜索全网网盘分享。115 分享点击自动转存；磁力 / ed2k 点击提交离线下载；其他网盘点击打开原链手动转存。"
      >
        <NInputGroup>
          <NInput
            v-model:value="query"
            placeholder="影视名称（中英文均可）或 TMDB ID"
            @keyup.enter="startSearch"
          />
          <NButton type="primary" @click="startSearch">
            <template #icon><Search :size="15" /></template>
            搜索
          </NButton>
        </NInputGroup>
      </FieldRow>
    </SectionCard>

    <TmdbPicker
      v-model:show="pickerShow"
      :query="query"
      skip-label="跳过 TMDB，直接用关键词搜盘搜"
      @pick="(title) => searchSite(title)"
      @skip="searchSite(query.trim())"
    />

    <NModal v-model:show="listShow" preset="card" :title="listTitle" style="width: 720px">
      <div class="list">
        <div v-if="loading" class="state">
          <NSpin size="small" /><span>多源并发搜索中，约需数秒…</span>
        </div>
        <p v-else-if="error" class="state err">{{ error }}</p>

        <template v-else>
          <div class="filters">
            <span class="filters-label">类型</span>
            <button class="pill" :class="{ on: !filter }" @click="filter = ''">
              全部 {{ items.length }}
            </button>
            <button
              v-for="t in types"
              :key="t.key"
              class="pill"
              :class="{ on: filter === t.key }"
              @click="filter = t.key"
            >
              {{ t.label }} {{ t.count }}
            </button>
          </div>

          <ResourceRow
            v-for="(it, i) in shown"
            :key="i"
            :tag="TYPE_LABEL[it.cloud_type] || it.cloud_type"
            :tag-type="it.cloud_type === '115' ? 'success' : 'default'"
            :title="it.note || it.url"
            :meta="metaOf(it)"
            :action="it.action"
            :run="runOf(it)"
          />
        </template>
      </div>
    </NModal>
  </div>
</template>

<style scoped>
.stack {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.list {
  max-height: 62vh;
  overflow-y: auto;
}
.state {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 30px 0;
  color: var(--c-text-3);
  font-size: 13px;
}
.state.err {
  color: var(--c-danger);
}

.filters {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  margin-bottom: 10px;
  font-size: 12px;
  color: var(--c-text-3);
}
.filters-label {
  margin-right: 2px;
}
.pill {
  all: unset;
  padding: 3px 12px;
  border-radius: 999px;
  cursor: pointer;
  font-size: 12.5px;
  line-height: 20px;
  background: var(--c-bg-hover);
  color: var(--c-text-2);
  transition: background-color 0.15s, color 0.15s;
}
.pill.on {
  background: var(--c-primary);
  color: #fff;
}
</style>
