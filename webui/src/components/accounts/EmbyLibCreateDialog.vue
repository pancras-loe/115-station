<script setup lang="ts">
/**
 * 在 Emby 里批量建媒体库（后端 embylibcreate.go）。候选由后端按媒体库表与分类方案算：
 * 选了分类方案的库一个分类一个 Emby 库（几个库同一个分类合成一个、带几个路径），不分类的库整个一个（或按本地子目录）。
 * Emby 里已经覆盖的跳过，同名库给它加路径。名字 / 类型改了点「重新检查」再看一遍；执行时后端照这些改动现算，不拿这里的结果。
 */
import { computed, ref, watch } from 'vue'
import { CircleCheck, RefreshCw, TriangleAlert } from '@lucide/vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HCheckbox from '@/components/hero/HCheckbox.vue'
import HChip from '@/components/hero/HChip.vue'
import HInput from '@/components/hero/HInput.vue'
import HModal from '@/components/hero/HModal.vue'
import HSelect from '@/components/hero/HSelect.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import { libraryApi } from '@/api'
import type { EmbyLibCand, EmbyLibOverride, EmbyLibPlan, EmbyLibResult, EmbyLibType } from '@/api/library'
import { toastError, useFeedback } from '@/composables/useFeedback'

const show = defineModel<boolean>('show', { default: false })
const emit = defineEmits<{ done: [] }>()
const { message, dialog } = useFeedback()

const plan = ref<EmbyLibPlan | null>(null)
const loading = ref(false)
const running = ref(false)
const results = ref<EmbyLibResult[] | null>(null)
/** 用户改的名字 / 类型，按 key */
const edits = ref<Record<string, { name: string; type: EmbyLibType }>>({})
const picked = ref<Set<string>>(new Set())
const realtime = ref(false)
const refresh = ref(true)
const saveNfo = ref(false)

const TYPE_OPTS: { label: string; value: EmbyLibType }[] = [
  { label: '电影', value: 'movies' },
  { label: '剧集', value: 'tvshows' },
  { label: '混合内容', value: 'mixed' },
]
const TYPE_TEXT: Record<string, string> = { movies: '电影', tvshows: '剧集', mixed: '混合内容' }
const STATE_TEXT: Record<string, string> = { new: '加进 Emby', covered: '已在 Emby', conflict: '冲突', unmapped: '换算不出' }

function overrides(): EmbyLibOverride[] {
  return Object.entries(edits.value).map(([key, e]) => ({ key, name: e.name, type: e.type }))
}

async function load(keepPicks = false) {
  loading.value = true
  try {
    const p = await libraryApi.embyLibPlan(overrides())
    plan.value = p
    const next = new Set<string>()
    for (const c of p.cands) {
      if (!edits.value[c.key]) edits.value[c.key] = { name: c.name, type: c.type }
      // 重名时后端会改名：显示后端定下的名字
      else edits.value[c.key].name = c.name
      if (c.action !== 'skip' && (!keepPicks || picked.value.has(c.key))) next.add(c.key)
    }
    picked.value = next
  } catch (e) {
    toastError(e, '读取失败')
  } finally {
    loading.value = false
  }
}

watch(show, (v) => {
  if (!v) return
  plan.value = null
  results.value = null
  edits.value = {}
  realtime.value = false
  refresh.value = true
  saveNfo.value = false
  void load()
})

const cands = computed(() => plan.value?.cands ?? [])
const actionable = computed(() => cands.value.filter((c) => c.action !== 'skip'))
const skipped = computed(() => cands.value.filter((c) => c.action === 'skip'))
const anyOff = computed(() => actionable.value.some((c) => c.scrape === 'off'))
const nPicked = computed(() => actionable.value.filter((c) => picked.value.has(c.key)).length)
const allPicked = computed(() => !!actionable.value.length && nPicked.value === actionable.value.length)
const dirty = computed(() =>
  cands.value.some((c) => {
    const e = edits.value[c.key]
    return !!e && (e.name.trim() !== c.name || e.type !== c.type)
  }),
)

function toggle(key: string, v: boolean) {
  const next = new Set(picked.value)
  if (v) next.add(key)
  else next.delete(key)
  picked.value = next
}
function toggleAll(v: boolean) {
  picked.value = v ? new Set(actionable.value.map((c) => c.key)) : new Set()
}

function actionText(c: EmbyLibCand) {
  if (c.action === 'add') return `加进「${c.target}」`
  if (c.action === 'create') return '新建'
  return '不动'
}

