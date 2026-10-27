<script setup lang="ts">
import { computed } from 'vue'
import HCheckbox from '@/components/hero/HCheckbox.vue'
import { NOTIFY_KINDS } from '@/types/message'

/**
 * 推哪些消息类型。存的是勾选的类型；一个都不勾 = 全推（同 MoviePilot 的 IYUU 插件），
 * 所以界面上「全部」勾着时存空数组，以后新加的类型也照推。
 */
const model = defineModel<string[]>({ required: true })

const all = computed(() => model.value.length === 0)
function isOn(k: string) {
  return all.value || model.value.includes(k)
}
function toggle(k: string, on: boolean) {
  // 从「全部」里去掉一个：展开成其余全部
  const cur = all.value ? NOTIFY_KINDS.map((x) => x.value) : [...model.value]
  const next = on ? [...new Set([...cur, k])] : cur.filter((x) => x !== k)
  // 勾满了收回成空 = 全部；一个都不剩不许（那等于关掉通道，用状态开关关）
  if (next.length === 0) return
  model.value = next.length === NOTIFY_KINDS.length ? [] : NOTIFY_KINDS.map((x) => x.value).filter((x) => next.includes(x))
}
</script>

<template>
  <div class="kinds">
    <HCheckbox :checked="all" @update:checked="(v) => v && (model = [])">全部类型</HCheckbox>
    <div class="grid">
      <HCheckbox v-for="k in NOTIFY_KINDS" :key="k.value" :checked="isOn(k.value)" @update:checked="(v) => toggle(k.value, v)">
        <span class="k">{{ k.label }}</span>
        <span class="hint">{{ k.hint }}</span>
      </HCheckbox>
    </div>
  </div>
</template>

<style scoped>
.kinds {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
  gap: 8px 16px;
}
.k {
  margin-right: 6px;
}
.hint {
  color: var(--muted);
  font-size: 12px;
}
</style>
