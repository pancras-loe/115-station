<script setup lang="ts">
import { computed } from 'vue'
import { Info } from '@lucide/vue'
import { probeErrAdvice } from '@/utils/mediaInfo'

/** 探测失败原因的说明：认得出的失败原因给出「是谁的问题、怎么办」，认不出的不显示 */
const props = defineProps<{ err?: string }>()
const advice = computed(() => probeErrAdvice(props.err))
</script>

<template>
  <div v-if="advice" class="probe-err-note">
    <Info :size="14" class="pen-icon" />
    <div class="pen-body">
      <p class="pen-title">{{ advice.title }}</p>
      <p>{{ advice.why }}</p>
      <p><span class="pen-label">怎么办：</span>{{ advice.todo }}</p>
    </div>
  </div>
</template>

<style scoped>
.probe-err-note {
  display: flex;
  gap: 8px;
  margin-top: 6px;
  padding: 8px 10px;
  border-radius: var(--r-sm);
  background: var(--warning-soft);
  color: var(--warning-soft-foreground);
  font-size: 12px;
  line-height: 1.6;
}
.pen-icon {
  flex: none;
  margin-top: 3px;
}
.pen-body {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.pen-body p {
  margin: 0;
}
.pen-title,
.pen-label {
  font-weight: 600;
}
</style>
