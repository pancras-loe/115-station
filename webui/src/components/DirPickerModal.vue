<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import HModal from '@/components/hero/HModal.vue'
import HSkeleton from '@/components/hero/HSkeleton.vue'
import { ChevronRight, CornerLeftUp, Folder, RefreshCw } from '@lucide/vue'
import { storageApi } from '@/api'
import type { DirEntry } from '@/api/storage'

export type PickerMode = '115' | 'local'

const props = defineProps<{
  show: boolean
  mode: PickerMode
  /** 打开时先定位到这里（通常是输入框的现值），读不出来再退回顶层。115 模式下是可读路径，可以为空 */
  initial?: string
  /** 115 模式的现值 cid；有它才定位（路径可能只是旧配置留下的纯数字） */
  initialCid?: string
}>()
const emit = defineEmits<{
  'update:show': [boolean]
  /** 115 模式回传 { cid, path }，local 只回传 path */
  pick: [{ cid: string; path: string }]
}>()

const TITLES: Record<PickerMode, string> = {
  '115': '选择 115 目录',
  local: '选择本地目录',
}

const items = ref<DirEntry[]>([])
const loading = ref(false)
const note = ref('')
const jumpText = ref('')

// 115 没有「父目录」的概念，只有 cid，所以进入子目录时压栈，返回时出栈。
// trail 是逐级目录名，用来拼出可读路径（cid 本身不含路径信息）；
// null 表示路径不知道（按纯数字 cid 跳过来、又反查不出路径），这时只能回传 cid。
const cid = ref('0')
const trail = ref<string[] | null>([])
const history = ref<{ cid: string; trail: string[] | null }[]>([])

const path = ref('')

const currentLabel = ref('')

// 连点几个目录时请求可能乱序回来，只认最后一次发出去的
let seq = 0

const trailPath = (t: string[]) => '/' + t.join('/')
const segsOf = (p: string) => p.split('/').filter(Boolean)
const isCid = (v: string) => /^\d+$/.test(v)

function label115(c: string, t: string[] | null) {
  if (t === null) return `cid ${c}`
  return t.length ? trailPath(t) : '根目录'
}

/**
 * 读 115 目录。cid / trail / history 只在**读成功之后**一起提交：
 * 以前先改 trail 再发请求，读失败时 trail 已经是子目录、cid 还是父目录，
 * 「选择此目录」回传的就是「子目录的路径 + 父目录的 cid」，存进配置后同步 / 整理的是另一个目录。
 */
async function load115(
  nextCid: string,
  nextTrail: string[] | null,
  opts?: { history?: 'push' | 'pop' | 'reset'; refresh?: boolean; fallback?: string },
) {
  const my = ++seq
  loading.value = true
  note.value = ''
  try {
    const data = await storageApi.dirs115(nextCid, opts?.refresh)
    if (my !== seq) return
    if (opts?.history === 'push') history.value.push({ cid: cid.value, trail: trail.value && [...trail.value] })
    else if (opts?.history === 'pop') history.value.pop()
    else if (opts?.history === 'reset') history.value = []
    cid.value = nextCid
    trail.value = nextTrail
    currentLabel.value = label115(nextCid, nextTrail)
    items.value = data.data ?? []
    if (!items.value.length && (data.count ?? 0) > 0) {
      note.value = `目录共有 ${data.count} 个条目，但没有识别到文件夹（通道 ${data.channel || '?'}，来源 ${data.origin || '?'}）`
    }
  } catch (e) {
    if (my !== seq) return
    const msg = e instanceof Error ? e.message : '加载失败'
    if (opts?.fallback) {
      loading.value = false
      await load115('0', [], { history: 'reset' })
      if (my + 1 === seq) note.value = `${opts.fallback}（${msg}），已回到根目录`
      return
    }
    note.value = `无法打开 ${label115(nextCid, nextTrail)}：${msg}`
  } finally {
    if (my === seq) loading.value = false
  }
}

/**
 * 读本地目录。失败时**留在原目录、保留原列表**，只把原因写在上面：
 * 以前失败会清空列表但「当前目录」不变，用户点了子目录、看着空列表点「选择此目录」，
 * 选中的其实还是上一级（反馈：/media 的下级怎么选都不变）。
 */
async function loadLocal(p: string, opts?: { fallback?: boolean }) {
  const my = ++seq
  loading.value = true
  note.value = ''
  try {
    const data = await storageApi.localDirs(p)
    if (my !== seq) return
    path.value = p
    currentLabel.value = p || '计算机'
    // 「..」与顶上的「上级」重复，且长得和普通文件夹一样，容易误点
    items.value = (data.data ?? []).filter((it) => it.name !== '..')
    if (data.truncated) note.value = '目录较大，仅显示前 1000 个文件夹，可直接在上方输入路径'
  } catch (e) {
    if (my !== seq) return
    if (opts?.fallback) {
      loading.value = false
      return loadLocal('')
    }
    note.value = `无法打开 ${p}：${e instanceof Error ? e.message : '加载失败'}`
  } finally {
    if (my === seq) loading.value = false
  }
}

