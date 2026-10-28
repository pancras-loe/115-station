<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { ChevronRight, RefreshCw } from '@lucide/vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HPagination from '@/components/hero/HPagination.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import MediaTypeChip from '@/components/MediaTypeChip.vue'
import PosterImage from '@/components/PosterImage.vue'
import TabSummary, { type SummaryItem } from '@/components/strm/TabSummary.vue'
import { syncApi } from '@/api'
import type { DeepDeletePage, DeepDeleteRecord, DeepDeleteStatus, EmbyWebhookStatus } from '@/api/sync'
import type { DeepDelSetting } from './deepDelSetting'
import { toastError } from '@/composables/useFeedback'
import { fullTime, relTime } from '@/utils/time'
import { num } from '@/utils/format'
import { useMediaServer } from '@/composables/mediaServer'

/**
 * 深度删除页签（2026-10-23 按「状态 → 记录 → 设置」重排，参考 p115strmhelper 的同步删除历史）：
 * 顶部近 30 天概况；删除记录占主栏，能按状态筛、分页，每条带类型、季集、海报，点开看删了哪些路径；
 * 右栏是三个开关与可展开的规则说明。
 * 配置由 SyncPage 持有一份（顶部状态总览也读它），见 deepDelSetting.ts
 */
const props = defineProps<{ setting: DeepDelSetting }>()
const cfg = computed(() => props.setting.model.value)

const filter = ref<DeepDeleteStatus | 'all'>('all')
const page = ref(1)
const size = ref(20)
const resp = ref<DeepDeletePage | null>(null)
const loading = ref(false)
async function load() {
  loading.value = true
  try {
    resp.value = await syncApi.deepDeleteRecords(page.value, size.value, filter.value === 'all' ? undefined : filter.value)
  } catch (e) {
    toastError(e, '删除记录读取失败')
  } finally {
    loading.value = false
  }
}
// ---- Emby Webhook 收到过什么：Emby 查不到它的 Webhook 配置，只能看本站收到过什么（embywebhookseen.go）----
const hook = ref<EmbyWebhookStatus | null>(null)
const ms = useMediaServer()
async function loadHook() {
  try {
    hook.value = await syncApi.embyWebhookStatus()
  } catch {
    hook.value = null
  }
}
onMounted(() => {
  void load()
  void loadHook()
})

const at = (s: string) => (s ? new Date(s).getTime() : 0)
/** 从上往下取第一条成立的；tone=warn 的才算没配好 */
const hookState = computed<{ tone: 'ok' | 'warn' | 'info'; title: string; text: string } | null>(() => {
  const h = hook.value
  if (!h) return null
  if (!h.token_set) {
    return { tone: 'warn', title: '还没生成 Webhook 接收地址', text: '打开「Emby 设置」会自动生成，把接收地址填到 Emby 的 Webhooks 里。' }
  }
  if (h.reject_at && at(h.reject_at) >= at(h.any)) {
    return { tone: 'warn', title: `Emby 推过来了，但被拒收（${relTime(h.reject_at)}）`, text: `${h.reject_why}。到「Emby 设置」复制最新的接收地址，更新到 Emby 里。` }
  }
  if (!h.any) {
    return {
      tone: 'warn',
      title: '还没收到过 Emby 推来的事件',
      text: 'Emby 里还没添加这个 Webhook：在 Emby「设置 → 通知」添加 Webhooks，填入接收地址、内容类型选 application/json，并勾上删除事件。',
    }
  }
  if (!h.delete) {
    return {
      tone: 'info',
      title: `收到过 Emby 的事件（${relTime(h.any)}），还没收到过删除事件`,
      text: h.test
        ? `Emby 的测试事件 ${relTime(h.test)} 收到过。还没在 Emby 里删过东西的话可以不管；删过的话，检查 Webhook 有没有勾上删除事件。`
        : '还没在 Emby 里删过东西的话可以不管；删过的话，检查 Emby 的 Webhook 有没有勾上删除事件。',
    }
  }
  return { tone: 'ok', title: `Webhook 正常，上次收到删除事件：${relTime(h.delete)}`, text: '' }
})
watch([filter, size], () => {
  if (page.value !== 1) page.value = 1
  else void load()
})
// 翻页后回到列表顶上（列表在卡片里自己滚）
const listBox = ref<HTMLElement | null>(null)
watch(page, async () => {
  await load()
  if (listBox.value) listBox.value.scrollTop = 0
})

const records = computed(() => resp.value?.data ?? [])

// 历史预演记录仅作回显，新版本不再提供预演操作。
const STATUS: Record<string, { text: string; color: 'success' | 'warning' | 'danger' | 'default' }> = {
  done: { text: '已删', color: 'success' },
  dry_run: { text: '历史预演', color: 'default' },
  rejected: { text: '拦下', color: 'warning' },
  failed: { text: '失败', color: 'danger' },
}
const reasonLabel: Record<string, string> = { local_scan: '历史扫描', emby_webhook: 'Emby 事件', manual_record: '整理记录', manual: '历史手动执行' }

