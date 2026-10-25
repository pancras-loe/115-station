<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { Plus, X } from '@lucide/vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import HSpinner from '@/components/hero/HSpinner.vue'
import { syncApi, tasksApi } from '@/api'
import type { StrmRebasePair, StrmRebaseReport } from '@/api/sync'
import type { TaskJob } from '@/api/tasks'
import { toastError } from '@/composables/useFeedback'

/**
 * 批量替换已有 STRM 的地址开头（后端 strmrebase.go）：自己填「旧地址 → 新地址」，
 * 扫本地媒体根下所有 .strm，内容以旧地址开头的换成新地址，别的项目写的也能换。
 * 只动本地文件，不请求 115，不用通知 Emby；修改时间保持原样。
 * 打开时先读一遍分组（本地 STRM 里都有哪些地址开头）给用户挑，预演看命中几个，再入队执行
 */
const props = defineProps<{
  /** 保存直连域名后自动弹出时，父组件已经读过分组，直接用它 */
  preload?: StrmRebaseReport | null
  /** 刚改了域名弹出来的：开头那句换个说法 */
  afterSave?: boolean
}>()

const loading = ref(false)
const loadErr = ref('')
const groups = ref<StrmRebaseReport | null>(null)
const pairs = ref<StrmRebasePair[]>([])
const previewing = ref(false)
const preview = ref<StrmRebaseReport | null>(null)
const starting = ref(false)
const job = ref<TaskJob | null>(null)
let timer: ReturnType<typeof setTimeout> | undefined

const FINISHED = ['success', 'partial', 'failed', 'canceled', 'interrupted']
const running = computed(() => !!job.value && !FINISHED.includes(job.value.status))
const result = computed(() => {
  const r = job.value?.result as unknown as StrmRebaseReport | undefined
  return r && typeof r.rewritten === 'number' ? r : null
})

const norm = (s: string) => s.trim().replace(/\/+$/, '')
const filled = computed(() => pairs.value.filter((p) => norm(p.from) || norm(p.to)))

/** 本站格式、又不是已保存直连域名的那些开头：多半就是要改掉的旧地址 */
function staleStation(r: StrmRebaseReport) {
  const d = norm(r.domain).toLowerCase()
  return d ? r.groups.filter((g) => g.station && g.base.toLowerCase() !== d) : []
}

function setGroups(r: StrmRebaseReport) {
  groups.value = r
  if (filled.value.length) return
  const stale = staleStation(r)
  pairs.value = stale.length ? stale.map((g) => ({ from: g.base, to: r.domain })) : [{ from: '', to: '' }]
}

