<script setup lang="ts">
import { ref } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import { FolderOpen } from '@lucide/vue'
import DirPickerModal, { type PickerMode } from './DirPickerModal.vue'

defineProps<{ modelValue: string; placeholder?: string; mode?: PickerMode }>()
const emit = defineEmits<{ 'update:modelValue': [string] }>()

const pickerShow = ref(false)
</script>

<template>
  <div>
    <div class="pick-row">
      <HInput
        mono
        :model-value="modelValue"
        :placeholder="placeholder || '/media'"
        @update:model-value="emit('update:modelValue', $event)"
      />
      <HButton variant="tertiary" class="pick-btn" aria-label="选择目录" @click="pickerShow = true">
        <template #icon><FolderOpen /></template>
        <span class="pick-label">选择目录</span>
      </HButton>
    </div>

    <DirPickerModal
      v-model:show="pickerShow"
      :mode="mode || 'local'"
      @pick="emit('update:modelValue', $event.path)"
    />
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