const FILTERS = computed(() => {
  const c = resp.value?.counts ?? {}
  return (
    [
      ['all', '全部'],
      ['done', '已删'],
      ['rejected', '拦下'],
      ['failed', '失败'],
    ] as const
  ).map(([value, label]) => ({ value, label: `${label} ${num(c[value] ?? 0)}` }))
})

const summary = computed<SummaryItem[]>(() => {
  const on = !!props.setting.saved.value.enabled
  const s = resp.value?.stats30
  const items: SummaryItem[] = [
    {
      key: 'on',
      label: '事件联动',
      value: on ? '已开启' : '未开启',
      tone: on ? (hookState.value?.tone === 'warn' ? 'warn' : 'ok') : 'off',
      sub: on
        ? hookState.value?.tone === 'warn'
          ? hookState.value.title
          : 'Emby 里删片后联动删网盘源文件'
        : '整理记录上的深度删除不受这个开关影响',
    },
  ]
  if (!s) return items
  items.push(
    {
      key: 'done',
      label: '近 30 天删除',
      value: `${num(s.done)} 次`,
      sub: s.done ? `网盘视频 ${num(s.videos)} 个${s.pan_dirs ? ` · 空目录 ${num(s.pan_dirs)} 个` : ''}` : '没有删过',
    },
    {
      key: 'rejected',
      label: '拦下',
      value: `${num(s.rejected)} 次`,
      tone: s.rejected ? 'warn' : undefined,
      sub: s.rejected ? '本地文件还在、越界等，没有删网盘' : '没有拦下过',
    },
    {
      key: 'failed',
      label: '失败',
      value: `${num(s.failed)} 次`,
      tone: s.failed ? 'danger' : undefined,
      sub: s.failed ? '原因写在记录里' : '没有失败',
    },
  )
  return items
})

// 每条「删了哪些」展开状态
const openPaths = ref<Set<number>>(new Set())
function togglePaths(id: number) {
  const s = new Set(openPaths.value)
  if (s.has(id)) s.delete(id)
  else s.add(id)
  openPaths.value = s
}
function posterOf(r: DeepDeleteRecord) {
  return r.poster ? `/api/local/poster?${r.poster}&w=120` : null
}

const rulesOpen = ref(false)
/**
 * 原来是一整段密密麻麻的提示框，关键的「什么时候会删、什么时候不删」被淹在里面。
 * 拆成条目：哪些会触发、哪些不会、删之前核验什么 —— 行为本身没变（见 AGENTS.md §6.10）
 */
const RULES = {
  trigger: [
    '需要先在 Emby 配好 webhook，接收原生 library.deleted 与神医助手 deep.delete',
    '只处理电影、单集、季、剧集事件，且只动本次事件对应、本地已消失的文件',
    '删除进入 115 回收站，可以还原',
  ],
  skip: ['移除媒体库、普通文件夹事件不会触发', '直接在磁盘上删 STRM 不会触发（不再定时扫描）'],
  guard: [
    '执行前实时查询 Emby 当前的媒体库，确认本地文件确实缺失',
    'Emby 查询失败、媒体库已移除、目录不可访问时一律拦下',
    '剧 / 季目录缺少台账布局证据时拦下',
    '同步、整理任务运行时会等它结束再执行',
  ],
}
</script>