async function load() {
  loading.value = true
  loadErr.value = ''
  try {
    setGroups(await syncApi.strmRebasePreview())
  } catch (e) {
    loadErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

function addPair(from = '', to = '') {
  const empty = pairs.value.findIndex((p) => !norm(p.from) && !norm(p.to))
  if (empty >= 0) pairs.value[empty] = { from, to }
  else pairs.value.push({ from, to })
}

function useGroup(g: { base: string; station?: boolean }) {
  if (pairs.value.some((p) => norm(p.from).toLowerCase() === g.base.toLowerCase())) return
  addPair(g.base, g.station ? (groups.value?.domain ?? '') : '')
}

function removePair(i: number) {
  pairs.value.splice(i, 1)
  if (!pairs.value.length) pairs.value.push({ from: '', to: '' })
}

// 改了任何一组，上次的预演就不作数了
watch(pairs, () => (preview.value = null), { deep: true })

async function runPreview() {
  previewing.value = true
  try {
    preview.value = await syncApi.strmRebasePreview(filled.value)
  } catch (e) {
    toastError(e, '预演失败')
  } finally {
    previewing.value = false
  }
}

async function start() {
  starting.value = true
  try {
    const d = await syncApi.strmRebase(filled.value)
    job.value = { id: d.job_id, status: 'queued', message: d.message } as TaskJob
    poll()
  } catch (e) {
    toastError(e, '替换没能开始')
  } finally {
    starting.value = false
  }
}

async function poll() {
  clearTimeout(timer)
  if (!job.value) return
  try {
    const { data } = await tasksApi.detail(job.value.id)
    job.value = data
  } catch {
    // 偶发网络错误：下一轮再读
  }
  if (running.value) timer = setTimeout(poll, 1500)
}

function again() {
  job.value = null
  preview.value = null
  pairs.value = [{ from: '', to: '' }]
  void load()
}

if (props.preload) setGroups(props.preload)
else void load()
onUnmounted(() => clearTimeout(timer))
</script>

<template>
  <div class="rebase">
    <p class="lead">
      <template v-if="afterSave">直连域名改了，已经生成的 STRM 里还是旧地址。</template>
      把本地 STRM 内容开头的旧地址换成新地址，后面的部分原样留着。本站和别的项目（p115strmhelper、挂载路径……）写的都能换，
      只改本地文件，不请求 115，Emby 不用刷新；修改时间保持原样，Emby 里的排序不受影响。
    </p>

    <div v-if="loading" class="state">
      <HSpinner />
      <span>正在读本地 STRM…（库大、本地是网络盘时要一会儿）</span>
    </div>

    <HAlert v-else-if="loadErr" status="danger" title="没能读取">{{ loadErr }}</HAlert>

    <template v-else-if="job">
      <div v-if="running" class="state">
        <HSpinner />
        <span>
          {{ job.progress?.phase || job.message || '排队中…' }}
          <template v-if="job.progress?.done">（已扫 {{ job.progress.done }} 个）</template>
        </span>
      </div>
      <HAlert v-else-if="result && !result.failed" status="success" title="换好了">{{ job.message }}</HAlert>
      <HAlert v-else-if="result" status="warning" title="有一部分没改成">{{ job.message }}</HAlert>
      <HAlert v-else status="danger" title="没有完成">{{ job.message }}</HAlert>
      <div v-if="!running" class="actions">
        <HButton variant="secondary" size="sm" @click="again">再换一批</HButton>
      </div>
    </template>

    <template v-else-if="groups">
      <div class="block">
        <div class="block-title">本地 STRM 里的地址（共 {{ groups.scanned }} 个，点一下当作旧地址）</div>
        <div class="groups">
          <button v-for="g in groups.groups" :key="g.base + g.station" type="button" class="group" @click="useGroup(g)">
            <code>{{ g.base }}</code>
            <span class="count">{{ g.count }}</span>
            <span v-if="g.station" class="tag">本站</span>
          </button>
        </div>
        <div v-if="!groups.groups.length" class="muted">没认出任何地址开头。</div>
      </div>

      <div class="block">
        <div class="block-title">替换（内容以旧地址开头的换成新地址；写到端口或目录为止，后面紧跟 / 或 ? 才算命中）</div>
        <div v-for="(p, i) in pairs" :key="i" class="pair">
          <HInput v-model="p.from" mono placeholder="旧地址，如 http://172.17.0.1:6086" aria-label="旧地址" />
          <span class="arrow">→</span>
          <HInput v-model="p.to" mono placeholder="新地址，如 http://192.168.1.10:6086" aria-label="新地址" />
          <HButton variant="ghost" size="sm" icon-only aria-label="删掉这一组" @click="removePair(i)">
            <template #icon><X :size="14" /></template>
          </HButton>
        </div>
        <div class="actions">
          <HButton variant="ghost" size="sm" @click="addPair()">
            <template #icon><Plus :size="14" /></template>
            添加一组
          </HButton>
          <span v-if="groups.domain" class="muted">已保存的直连域名：<code>{{ groups.domain }}</code></span>
        </div>
      </div>

      <div v-if="preview" class="block">
        <div class="block-title">预演</div>
        <div v-for="p in preview.pairs" :key="p.from" class="hit">
          <code>{{ p.from }}</code><span class="arrow">→</span><code>{{ p.to }}</code>
          <span class="count">{{ p.count }} 个</span>
        </div>
        <div v-for="x in preview.samples" :key="x.rel" class="sample">
          <div class="rel">{{ x.rel }}</div>
          <code class="old">{{ x.old }}</code>
          <code class="new">{{ x.new }}</code>
        </div>
        <HAlert v-if="!preview.matched" status="warning">没有以这些旧地址开头的 STRM。</HAlert>
      </div>

      <div class="actions end">
        <HButton variant="secondary" :disabled="!filled.length" :loading="previewing" @click="runPreview">预演</HButton>
        <HButton
          variant="primary"
          :disabled="!preview || !preview.matched"
          :loading="starting"
          @click="start"
        >
          替换{{ preview?.matched ? ` ${preview.matched} 个` : '' }}
        </HButton>
      </div>
      <p class="note">
        这里只换开头的地址。改了「直连格式」「保留文件后缀」要在「Strm 管理 → 全量」把「本地已存在时」设为覆盖、跑一次全量；
        要把别的项目的直链换成本站直链并让本站接手管理，用「接管其他项目」。
      </p>
    </template>
  </div>
</template>

<style scoped>
.rebase {
  display: flex;
  flex-direction: column;
  gap: 14px;
  font-size: 13px;
}
.lead,
.note {
  margin: 0;
  color: var(--muted);
  line-height: 1.6;
}
.note,
.muted {
  font-size: 12px;
  color: var(--muted);
}
.state {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 24px 0;
  color: var(--muted);
}
code {
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--foreground);
  word-break: break-all;
}
.block {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.block-title {
  font-size: 12px;
  color: var(--muted);
}
.groups {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.group {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: 100%;
  padding: 4px 10px;
  border: 1px solid var(--border);
  border-radius: var(--r-sm);
  background: var(--surface-secondary);
  color: inherit;
  font: inherit;
  cursor: pointer;
}
.group:hover {
  border-color: var(--accent);
}
.tag {
  font-size: 11px;
  color: var(--accent);
}
.count,
.arrow {
  color: var(--muted);
}
.pair {
  display: grid;
  grid-template-columns: 1fr auto 1fr auto;
  align-items: center;
  gap: 8px;
}
.actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 12px;
}
.actions.end {
  justify-content: flex-end;
}
.hit {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.sample {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 8px 10px;
  border-radius: var(--r-sm);
  background: var(--surface-secondary);
}
.rel {
  font-size: 12px;
  color: var(--muted);
  word-break: break-all;
}
.old {
  color: var(--muted);
  text-decoration: line-through;
}
@media (max-width: 720px) {
  .pair {
    grid-template-columns: 1fr auto;
  }
  .pair .arrow {
    display: none;
  }
}
</style>
