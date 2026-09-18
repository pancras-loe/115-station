<script setup lang="ts">
import { ref, watch } from 'vue'
import { NButton, NInput, NInputGroup, NModal, NSpin } from 'naive-ui'
import { ChevronRight, CornerLeftUp, Folder } from '@lucide/vue'
import { storageApi } from '@/api'
import type { DirEntry } from '@/api/storage'

export type PickerMode = '115' | 'local' | 'cd2'

const props = defineProps<{ show: boolean; mode: PickerMode }>()
const emit = defineEmits<{
  'update:show': [boolean]
  /** 115 模式回传 { cid, path }，local/cd2 只回传 path */
  pick: [{ cid: string; path: string }]
}>()

const TITLES: Record<PickerMode, string> = {
  '115': '选择 115 目录',
  local: '选择本地目录',
  cd2: '选择 CD2 目录',
}

const items = ref<DirEntry[]>([])
const loading = ref(false)
const note = ref('')
const jumpText = ref('')

// 115 没有「父目录」的概念，只有 cid，所以进入子目录时压栈，返回时出栈。
// trail 是逐级目录名，用来拼出可读路径（cid 本身不含路径信息）。
const cid = ref('0')
const trail = ref<string[]>([])
const history = ref<{ cid: string; trail: string[] }[]>([])

const path = ref('')

const currentLabel = ref('')

