<script setup lang="ts">
import { ref, watch } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import { FolderOpen } from '@lucide/vue'
import DirPickerModal from './DirPickerModal.vue'
import { storageApi } from '@/api'

/**
 * 115 目录输入框。
 *
 * 核心约束（旧版靠 DOM dataset 维护，这里建模成值对象）：
 * **cid 只有在它对应的 path 与当前输入值一致时才可信。**
 * 用户手改了路径而 cid 还是上一个目录的，静默拿旧 cid 去同步会搬错整个目录。
 * 所以 path 一变就把 cid 作废，重新解析成功才恢复。
 */
export interface CidValue {
  /** 115 目录 id。空串表示「当前 path 尚未解析出可信的 cid」 */
  cid: string
  /** 可读路径，也是输入框里显示的值 */
  path: string
}

const props = defineProps<{
  modelValue: CidValue
  placeholder?: string
}>()
const emit = defineEmits<{ 'update:modelValue': [CidValue] }>()

const pickerShow = ref(false)
const resolving = ref(false)

/** 输入框的文本；与 modelValue.path 双向同步 */
const text = ref(props.modelValue.path)
watch(
  () => props.modelValue.path,
  (v) => {
    if (v !== text.value) text.value = v
  },
)

let debounce: number | undefined

function onInput(v: string) {
  text.value = v
  // 纯数字直接就是 cid，不需要解析
  if (/^\d+$/.test(v.trim())) {
    emit('update:modelValue', { cid: v.trim(), path: v.trim() })
    return
  }
  // 路径变了 → 旧 cid 立刻作废，防止提交时用到失配的 cid
  emit('update:modelValue', { cid: '', path: v })
  clearTimeout(debounce)
  debounce = window.setTimeout(() => resolve(v), 600)
}

async function resolve(v: string) {
  const path = v.trim()
  if (!path || /^\d+$/.test(path)) return
  resolving.value = true
  try {
    const data = await storageApi.resolve115(path)
    // 解析期间用户可能又改了输入，结果已经过期就丢弃
    if (data.cid && text.value.trim() === path) {
      emit('update:modelValue', { cid: data.cid, path: v })
    }
  } catch {
    // 解析失败保持 cid 为空，由提交前的校验拦截并提示用户重选目录
  } finally {
    resolving.value = false
  }
}

function onPick(v: { cid: string; path: string }) {
  text.value = v.path
  emit('update:modelValue', v)
}

/** 提交前兜底：防抖还没触发时主动解析一次，返回可信的 cid（拿不到返回空串） */
async function ensureCid(): Promise<string> {
  const v = text.value.trim()
  if (/^\d+$/.test(v)) return v
  if (props.modelValue.cid && props.modelValue.path.trim() === v) return props.modelValue.cid
  clearTimeout(debounce)
  await resolve(v)
  return props.modelValue.path.trim() === v ? props.modelValue.cid : ''
}

defineExpose({ ensureCid })
</script>

<template>
  <div>
    <div class="pick-row">
      <HInput
        :model-value="text"
        :placeholder="placeholder || '选择或填写 cid'"
        :loading="resolving"
        @update:model-value="onInput"
      />
      <HButton variant="tertiary" class="pick-btn" aria-label="选择目录" @click="pickerShow = true">
        <template #icon><FolderOpen /></template>
        <span class="pick-label">选择目录</span>
      </HButton>
    </div>

    <DirPickerModal v-model:show="pickerShow" mode="115" @pick="onPick" />
  </div>
</template>

<style scoped>
/* 输入框 + 旁边一颗胶囊按钮（HeroUI 不做「按钮焊在输入框上」的组合），手机上按钮只留图标 */
.pick-row {
  display: flex;
  gap: 8px;
}
.pick-btn {
  flex: none;
}
@media (max-width: 720px) {
  .pick-label {
    display: none;
  }
  .pick-btn {
    width: 40px;
    padding: 0;
  }
}
</style>
