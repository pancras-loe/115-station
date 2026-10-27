<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import HPopconfirm from '@/components/hero/HPopconfirm.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import { RefreshCw, Trash2 } from '@lucide/vue'
import { pluginsApi } from '@/api'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { logToneOf } from '@/utils/logTone'

/**
 * 插件弹窗里的「日志」页签：只看这个插件自己的那份（后端 /logs/plugins/<key>.log），
 * 总日志被同步、整理刷掉之后，上一次跑的完整过程还在这里。
 */
const props = defineProps<{ pluginKey: string }>()

const { message } = useFeedback()
const lines = ref<string[]>([])
const error = ref('')
const loading = ref(false)
const keyword = ref('')
/** 翻旧日志时关掉，免得一刷新列表又变 */
const autoRefresh = ref(true)

async function load(manual = false) {
  if (manual) loading.value = true
  try {
    const d = await pluginsApi.pluginLogs(props.pluginKey)
    // 最新的在最上面
    lines.value = (d.lines ?? []).slice().reverse()
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

const filtered = computed(() => {
  const k = keyword.value.trim().toLowerCase()
  if (!k) return lines.value
  return lines.value.filter((l) => l.toLowerCase().includes(k))
})

let timer: number | undefined
function startTimer() {
  stopTimer()
  if (autoRefresh.value) timer = window.setInterval(() => load(), 5000)
}
function stopTimer() {
  if (timer !== undefined) {
    clearInterval(timer)
    timer = undefined
  }
}
watch(autoRefresh, startTimer)

async function clear() {
  try {
    await pluginsApi.clearPluginLogs(props.pluginKey)
    message.success('已清空')
    await load(true)
  } catch (e) {
    toastError(e, '清空失败')
  }
}

onMounted(() => {
  load(true)
  startTimer()
})
onUnmounted(stopTimer)
</script>

<template>
  <div class="plog">
    <div class="tools">
      <HInput v-model="keyword" placeholder="过滤关键字" clearable class="filter" />
      <span class="count">{{ filtered.length }} 行</span>
      <span class="grow" />
      <label class="auto">
        <HSwitch v-model="autoRefresh" />
        <span>自动刷新</span>
      </label>
      <HButton variant="tertiary" size="sm" :loading="loading" @click="load(true)">
        <template #icon><RefreshCw :size="14" /></template>
        刷新
      </HButton>
      <HPopconfirm danger @confirm="void clear()">
        <HButton variant="danger-soft" size="sm">
          <template #icon><Trash2 :size="14" /></template>
          清空
        </HButton>
        <template #content>只清空这个插件的日志，总日志里的照旧保留。继续？</template>
      </HPopconfirm>
    </div>
    <div class="viewer">
      <p v-if="error" class="state err">{{ error }}</p>
      <p v-else-if="!filtered.length" class="state">{{ keyword ? '没有匹配的日志行' : '还没有日志，跑一次之后这里会有记录' }}</p>
      <div v-for="(l, i) in filtered" v-else :key="i" class="line" :class="logToneOf(l)">{{ l }}</div>
    </div>
  </div>
</template>

<style scoped>
.plog {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.tools {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.filter {
  width: 180px;
}
.count {
  font-size: 12.5px;
  color: var(--muted);
}
.grow {
  flex: 1;
}
.auto {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12.5px;
  color: var(--muted);
  cursor: pointer;
  white-space: nowrap;
}
.viewer {
  height: min(56vh, 520px);
  min-height: 240px;
  overflow: auto;
  padding: 10px 12px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  font-family: var(--font-mono);
  font-size: 12px;
  line-height: 1.7;
}
.line {
  white-space: pre-wrap;
  word-break: break-all;
  color: var(--c-text-2);
}
.line.ok {
  color: var(--c-success);
}
.line.err {
  color: var(--c-danger);
}
.line.warn {
  color: var(--c-warning);
}
.line.dim {
  color: var(--c-text-3);
}
.state {
  margin: 0;
  padding: 24px 0;
  text-align: center;
  color: var(--c-text-3);
}
.state.err {
  color: var(--c-danger);
}
@media (max-width: 720px) {
  .filter {
    width: 120px;
  }
  .auto span {
    display: none;
  }
}
</style>