async function load115(nextCid: string, opts?: { enter?: string; restore?: string[] }) {
  loading.value = true
  note.value = ''
  try {
    if (opts?.enter) {
      history.value.push({ cid: cid.value, trail: [...trail.value] })
      trail.value.push(opts.enter)
    } else if (opts?.restore) {
      trail.value = opts.restore
    } else {
      trail.value = [] // 根目录 / 手动跳转
      history.value = []
    }
    const data = await storageApi.dirs115(nextCid)
    cid.value = nextCid
    currentLabel.value = trail.value.length ? '/' + trail.value.join('/') : '根目录'
    items.value = data.data ?? []
    if (!items.value.length && (data.count ?? 0) > 0) {
      note.value = `目录共有 ${data.count} 个条目，但没有识别到文件夹（通道 ${data.channel || '?'}，来源 ${data.origin || '?'}）`
    }
  } catch (e) {
    items.value = []
    note.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

async function loadLocal(p: string) {
  loading.value = true
  note.value = ''
  try {
    const data = await storageApi.localDirs(p)
    path.value = p
    currentLabel.value = p || '计算机'
    items.value = data.data ?? []
    if (data.truncated) note.value = '目录较大，仅显示前 1000 个文件夹，可直接在上方输入路径'
  } catch (e) {
    items.value = []
    note.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

async function loadCd2(p: string) {
  loading.value = true
  note.value = ''
  try {
    const data = await storageApi.cd2Dirs(p)
    path.value = data.path || p || '/'
    currentLabel.value = path.value
    items.value = data.data ?? []
  } catch (e) {
    items.value = []
    note.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

function reset() {
  items.value = []
  note.value = ''
  jumpText.value = ''
  cid.value = '0'
  trail.value = []
  history.value = []
  path.value = ''
  if (props.mode === '115') load115('0')
  else if (props.mode === 'cd2') loadCd2('/')
  else loadLocal('')
}

function enter(it: DirEntry) {
  if (props.mode === '115') load115(it.cid ?? '0', { enter: it.name })
  else if (props.mode === 'cd2') loadCd2(it.path ?? '/')
  else loadLocal(it.path ?? '')
}

function parentPath(p: string) {
  const trimmed = p.replace(/[\\/]+$/, '')
  const idx = Math.max(trimmed.lastIndexOf('\\'), trimmed.lastIndexOf('/'))
  return idx <= 0 ? '' : trimmed.slice(0, idx + 1)
}

function goUp() {
  if (props.mode === '115') {
    const prev = history.value.pop()
    if (!prev) return // 已在根目录
    load115(prev.cid, { restore: prev.trail })
  } else if (props.mode === 'cd2') {
    const parts = (path.value || '/').replace(/\/+$/, '').split('/').filter(Boolean)
    parts.pop()
    loadCd2('/' + parts.join('/'))
  } else {
    loadLocal(parentPath(path.value))
  }
}

/** 手动输入跳转。115 同时支持纯数字 cid 与 /路径/写法 */
async function jump() {
  const v = jumpText.value.trim()
  if (!v) return
  if (props.mode === '115') {
    if (/^\d+$/.test(v)) {
      load115(v)
      return
    }
    try {
      const data = await storageApi.resolve115(v)
      if (data.cid) {
        const t = v.replace(/^\/+|\/+$/g, '').split('/').filter(Boolean)
        load115(data.cid, { restore: t })
      }
    } catch (e) {
      items.value = []
      note.value = e instanceof Error ? e.message : '路径无法解析'
    }
  } else if (props.mode === 'cd2') {
    loadCd2(v.startsWith('/') ? v : '/' + v)
  } else {
    loadLocal(v)
  }
}

function confirm() {
  if (props.mode === '115') {
    emit('pick', { cid: cid.value, path: trail.value.length ? '/' + trail.value.join('/') : '' })
  } else {
    emit('pick', { cid: '', path: path.value || (props.mode === 'cd2' ? '/' : '/media') })
  }
  emit('update:show', false)
}

watch(() => props.show, (v) => v && reset())
</script>

<template>
  <NModal
    :show="show"
    preset="card"
    :title="TITLES[mode]"
    style="width: 520px"
    @update:show="emit('update:show', $event)"
  >
    <div class="picker">
      <NInputGroup>
        <NInput
          v-model:value="jumpText"
          :placeholder="mode === '115' ? '输入路径或纯数字 cid 直接跳转' : '输入路径直接跳转'"
          @keyup.enter="jump"
        />
        <NButton @click="jump">跳转</NButton>
      </NInputGroup>

      <div class="crumb">
        <NButton size="tiny" quaternary :disabled="loading" @click="goUp">
          <template #icon><CornerLeftUp :size="14" /></template>
          上级
        </NButton>
        <span class="crumb-path">{{ currentLabel }}</span>
      </div>

      <div class="list">
        <div v-if="loading" class="state"><NSpin size="small" /></div>
        <template v-else>
          <div v-if="note" class="state note">{{ note }}</div>
          <button v-for="(it, i) in items" :key="i" class="item" @click="enter(it)">
            <Folder :size="15" class="item-ico" />
            <span class="item-name">{{ it.name || it.path }}</span>
            <ChevronRight :size="14" class="item-arrow" />
          </button>
          <div v-if="!items.length && !note" class="state">该目录下没有子文件夹</div>
        </template>
      </div>
    </div>

    <template #footer>
      <div class="footer">
        <span class="footer-hint">选择当前目录：{{ currentLabel || '—' }}</span>
        <NButton @click="emit('update:show', false)">取消</NButton>
        <NButton type="primary" @click="confirm">选择此目录</NButton>
      </div>
    </template>
  </NModal>
</template>

<style scoped>
.picker {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.crumb {
  display: flex;
  align-items: center;
  gap: 8px;
}
.crumb-path {
  flex: 1;
  min-width: 0;
  font-size: 12px;
  color: var(--c-text-3);
  word-break: break-all;
}

.list {
  height: 320px;
  overflow-y: auto;
  border: 1px solid var(--c-border);
  border-radius: var(--radius);
  background: var(--c-bg-raised);
}

.item {
  all: unset;
  box-sizing: border-box;
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 8px 12px;
  cursor: pointer;
  font-size: 13.5px;
  color: var(--c-text-2);
  border-bottom: 1px solid var(--c-border);
  transition: background-color 0.12s, color 0.12s;
}
.item:last-child {
  border-bottom: none;
}
.item:hover {
  background: var(--c-bg-hover);
  color: var(--c-text-1);
}
.item-ico {
  flex-shrink: 0;
  color: var(--c-text-4);
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
  color: var(--c-text-4);
}

.state {
  display: grid;
  place-items: center;
  padding: 26px 16px;
  text-align: center;
  font-size: 12.5px;
  color: var(--c-text-3);
}
.state.note {
  padding: 12px 16px;
  border-bottom: 1px solid var(--c-border);
  opacity: 0.85;
}

.footer {
  display: flex;
  align-items: center;
  gap: 8px;
}
.footer-hint {
  flex: 1;
  min-width: 0;
  font-size: 12px;
  color: var(--c-text-3);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