<template>
  <div class="tab-body">
    <TabSummary :items="summary" />

    <div class="grid">
      <SectionCard title="删除记录" hint="刷新只读取记录，不触发扫描或删除">
        <template #extra>
          <div class="head-btns">
            <HSegmented v-model="filter" :options="FILTERS" size="sm" aria-label="状态" />
            <HButton size="sm" variant="ghost" :loading="loading" aria-label="刷新" @click="load">
              <template #icon><RefreshCw :size="14" /></template>
            </HButton>
          </div>
        </template>

        <EmptyState v-if="resp && !records.length" :text="filter === 'all' ? '暂无删除记录' : '没有这类记录'" />
        <ul v-else ref="listBox" class="records">
          <li v-for="r in records" :key="r.id" class="record">
            <PosterImage class="poster" :src="posterOf(r)" :alt="r.title" :icon-size="16" />
            <div class="record-main">
              <div class="record-head">
                <b class="record-title" :title="r.title">{{ r.title || '事件处理' }}</b>
                <MediaTypeChip v-if="r.media_type" :type="r.media_type" />
                <span v-if="r.episodes" class="record-ep">{{ r.episodes }}</span>
              </div>
              <div class="record-meta">
                <span>{{ reasonLabel[r.reason] ?? r.reason }}</span>
                <template v-if="r.video_cnt || r.asset_cnt">
                  <span>视频 <b>{{ r.video_cnt }}</b></span>
                  <span>附属 <b>{{ r.asset_cnt }}</b></span>
                </template>
                <span v-if="r.pan_dirs">空目录 <b>{{ r.pan_dirs }}</b></span>
              </div>
              <div v-if="r.message" class="record-msg" :class="`is-${r.status}`">{{ r.message }}</div>
              <div v-if="openPaths.has(r.id)" class="record-paths">
                <div v-for="p in r.rel_paths" :key="p">{{ p }}</div>
              </div>
            </div>
            <div class="record-side">
              <HChip :color="STATUS[r.status]?.color ?? 'default'">{{ STATUS[r.status]?.text ?? r.status }}</HChip>
              <span :title="fullTime(r.created_at)">{{ relTime(r.created_at) }}</span>
              <button v-if="r.rel_paths?.length" type="button" class="link" @click="togglePaths(r.id)">
                {{ openPaths.has(r.id) ? '收起' : `删了哪些（${r.rel_paths.length}）` }}
              </button>
            </div>
          </li>
        </ul>
        <HPagination
          v-if="(resp?.total ?? 0) > size"
          v-model:page="page"
          v-model:page-size="size"
          :total="resp?.total ?? 0"
          class="pager"
        />
      </SectionCard>

      <div class="side">
        <SectionCard title="事件联动" hint="Emby 里删片后，联动删除对应的网盘源文件">
          <FieldRow label="启用事件联动" tip="开启后接收原生 library.deleted 和神医助手 deep.delete。整理记录的深度删除按钮独立可用，不受这个开关影响。">
            <HSwitch v-model="cfg.enabled" :disabled="!!ms.blocked('deep_delete') && !cfg.enabled" aria-label="启用事件联动" />
          </FieldRow>
          <HAlert v-if="ms.blocked('deep_delete')" status="warning" class="warn">
            {{ ms.name.value }} 下不可用：{{ ms.blocked('deep_delete') }}。开关开着也不会删任何东西；整理记录里的深度删除照常可用。
          </HAlert>
          <FieldRow label="清理网盘空目录" tip="影片/季目录空了就跟着删，再往上只删完全为空的目录，不会进入其他影片。也适用于整理记录删除。">
            <HSwitch v-model="cfg.prune_pan_dirs" aria-label="清理网盘空目录" />
          </FieldRow>
          <FieldRow label="删除后发通知" tip="一次删除只推一条消息，写明删了几个网盘文件或被拦下的原因。也适用于整理记录删除。">
            <HSwitch v-model="cfg.notify" aria-label="删除后发通知" />
          </FieldRow>

          <!-- 联动靠 Emby 推删除事件：没配 Webhook 时开关开着也什么都不会发生，所以开着时把收到过什么说清楚 -->
          <HAlert
            v-if="cfg.enabled && hookState && hookState.tone !== 'ok'"
            :status="hookState.tone === 'warn' ? 'warning' : 'default'"
            :title="hookState.title"
            class="warn"
          >
            {{ hookState.text }}
            <div class="hook-act">
              <RouterLink class="jump" :to="{ name: 'settings', query: { tab: 'emby' } }">去 Emby 设置复制接收地址 →</RouterLink>
              <HButton size="sm" variant="ghost" @click="loadHook">重新检查</HButton>
            </div>
          </HAlert>
          <p v-else-if="cfg.enabled && hookState" class="hook-ok">{{ hookState.title }}</p>

          <HAlert v-if="cfg.enabled" status="warning" class="warn">
            原生删除事件也可能来自 Emby 扫库清理，单凭事件分不清是主动删除还是文件丢失。
            请确认「系统配置 → Emby」里的地址与 API 密钥有效。
          </HAlert>

          <FormActions>
            <HButton
              variant="primary"
              :loading="setting.saving.value"
              :disabled="!setting.dirty.value && !setting.saving.value"
              @click="setting.save()"
            >
              保存配置
            </HButton>
          </FormActions>
        </SectionCard>

        <section class="card card--default rules-card">
          <button type="button" class="rules-head" :aria-expanded="rulesOpen" @click="rulesOpen = !rulesOpen">
            <ChevronRight :size="16" class="chev" :class="{ open: rulesOpen }" />
            <span class="rules-head-title">什么时候会删</span>
            <span class="dim">会删 / 不会删 / 删之前核验</span>
          </button>
          <div v-if="rulesOpen" class="rules">
            <div class="rules-col">
              <div class="rules-title">会删</div>
              <ul><li v-for="r in RULES.trigger" :key="r">{{ r }}</li></ul>
            </div>
            <div class="rules-col">
              <div class="rules-title">不会删</div>
              <ul><li v-for="r in RULES.skip" :key="r">{{ r }}</li></ul>
            </div>
            <div class="rules-col">
              <div class="rules-title">删之前核验</div>
              <ul><li v-for="r in RULES.guard" :key="r">{{ r }}</li></ul>
            </div>
          </div>
        </section>
      </div>
    </div>
  </div>