/** 按 cid 反查可读路径；查不出返回 null（只能按 cid 显示） */
async function trailOfCid(c: string): Promise<string[] | null> {
  try {
    return segsOf((await storageApi.path115(c)).path)
  } catch {
    return null
  }
}

/** 115 打开时定位到现值：路径可读就直接用，只有 cid 就反查一次路径 */
async function open115() {
  const c = props.initialCid?.trim() ?? ''
  if (!isCid(c) || c === '0') return load115('0', [])
  const p = props.initial?.trim() ?? ''
  const t = p.startsWith('/') ? segsOf(p) : await trailOfCid(c)
  return load115(c, t, { fallback: '原来选的目录打不开' })
}

function reset() {
  seq++ // 作废上一次打开时还没回来的请求
  items.value = []
  note.value = ''
  jumpText.value = ''
  cid.value = '0'
  trail.value = []
  history.value = []
  path.value = ''
  currentLabel.value = ''
  if (props.mode === '115') void open115()
  else if (props.initial?.trim()) void loadLocal(props.initial.trim(), { fallback: true })
  else void loadLocal('')
}

function enter(it: DirEntry) {
  if (props.mode === '115') {
    if (it.cid) load115(it.cid, trail.value && [...trail.value, it.name], { history: 'push' })
  } else if (it.path) loadLocal(it.path)
}

function parentPath(p: string) {
  const trimmed = p.replace(/[\\/]+$/, '')
  const idx = Math.max(trimmed.lastIndexOf('\\'), trimmed.lastIndexOf('/'))
  return idx <= 0 ? '' : trimmed.slice(0, idx + 1)
}

/** 能不能往上走。115：有来路，或者知道路径且不在根 */
const canGoUp = computed(() =>
  props.mode === '115' ? history.value.length > 0 || (!!trail.value && trail.value.length > 0) : !!path.value,
)

async function goUp() {
  if (props.mode !== '115') {
    loadLocal(parentPath(path.value))
    return
  }
  const prev = history.value[history.value.length - 1]
  if (prev) {
    load115(prev.cid, prev.trail, { history: 'pop' })
    return
  }
  // 跳转 / 定位过来的没有来路（以前这时「上级」点了没反应）：按路径解析上一级
  const t = trail.value
  if (!t || !t.length) return
  const parent = t.slice(0, -1)
  if (!parent.length) {
    load115('0', [], { history: 'reset' })
    return
  }
  try {
    const data = await storageApi.resolve115(trailPath(parent))
    if (data.cid) load115(data.cid, parent, { history: 'reset' })
  } catch (e) {
    note.value = e instanceof Error ? e.message : '上级目录无法解析'
  }
}

/** 重新拉取当前目录。115 侧绕过后端缓存，否则刚新建的文件夹要等缓存过期才出现 */
function refresh() {
  if (props.mode === '115') load115(cid.value, trail.value, { refresh: true })
  else loadLocal(path.value)
}

/** 手动输入跳转。115 同时支持纯数字 cid 与 /路径/写法 */
async function jump() {
  const v = jumpText.value.trim()
  if (!v) return
  if (props.mode !== '115') {
    loadLocal(v)
    return
  }
  if (isCid(v)) {
    if (v === '0') return load115('0', [], { history: 'reset' })
    // 反查一次路径：以前直接按根目录处理，标题写「根目录」、选中后输入框是空的
    load115(v, await trailOfCid(v), { history: 'reset' })
    return
  }
  try {
    const data = await storageApi.resolve115(v)
    if (data.cid) load115(data.cid, segsOf(v), { history: 'reset' })
  } catch (e) {
    // 解析失败只报原因，列表与当前目录保持不动
    note.value = e instanceof Error ? e.message : '路径无法解析'
  }
}

/** 顶层（本地的盘符 / 根目录列表、115 的网盘根）不能选：工作目录选成网盘根，整理与清理会从整个网盘开始动 */
const canConfirm = computed(() => !loading.value && (props.mode === '115' ? cid.value !== '0' : !!path.value))

/** 路径不知道时回传 cid 本身：Cid115Input 把纯数字当 cid，不会去解析 */
const pathOf115 = (c: string, t: string[] | null) => (t === null ? c : trailPath(t))

function confirm() {
  if (!canConfirm.value) return
  if (props.mode === '115') {
    emit('pick', { cid: cid.value, path: pathOf115(cid.value, trail.value) })
  } else {
    emit('pick', { cid: '', path: path.value })
  }
  emit('update:show', false)
}

/** 行尾「选择」：不用先点进去，直接选这一行的子目录 */
function pickItem(it: DirEntry) {
  if (props.mode === '115') {
    if (!it.cid) return
    emit('pick', { cid: it.cid, path: pathOf115(it.cid, trail.value && [...trail.value, it.name]) })
  } else {
    if (!it.path) return
    emit('pick', { cid: '', path: it.path })
  }
  emit('update:show', false)
}

