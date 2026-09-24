<script setup lang="ts">
import { ref, watch } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import HModal from '@/components/hero/HModal.vue'
import HSkeleton from '@/components/hero/HSkeleton.vue'
import { ChevronRight, CornerLeftUp, Folder } from '@lucide/vue'
import { storageApi } from '@/api'
import type { DirEntry } from '@/api/storage'

export type PickerMode = '115' | 'local'

const props = defineProps<{ show: boolean; mode: PickerMode }>()
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

function reset() {
  items.value = []
  note.value = ''
  jumpText.value = ''
  cid.value = '0'
  trail.value = []
  history.value = []
  path.value = ''
  if (props.mode === '115') load115('0')
  else loadLocal('')
}

function enter(it: DirEntry) {
  if (props.mode === '115') load115(it.cid ?? '0', { enter: it.name })
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
  } else {
    loadLocal(v)
  }
}

function confirm() {
  if (props.mode === '115') {
    emit('pick', { cid: cid.value, path: trail.value.length ? '/' + trail.value.join('/') : '' })
  } else {
    emit('pick', { cid: '', path: path.value || '/media' })
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
        <HButton size="sm" variant="ghost" :disabled="loading" @click="goUp">
          <template #icon><CornerLeftUp /></template>
          上级
        </HButton>
        <span class="crumb-path">{{ currentLabel }}</span>
      </div>

      <div class="list" :aria-busy="loading">
        <div v-if="loading" class="skel">
          <HSkeleton v-for="i in 6" :key="i" height="36px" radius="12px" />
        </div>
        <template v-else>
          <div v-if="note" class="state note">{{ note }}</div>
          <button v-for="(it, i) in items" :key="i" type="button" class="item" @click="enter(it)">
            <Folder :size="16" class="item-ico" />
            <span class="item-name">{{ it.name || it.path }}</span>
            <ChevronRight :size="15" class="item-arrow" />
          </button>
          <div v-if="!items.length && !note" class="state">该目录下没有子文件夹</div>
        </template>
      </div>
    </div>

    <template #footer>
      <span class="footer-hint">选择：{{ currentLabel || '—' }}</span>
      <div class="footer-btns">
        <HButton variant="tertiary" @click="emit('update:show', false)">取消</HButton>
        <HButton variant="primary" @click="confirm">选择此目录</HButton>
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

.item {
  all: unset;
  box-sizing: border-box;
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
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