async function submit() {
  if (!nPicked.value) return
  if (dirty.value) {
    message.warning('改过名字或类型：先点「重新检查」看一遍')
    return
  }
  const ok = await dialog.confirm({
    title: `在 Emby 里处理 ${nPicked.value} 个媒体库`,
    content: refresh.value ? '建完会让 Emby 扫描一次媒体库，库大的话要一阵子。' : '建完不扫描，之后在 Emby 里自己扫。',
    actions: [
      { label: '取消', value: false, variant: 'tertiary' },
      { label: '开始', value: true, variant: 'primary' },
    ],
  })
  if (!ok) return
  running.value = true
  try {
    const r = await libraryApi.embyLibCreate({
      overrides: overrides(),
      keys: [...picked.value],
      refresh: refresh.value,
      realtime: realtime.value,
      save_nfo: saveNfo.value,
    })
    results.value = r.results
    if (r.failed) message.warning(`${r.ok} 个做成了，${r.failed} 个没做成`)
    else message.success(`${r.ok} 个已加进 Emby`)
    emit('done')
  } catch (e) {
    toastError(e, '建库失败')
  } finally {
    running.value = false
  }
}
</script>

<template>
  <HModal v-model:show="show" title="在 Emby 里建媒体库" width="920px" persistent>
    <template v-if="results">
      <ul class="results">
        <li v-for="r in results" :key="r.key" :class="{ bad: !r.ok }">
          <p class="r-head">
            <CircleCheck v-if="r.ok" :size="14" class="ok" /><TriangleAlert v-else :size="14" class="bad-ico" />
            <b>{{ r.name }}</b>
            <span class="muted">{{ r.ok ? (r.action === 'add' ? '已加路径' : '已建好') : r.error }}</span>
          </p>
          <p v-if="r.paths.length" class="muted small"><code v-for="p in r.paths" :key="p">{{ p }}</code></p>
          <ul v-if="r.warns?.length" class="warns">
            <li v-for="w in r.warns" :key="w">{{ w }}</li>
          </ul>
        </li>
      </ul>
    </template>

    <template v-else>
      <p v-if="loading && !plan" class="muted">正在对照 Emby 现有的媒体库…</p>
      <template v-else-if="plan">
        <HAlert v-if="plan.error" status="danger">{{ plan.error }}</HAlert>
        <template v-else>
          <p class="hint">
            按本站的媒体库与分类方案算：一个分类建一个 Emby 库，几个库里同一个分类合成一个库。Emby 里已经有的跳过，同名库给它加路径。
            库选项按本站库的刮削方式设：本站刮的关掉 Emby 的元数据 / 图像下载器、Emby 不写 NFO；交给 Emby 刮的开 TheMovieDb。
            章节图与片头检测一律关（对 STRM 都要取 115 直链）。
          </p>
          <HAlert v-for="w in plan.warnings" :key="w" status="warning" class="gap">{{ w }}</HAlert>

          <div v-if="!actionable.length" class="empty">
            <CircleCheck :size="16" class="ok" />没有要建的：本站的目录都已经在 Emby 里了
          </div>
          <div v-else class="table" role="table" aria-label="要建的 Emby 媒体库">
            <div class="tr th" role="row">
              <span class="c-check">
                <HCheckbox :checked="allPicked" :indeterminate="!!nPicked && !allPicked" aria-label="全选" @update:checked="toggleAll" />
              </span>
              <span>Emby 库名</span>
              <span>类型</span>
              <span>路径</span>
            </div>
            <div v-for="c in actionable" :key="c.key" class="tr" :class="{ 'is-off': !picked.has(c.key) }" role="row">
              <span class="c-check">
                <HCheckbox :checked="picked.has(c.key)" :aria-label="`选择 ${c.name}`" @update:checked="(v: boolean) => toggle(c.key, v)" />
              </span>
              <div class="c-name">
                <HInput
                  v-if="c.action === 'create' && edits[c.key]"
                  v-model="edits[c.key].name"
                  :input-attrs="{ 'aria-label': `${c.name} 的库名` }"
                />
                <span v-else class="target">{{ c.target }}</span>
                <span class="tags">
                  <HChip :color="c.action === 'create' ? 'accent' : 'default'">{{ actionText(c) }}</HChip>
                  <HChip>{{ c.scrape === 'off' ? 'Emby 刮' : '本站刮' }}</HChip>
                </span>
              </div>
              <div class="c-type">
                <HSelect
                  v-if="!c.type_fixed && c.action === 'create' && edits[c.key]"
                  v-model="edits[c.key].type"
                  :options="TYPE_OPTS"
                  :aria-label="`${c.name} 的类型`"
                />
                <span v-else>{{ TYPE_TEXT[c.type] }}</span>
              </div>
              <ul class="c-paths">
                <li v-for="p in c.paths" :key="p.local" :class="`st-${p.state}`" :title="p.note || p.local">
                  <code>{{ p.emby || p.local }}</code>
                  <small>{{ c.libs.length > 1 ? `${p.lib} · ` : '' }}{{ p.note || STATE_TEXT[p.state] }}</small>
                </li>
                <li v-if="c.note" class="note">{{ c.note }}</li>
              </ul>
            </div>
          </div>

          <details v-if="skipped.length" class="skipped">
            <summary>不动的 {{ skipped.length }} 个</summary>
            <ul>
              <li v-for="c in skipped" :key="c.key">
                <b>{{ c.name }}</b><span class="muted">：{{ c.why }}</span>
                <span v-for="p in c.paths.filter((p) => p.state !== 'covered')" :key="p.local" class="muted small"> · {{ p.note }}</span>
              </li>
            </ul>
          </details>

          <div v-if="actionable.length" class="opts">
            <FieldRow label="实时监控" tip="本站入库后会自己通知 Emby 刷新，开着等于 Emby 再扫一遍。默认关。">
              <HSwitch v-model="realtime" aria-label="实时监控" />
            </FieldRow>
            <FieldRow v-if="anyOff" label="NFO 存进媒体文件夹" tip="只对交给 Emby 刮的库：Emby 把刮来的元数据与图片写进 STRM 所在目录。默认关（存在 Emby 自己的元数据目录里）。">
              <HSwitch v-model="saveNfo" aria-label="NFO 存进媒体文件夹" />
            </FieldRow>
            <FieldRow label="建完扫描" tip="全部建完让 Emby 扫描一次媒体库。">
              <HSwitch v-model="refresh" aria-label="建完扫描" />
            </FieldRow>
          </div>
        </template>
      </template>
    </template>

    <template #footer>
      <template v-if="results">
        <HButton variant="primary" @click="show = false">关闭</HButton>
      </template>
      <template v-else>
        <HButton variant="tertiary" @click="show = false">取消</HButton>
        <HButton v-if="plan && !plan.error" variant="secondary" :loading="loading" @click="load(true)"><RefreshCw :size="14" />重新检查</HButton>
        <HButton variant="primary" :loading="running" :disabled="!nPicked || loading" @click="submit">
          {{ nPicked ? `处理 ${nPicked} 个` : '开始' }}
        </HButton>
      </template>
    </template>
  </HModal>