watch(() => props.show, (v) => v && reset())
</script>

<template>
  <HModal :show="show" :title="TITLES[mode]" width="520px" @update:show="emit('update:show', $event)">
    <div class="picker">
      <div class="jump">
        <HInput
          v-model="jumpText"
          mono
          :placeholder="mode === '115' ? '输入路径或纯数字 cid 直接跳转' : '输入路径直接跳转'"
          @enter="jump"
        />
        <HButton variant="tertiary" @click="jump">跳转</HButton>
      </div>

      <div class="crumb">
        <HButton size="sm" variant="ghost" :disabled="loading || !canGoUp" @click="goUp">
          <template #icon><CornerLeftUp /></template>
          上级
        </HButton>
        <span class="crumb-path">{{ currentLabel }}</span>
        <HButton size="sm" variant="ghost" :disabled="loading" @click="refresh">
          <template #icon><RefreshCw /></template>
          刷新
        </HButton>
      </div>

      <div class="list" :aria-busy="loading">
        <div v-if="loading" class="skel">
          <HSkeleton v-for="i in 6" :key="i" height="36px" radius="12px" />
        </div>
        <template v-else>
          <div v-if="note" class="state note">{{ note }}</div>
          <div v-for="(it, i) in items" :key="i" class="row">
            <button type="button" class="item" :title="`进入 ${it.name || it.path}`" @click="enter(it)">
              <Folder :size="16" class="item-ico" />
              <span class="item-name">{{ it.name || it.path }}</span>
              <ChevronRight :size="15" class="item-arrow" />
            </button>
            <HButton size="sm" variant="ghost" class="row-pick" @click="pickItem(it)">选择</HButton>
          </div>
          <div v-if="!items.length && !note" class="state">该目录下没有子文件夹</div>
        </template>
      </div>
    </div>

    <template #footer>
      <span class="footer-hint">{{ canConfirm ? `当前目录：${currentLabel}` : loading ? '加载中…' : '点文件夹进入，或点行尾「选择」' }}</span>
      <div class="footer-btns">
        <HButton variant="tertiary" @click="emit('update:show', false)">取消</HButton>
        <HButton variant="primary" :disabled="!canConfirm" @click="confirm">选择当前目录</HButton>
      </div>
    </template>
  </HModal>
</template>

<style scoped>
.picker {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.jump {
  display: flex;
  gap: 8px;
}

.crumb {
  display: flex;
  align-items: center;
  gap: 8px;
}
.crumb-path {
  flex: 1;
  min-width: 0;
  font-size: 12.5px;
  font-family: var(--font-mono);
  color: var(--muted);
  word-break: break-all;
}

/* 列表高度跟着视口走：手机上是底部抽屉，固定 320px 会把下面的按钮挤出屏幕 */
.list {
  height: min(360px, 48dvh);
  overflow-y: auto;
  overscroll-behavior: contain;
  padding: 4px;
  border-radius: 18px;
  background: var(--surface-secondary);
}
.skel {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.row {
  display: flex;
  align-items: center;
  gap: 4px;
  border-radius: 14px;
}
@media (hover: hover) {
  .row:hover {
    background: var(--surface);
  }
}
.row-pick {
  flex: none;
  margin-right: 4px;
}

.item {
  all: unset;
  box-sizing: border-box;
  display: flex;
  align-items: center;
  gap: 10px;
  flex: 1;
  min-width: 0;
  min-height: 40px;
  padding: 8px 12px;
  border-radius: 14px;
  cursor: pointer;
  font-size: 13.5px;
  color: var(--foreground);
  transition:
    background-color 0.12s,
    transform 0.12s;
}
@media (hover: hover) {
  .item:hover {
    background: var(--surface);
  }
}
.item:active {
  transform: scale(0.99);
  background: var(--surface);
}
.item:focus-visible {
  box-shadow: 0 0 0 2px var(--focus);
}
.item-ico {
  flex-shrink: 0;
  color: var(--accent);
}
.item-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.item-arrow {
  flex-shrink: 0;
  color: var(--muted);
}

.state {
  display: grid;
  place-items: center;
  padding: 26px 16px;
  text-align: center;
  font-size: 12.5px;
  color: var(--muted);
}
.state.note {
  padding: 12px 16px;
}

.footer-hint {
  flex: 1 1 200px;
  min-width: 0;
  font-size: 12px;
  font-family: var(--font-mono);
  color: var(--muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.footer-btns {
  display: flex;
  gap: 8px;
}
@media (max-width: 639px) {
  .footer-hint {
    flex-basis: 100%;
  }
  .footer-btns {
    width: 100%;
  }
  .footer-btns > * {
    flex: 1;
  }
}
</style>
