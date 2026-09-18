<script setup lang="ts">
import { ref } from 'vue'
import { NButton, NInput, NInputGroup } from 'naive-ui'
import { FolderOpen } from '@lucide/vue'
import DirPickerModal, { type PickerMode } from './DirPickerModal.vue'

defineProps<{ modelValue: string; placeholder?: string; mode?: PickerMode }>()
const emit = defineEmits<{ 'update:modelValue': [string] }>()

const pickerShow = ref(false)
</script>

<template>
  <div>
    <NInputGroup>
      <NInput
        :value="modelValue"
        :placeholder="placeholder || '/media'"
        @update:value="emit('update:modelValue', $event)"
      />
      <NButton @click="pickerShow = true">
        <template #icon><FolderOpen :size="15" /></template>
        选择目录
      </NButton>
    </NInputGroup>

    <DirPickerModal
      v-model:show="pickerShow"
      :mode="mode || 'local'"
      @pick="emit('update:modelValue', $event.path)"
    />
  </div>
</template>