</template>

<style scoped>
.tab-body { display: flex; flex-direction: column; gap: 16px; }
.grid {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 360px;
  gap: 16px;
  align-items: start;
}
.side { display: flex; flex-direction: column; gap: 16px; min-width: 0; }
.head-btns { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.warn { margin: 6px 0 4px; }
.hook-act { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 12px; margin-top: 6px; }
.hook-ok { margin: 6px 0 0; font-size: 12.5px; color: var(--success); }
.jump { font-size: 13px; font-weight: 500; color: var(--accent); text-decoration: none; }
.jump:hover { text-decoration: underline; }
.dim { color: var(--muted); font-size: 12px; }

/* ---- 删除记录 ---- */
/* 每条带海报约 90px 高，20 条就是一整屏多：限高后在卡片里自己滚 */
.records {
  list-style: none;
  margin: 0;
  padding: 0 6px 0 0;
  max-height: min(640px, 65vh);
  overflow-y: auto;
  overscroll-behavior: contain;
}
.record {
  display: grid;
  grid-template-columns: 44px minmax(0, 1fr) auto;
  gap: 12px;
  align-items: start;
  padding: 12px 0;
  border-top: 1px solid var(--separator);
  font-size: 13px;
}
.record:first-child { border-top: 0; padding-top: 0; }
.poster {
  width: 44px;
  height: 66px;
  border-radius: 8px;
}
.record-main { min-width: 0; }
.record-head { display: flex; align-items: center; flex-wrap: wrap; gap: 6px 8px; min-width: 0; }
.record-title {
  min-width: 0;
  font-weight: 600;
  color: var(--foreground);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.record-ep { font-family: var(--font-mono); font-size: 12px; color: var(--muted); }
.record-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 2px 12px;
  margin-top: 4px;
  font-size: 12px;
  color: var(--muted);
}
.record-meta b { color: var(--foreground); font-weight: 600; font-variant-numeric: tabular-nums; }
.record-msg {
  margin-top: 6px;
  padding: 6px 10px;
  border-radius: 8px;
  font-size: 12.5px;
  overflow-wrap: anywhere;
  background: var(--surface-secondary);
  color: color-mix(in oklab, var(--foreground) 75%, var(--muted));
}
.record-msg.is-rejected {
  background: color-mix(in oklab, var(--warning) 10%, transparent);
  color: var(--warning-soft-foreground);
}
.record-msg.is-failed {
  background: color-mix(in oklab, var(--danger) 10%, transparent);
  color: var(--danger);
}
.record-paths {
  margin-top: 6px;
  padding: 6px 10px;
  border-radius: 8px;
  background: var(--surface-secondary);
  font-family: var(--font-mono);
  font-size: 11.5px;
  line-height: 1.8;
  color: var(--muted);
  overflow-wrap: anywhere;
  max-height: 220px;
  overflow-y: auto;
}
.record-side {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 6px;
  font-size: 12px;
  color: var(--muted);
  white-space: nowrap;
}
.link {
  padding: 0;
  border: 0;
  background: none;
  font: inherit;
  font-size: 12px;
  color: var(--accent);
  cursor: pointer;
}
.link:hover { text-decoration: underline; }
.pager { margin-top: 10px; }

/* ---- 规则说明 ---- */
.rules-card { padding: 0; gap: 0; }
.rules-head {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px 10px;
  width: 100%;
  padding: 16px 20px;
  border: 0;
  background: none;
  font: inherit;
  color: inherit;
  text-align: left;
  cursor: pointer;
}
.rules-head-title { font-size: 15px; font-weight: 600; }
.chev { color: var(--muted); transition: transform 0.15s ease; }
.chev.open { transform: rotate(90deg); }
.rules {
  display: flex;
  flex-direction: column;
  margin: 0 20px 18px;
  padding: 4px 14px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.rules-col { padding: 8px 0; }
.rules-col + .rules-col { border-top: 1px solid var(--separator); }
.rules-title { font-size: 12px; font-weight: 600; color: var(--foreground); margin-bottom: 2px; }
.rules ul {
  margin: 0;
  padding-left: 16px;
  font-size: 12px;
  line-height: 1.6;
  color: var(--muted);
}
.rules li + li { margin-top: 2px; }

@media (max-width: 1280px) {
  .grid { grid-template-columns: 1fr; }
}
@media (max-width: 720px) {
  .record { grid-template-columns: 40px minmax(0, 1fr); }
  .record-side { grid-column: 2; flex-direction: row; align-items: center; }
}
</style>
