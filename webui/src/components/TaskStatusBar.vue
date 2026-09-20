<script setup lang="ts">
import { onMounted, onUnmounted } from 'vue'
import { NAlert } from 'naive-ui'
import { Check, X } from '@lucide/vue'
import { useTaskStore } from '@/stores/task'

const task = useTaskStore()

onMounted(() => task.start())
onUnmounted(() => task.stop())
</script>

<template>
  <NAlert v-if="task.status.running" class="bar" type="info" :bordered="false">
    <div class="line">
      <span class="pulse" />
      <strong>{{ task.status.task || '任务' }}</strong>
      <span class="dim">正在执行（已运行 {{ task.status.elapsed || '-' }}）</span>
    </div>
    <div v-if="task.status.progress" class="progress">{{ task.status.progress }}</div>
  </NAlert>

  <div v-else-if="task.status.recent?.length" class="recent">
    <span class="recent-label">最近任务</span>
    <span v-for="(r, i) in task.status.recent.slice(0, 3)" :key="i" class="recent-item">
      <Check v-if="r.ok" :size="13" class="ok" />
      <X v-else :size="13" class="err" />
      {{ r.name }}（{{ r.elapsed }}，{{ r.start }}）
      <span v-if="r.message" class="err">{{ r.message }}</span>
    </span>
  </div>
</template>

<style scoped>
.bar {
  margin-bottom: 14px;
}
.line {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
}
.dim {
  color: var(--c-text-2);
}
.progress {
  margin-top: 4px;
  font-size: 12px;
  color: var(--c-text-2);
}
.pulse {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--c-primary);
  animation: pulse 1.4s ease-in-out infinite;
}
@keyframes pulse {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.25;
  }
}

.recent {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 16px;
  margin-bottom: 14px;
  font-size: 12px;
  color: var(--c-text-3);
}
.recent-label {
  color: var(--c-text-4);
}
.recent-item {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.ok {
  color: var(--c-success);
}
.err {
  color: var(--c-danger);
}
</style>