</template>

<style scoped>
.hint {
  margin: 0 0 10px;
  font-size: 12.5px;
  line-height: 1.6;
  color: var(--muted);
}
.gap {
  margin-bottom: 8px;
}
.empty {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 16px 0;
  font-size: 13.5px;
}
.table {
  display: flex;
  flex-direction: column;
  gap: 4px;
  max-height: 48vh;
  overflow-y: auto;
}
.tr {
  display: grid;
  grid-template-columns: 28px minmax(0, 1.1fr) 120px minmax(0, 1.8fr);
  align-items: start;
  gap: 10px;
  padding: 6px;
  border-radius: var(--r-sm);
}
.th {
  position: sticky;
  top: 0;
  z-index: 1;
  align-items: center;
  background: var(--overlay, var(--surface));
  font-size: 12px;
  color: var(--muted);
}
.tr:not(.th):hover {
  background: var(--surface-secondary, var(--default));
}
.is-off {
  opacity: 0.6;
}
.c-check {
  display: flex;
  justify-content: center;
  padding-top: 6px;
}
.c-name {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}
.target {
  font-weight: 600;
  font-size: 13.5px;
  padding-top: 5px;
}
.tags {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}
.c-type {
  font-size: 13px;
  padding-top: 5px;
}
.c-type :deep(.select) {
  margin-top: -5px;
}
.c-paths {
  display: flex;
  flex-direction: column;
  gap: 3px;
  margin: 0;
  padding: 4px 0 0;
  list-style: none;
  min-width: 0;
}
.c-paths li {
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.c-paths code {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: var(--font-mono);
  font-size: 12.5px;
}
.c-paths small {
  font-size: 11.5px;
  color: var(--muted);
}
.c-paths .st-covered small {
  color: var(--success);
}
.c-paths .st-conflict small,
.c-paths .st-unmapped small,
.note {
  color: var(--warning);
  font-size: 11.5px;
}
.skipped {
  margin-top: 10px;
  font-size: 12.5px;
}
.skipped summary {
  cursor: pointer;
  color: var(--muted);
}
.skipped ul {
  margin: 6px 0 0;
  padding-left: 18px;
  line-height: 1.7;
}
.opts {
  margin-top: 12px;
  padding-top: 8px;
  border-top: 1px solid var(--separator);
}
.results {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.r-head {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 0;
  font-size: 13.5px;
}
.warns {
  margin: 4px 0 0;
  padding-left: 22px;
  font-size: 12px;
  color: var(--warning);
}
.ok {
  color: var(--success);
}
.bad-ico {
  color: var(--danger);
}
.muted {
  color: var(--muted);
}
.small {
  font-size: 12px;
  margin: 2px 0 0 20px;
}
.small code {
  margin-right: 8px;
  font-family: var(--font-mono);
}

@media (max-width: 720px) {
  .th {
    display: none;
  }
  .tr {
    grid-template-columns: 28px minmax(0, 1fr);
  }
  .tr > :nth-child(n + 3) {
    grid-column: 2;
  }
}
</style>
