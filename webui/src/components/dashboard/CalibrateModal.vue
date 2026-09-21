<script setup lang="ts">
import { ref, watch } from 'vue'
import { NAlert, NButton, NModal, NSpin, NTag } from 'naive-ui'
import { dashboardApi } from '@/api'
import type { CalibrateResult } from '@/types/dashboard'
import { num } from '@/utils/format'
import { toastError, useFeedback } from '@/composables/useFeedback'

/**
 * 媒体库台账校准。
 *
 * 整理台账（MediaLibrary）只在整理时写入，手工删本地 STRM、解除 Emby 目录关联
 * 都不会回头改它，于是它会一直停在历史最高水位。这里拿本地 STRM 树当事实，
 * 把已经没有落点的台账行清掉 —— 只动数据库这一张表，不碰网盘和 Emby。
 *
 * 一律先预演再执行：打开弹窗时拉一次预演，用户看清要删什么才给按钮。
 */
const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ 'update:show': [boolean]; done: [] }>()

const { message } = useFeedback()
const loading = ref(false)
const result = ref<CalibrateResult | null>(null)
const failed = ref('')

async function preview() {
  loading.value = true
  failed.value = ''
  result.value = null
  try {
    result.value = await dashboardApi.calibrate(false)
  } catch (e) {
    failed.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

async function apply() {
  loading.value = true
  try {
    const r = await dashboardApi.calibrate(true)
    result.value = r
    message.success(`已清除 ${r.removed} 条失效台账`)
    emit('done')
  } catch (e) {
    toastError(e, '校准失败')
  } finally {
    loading.value = false
  }
}

watch(
  () => props.show,
  (v) => {
    if (v) preview()
  },
)
</script>

<template>
  <NModal
    :show="show"
    preset="card"
    title="媒体库台账校准"
    style="width: 640px"
    @update:show="emit('update:show', $event)"
  >
    <div class="body">
      <p class="intro">
        以<b>本地 STRM 目录</b>为准核对整理台账：标题目录已经不在本地的，说明这部片早就不在库里，
        台账行留着只会让总览面板越数越多。校准只删台账行，不动网盘、不动 Emby、不动 STRM 文件。
      </p>

      <div v-if="loading && !result" class="center"><NSpin size="small" /></div>

      <NAlert v-else-if="failed" type="error" :bordered="false">{{ failed }}</NAlert>

      <template v-else-if="result">
        <div class="stats">
          <div class="cell">
            <span class="k">台账共</span><span class="v">{{ num(result.total) }}</span>
          </div>
          <div class="cell">
            <span class="k">本地仍在</span><span class="v ok">{{ num(result.kept) }}</span>
          </div>
          <div class="cell">
            <span class="k">{{ result.applied ? '已清除' : '待清除' }}</span>
            <span class="v warn">{{ num(result.applied ? result.removed : result.stale) }}</span>
          </div>
          <div class="cell">
            <span class="k">无法判断</span><span class="v">{{ num(result.skipped) }}</span>
          </div>
        </div>

        <div class="root">本地媒体库根：<code>{{ result.local_root }}</code></div>

        <div v-if="result.libraries.length" class="block">
          <div class="block-title">本地实际部数（数的是标题目录，季目录不重复计）</div>
          <div class="libs">
            <NTag v-for="l in result.libraries" :key="l.name" size="small" :bordered="false">
              {{ l.name }} · {{ num(l.count) }}
            </NTag>
          </div>
        </div>

        <div v-if="!result.applied && result.sample.length" class="block">
          <div class="block-title">将被清除的条目（前 {{ result.sample.length }} 条）</div>
          <ul class="sample">
            <li v-for="(s, i) in result.sample" :key="i">
              <span class="s-title">{{ s.title }}</span>
              <span class="s-year">{{ s.year }}</span>
              <span class="s-path">{{ s.target_path }}</span>
            </li>
          </ul>
        </div>

        <NAlert v-if="result.applied" type="success" :bordered="false">
          已清除 {{ num(result.removed) }} 条失效台账，总览面板的数字会在下次刷新后对上。
        </NAlert>
        <NAlert v-else-if="result.stale === 0" type="success" :bordered="false">
          台账与本地目录一致，没有需要清理的条目。
        </NAlert>
        <NAlert v-if="!result.applied && result.skipped > 0" type="info" :bordered="false">
          有 {{ num(result.skipped) }} 条台账没有记录落点（早期版本写的记录），无法核对，一律保留。
        </NAlert>
      </template>
    </div>

    <template #footer>
      <div class="footer">
        <NButton quaternary :disabled="loading" @click="emit('update:show', false)">
          {{ result?.applied ? '完成' : '取消' }}
        </NButton>
        <NButton
          v-if="result && !result.applied && result.stale > 0"
          type="warning"
          :loading="loading"
          @click="apply"
        >
          清除 {{ num(result.stale) }} 条失效台账
        </NButton>
      </div>
    </template>
  </NModal>
</template>

<style scoped>
.body {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.intro {
  margin: 0;
  font-size: 12.5px;
  line-height: 1.7;
  color: var(--c-text-2);
}
.center {
  display: grid;
  place-items: center;
  padding: 26px 0;
}

.stats {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 10px;
}
.cell {
  display: flex;
  flex-direction: column;
  gap: 3px;
  padding: 10px 12px;
  border: 1px solid var(--c-border);
  border-radius: var(--radius);
  background: var(--c-bg-base);
}
.k {
  font-size: 11.5px;
  color: var(--c-text-3);
}
.v {
  font-size: 18px;
  font-weight: 600;
  color: var(--c-text-1);
  font-variant-numeric: tabular-nums;
}
.v.ok {
  color: var(--c-success);
}
.v.warn {
  color: var(--c-warning);
}

.root {
  font-size: 12px;
  color: var(--c-text-3);
}
.root code {
  color: var(--c-text-2);
}

.block-title {
  margin-bottom: 7px;
  font-size: 12px;
  color: var(--c-text-3);
}
.libs {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.sample {
  margin: 0;
  padding: 0;
  list-style: none;
  max-height: 210px;
  overflow: auto;
  border: 1px solid var(--c-border);
  border-radius: var(--radius);
}
.sample li {
  display: flex;
  align-items: baseline;
  gap: 8px;
  padding: 7px 11px;
  font-size: 12px;
  border-bottom: 1px solid var(--c-border);
}
.sample li:last-child {
  border-bottom: none;
}
.s-title {
  color: var(--c-text-1);
  flex-shrink: 0;
}
.s-year {
  color: var(--c-text-3);
  flex-shrink: 0;
}
.s-path {
  flex: 1;
  min-width: 0;
  text-align: right;
  color: var(--c-text-3);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
