<script setup lang="ts">
import { onMounted, onUnmounted } from 'vue'
import { Check, X } from '@lucide/vue'
import { useTaskStore } from '@/stores/task'

const task = useTaskStore()

onMounted(() => task.start())
onUnmounted(() => task.stop())
</script>

<template>
  <div v-if="task.status.running" class="bar" role="status">
    <div class="line">
      <span class="pulse" />
      <strong>{{ task.status.task || '任务' }}</strong>
      <span class="dim">正在执行（已运行 {{ task.status.elapsed || '-' }}）</span>
    </div>
    <div v-if="task.status.progress" class="progress">{{ task.status.progress }}</div>
  </div>

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
  padding: 12px 16px;
  border-radius: 20px;
  background: var(--accent-soft);
  color: var(--accent-soft-foreground);
}
.line {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
}
.dim {
  color: color-mix(in oklab, var(--foreground) 70%, var(--muted));
}
.progress {
  margin-top: 4px;
  padding-left: 15px;
  font-size: 12px;
  color: color-mix(in oklab, var(--foreground) 70%, var(--muted));
  word-break: break-all;
}
.pulse {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  flex: none;
  background: var(--accent);
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
  font-size: 12px;
  color: var(--muted);
}
.recent-label {
  font-weight: 500;
}
.recent-item {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.ok {
  color: var(--success);
}
.err {
  color: var(--danger);
}
</style>
