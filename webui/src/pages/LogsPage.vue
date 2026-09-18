<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { NButton, NInput, NPopconfirm, NSwitch } from 'naive-ui'
import { RefreshCw, Trash2 } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import { systemApi } from '@/api'
import { toastError, useFeedback } from '@/composables/useFeedback'

const { message } = useFeedback()

const lines = ref<string[]>([])
const error = ref('')
const loading = ref(false)
const keyword = ref('')
/** 关掉自动刷新是为了能安心翻阅历史日志——3 秒一刷时滚动位置很难保持 */
const autoRefresh = ref(true)
const viewer = ref<HTMLElement | null>(null)

async function load(manual = false) {
  if (manual) loading.value = true
  try {
    const data = await systemApi.logs()
    const raw = typeof data.logs === 'string' ? data.logs : (data.logs ?? []).join('\n')
    // 后端按时间正序追加，这里倒序显示：最新的在最上面，不用每次滚到底
    lines.value = String(raw || '')
      .split('\n')
      .filter((l) => l.trim() !== '')
      .reverse()
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

/** 日志行的级别着色：按前缀符号判定，与后端 `[模块] ✓/✗/○ 消息` 的约定对齐 */
function toneOf(line: string) {
  if (line.includes('✗') || /ERROR|error|失败|错误/.test(line)) return 'err'
  if (line.includes('✓') || /成功|完成/.test(line)) return 'ok'
  if (line.includes('⚠') || /WARN|warn|警告/.test(line)) return 'warn'
  if (line.includes('○')) return 'dim'
  return ''
}

let timer: number | undefined
function startTimer() {
  stopTimer()
  if (autoRefresh.value) timer = window.setInterval(() => load(), 3000)
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
    await systemApi.clearLogs()
    message.success('日志已清空')
    await load(true)
    nextTick(() => viewer.value?.scrollTo({ top: 0 }))
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
  <SectionCard title="任务日志" :hint="`共 ${filtered.length} 行`">
    <template #extra>
      <div class="tools">
        <NInput v-model:value="keyword" size="small" placeholder="过滤关键字" clearable class="filter" />
        <label class="auto">
          <NSwitch v-model:value="autoRefresh" size="small" />
          <span>自动刷新</span>
        </label>
        <NButton size="small" :loading="loading" @click="load(true)">
          <template #icon><RefreshCw :size="14" /></template>
          刷新
        </NButton>
        <NPopconfirm @positive-click="clear">
          <template #trigger>
            <NButton size="small" type="error" ghost>
              <template #icon><Trash2 :size="14" /></template>
              清空
            </NButton>
          </template>
          确定清空任务日志？清空后不可恢复（新日志会继续正常写入）。
        </NPopconfirm>
      </div>
    </template>

    <div ref="viewer" class="viewer">
      <p v-if="error" class="state err">{{ error }}</p>
      <p v-else-if="!filtered.length" class="state">{{ keyword ? '没有匹配的日志行' : '暂无日志' }}</p>
      <div v-for="(l, i) in filtered" v-else :key="i" class="line" :class="toneOf(l)">{{ l }}</div>
    </div>
  </SectionCard>
</template>

<style scoped>
.tools {
  display: flex;
  align-items: center;
  gap: 8px;
}
.filter {
  width: 180px;
}
.auto {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12.5px;
  color: var(--c-text-2);
  cursor: pointer;
  white-space: nowrap;
}

.viewer {
  height: calc(100vh - 230px);
  min-height: 320px;
  overflow: auto;
  padding: 12px 14px;
  border-radius: var(--radius);
  background: var(--c-bg-raised);
  border: 1px solid var(--c-border);
  font-family: var(--font-mono);
  font-size: 12px;
  line-height: 1.75;
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
    width: 110px;
  }
  .auto span {
    display: none;
  }
}
</style>
