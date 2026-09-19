<script setup lang="ts">
/**
 * 一条重命名规则的编辑器：输入框 + 变量插入面板 + 实时示例。
 *
 * 命名模板是这个页面里最容易写错的东西——变量名一个字母打错，后端会把
 * `{titel}` 原样写进文件名，等发现时整批文件已经改完了。所以这里做三件事：
 * 1) 变量靠选不靠背，按「电影/剧集」「文件夹/文件」过滤掉用不上的；
 * 2) 未知变量当场标红，保存前就能看见；
 * 3) 示例给两行——信息齐全一行、信息缺失一行，<> 块的省略行为一眼可见。
 */
import { computed, nextTick, ref } from 'vue'
import { NButton, NCheckbox, NInput, NPopover, NRadioButton, NRadioGroup, type InputInst } from 'naive-ui'
import FieldRow from '@/components/ui/FieldRow.vue'
import {
  RENAME_VARS_SPARSE,
  RENAME_VAR_GROUPS,
  findUnknownTokens,
  renderRenameExample,
} from '@/utils/rename'

const props = defineProps<{
  label: string
  tip?: string
  /** 电影只展示通用变量，剧集额外放开季集变量 */
  scope: 'movie' | 'tv'
  /** 文件夹规则里 {ext}/{original_name} 没有意义，过滤掉 */
  kind: 'folder' | 'file'
  /** 「恢复默认」写回的内容 */
  fallback: string
}>()

const value = defineModel<string>({ required: true })

const inputRef = ref<InputInst>()
/** 上次光标位置，-1 表示还没定位过 → 插到末尾 */
const caret = ref(-1)

function rememberCaret() {
  // 从 textarea 元素读，不从事件对象读：点到输入框内边距时 event.target
  // 是外层容器，没有 selectionStart，光标会被误判成末尾
  const el = inputRef.value?.textareaElRef
  caret.value = el ? el.selectionStart : -1
}

const query = ref('')
const asBlock = ref(true)
const transform = ref<'raw' | 'dot' | 'lower' | 'upper'>('raw')

const groups = computed(() => {
  const q = query.value.trim().toLowerCase()
  return RENAME_VAR_GROUPS.map(g => ({
    key: g.key,
    title: g.title,
    vars: g.vars.filter(v => {
      if (v.tv && props.scope !== 'tv') return false
      if (v.fileOnly && props.kind !== 'file') return false
      return !q || v.token.toLowerCase().includes(q) || v.label.toLowerCase().includes(q)
    }),
  })).filter(g => g.vars.length > 0)
})

/** 按面板上选的「处理方式 + 可选块」把变量名拼成最终写法 */
function compose(token: string): string {
  const name = token.slice(1, -1)
  let out = token
  if (transform.value === 'dot') out = `{${name}.replace('.', ' ')}`
  else if (transform.value === 'lower') out = `{${name}.lower()}`
  else if (transform.value === 'upper') out = `{${name}.upper()}`
  return asBlock.value ? `<.${out}>` : out
}

function insert(token: string) {
  const text = compose(token)
  const cur = value.value ?? ''
  const at = caret.value < 0 ? cur.length : Math.min(caret.value, cur.length)
  value.value = cur.slice(0, at) + text + cur.slice(at)
  const next = at + text.length
  caret.value = next
  // 面板不关，方便连着插几个；焦点回输入框让用户能直接接着打字
  nextTick(() => {
    const el = inputRef.value?.textareaElRef
    el?.focus()
    el?.setSelectionRange(next, next)
  })
}

const fullExample = computed(() => renderRenameExample(value.value))
const sparseExample = computed(() => renderRenameExample(value.value, RENAME_VARS_SPARSE))
const unknown = computed(() => findUnknownTokens(value.value))
const isDefault = computed(() => value.value.trim() === props.fallback)
</script>

<template>
  <FieldRow :label="label" :tip="tip" wide>
    <div class="editor">
      <NInput
        ref="inputRef"
        v-model:value="value"
        type="textarea"
        :autosize="{ minRows: 2 }"
        :status="unknown.length ? 'error' : undefined"
        spellcheck="false"
        @focus="rememberCaret"
        @click="rememberCaret"
        @keyup="rememberCaret"
        @input="rememberCaret"
      />

      <div class="toolbar">
        <NPopover trigger="click" placement="bottom-start" raw :show-arrow="false">
          <template #trigger>
            <NButton size="tiny" secondary type="primary">插入变量</NButton>
          </template>
          <div class="picker">
            <NInput v-model:value="query" size="small" clearable placeholder="搜索变量名或中文说明" />
            <div class="picker-opts">
              <NRadioGroup v-model:value="transform" size="small">
                <NRadioButton value="raw">原样</NRadioButton>
                <NRadioButton value="dot">点转空格</NRadioButton>
                <NRadioButton value="lower">小写</NRadioButton>
                <NRadioButton value="upper">大写</NRadioButton>
              </NRadioGroup>
              <NCheckbox v-model:checked="asBlock" size="small">
                作为可选块插入，取不到值时整段省略
              </NCheckbox>
            </div>
            <div class="picker-list">
              <template v-for="g in groups" :key="g.key">
                <div class="picker-group">{{ g.title }}</div>
                <button v-for="v in g.vars" :key="v.token" class="picker-item" @click="insert(v.token)">
                  <span class="pi-label">{{ v.label }}</span>
                  <code class="pi-token">{{ compose(v.token) }}</code>
                  <span v-if="v.example" class="pi-example">{{ v.example }}</span>
                </button>
              </template>
              <div v-if="!groups.length" class="picker-empty">没有匹配的变量</div>
            </div>
          </div>
        </NPopover>

        <NButton size="tiny" quaternary :disabled="isDefault" @click="value = fallback">恢复默认</NButton>
      </div>

      <p v-if="unknown.length" class="warn">
        认不出的变量：<code>{{ unknown.join('、') }}</code> —— 会原样写进文件名，请改用面板里的变量。
      </p>

      <div class="examples">
        <div class="example">
          <span class="ex-tag">信息齐全</span>
          <code>{{ fullExample || '（空）' }}</code>
        </div>
        <div v-if="sparseExample !== fullExample" class="example muted">
          <span class="ex-tag">信息缺失</span>
          <code>{{ sparseExample || '（空）' }}</code>
        </div>
      </div>
    </div>
  </FieldRow>
</template>

<style scoped>
.editor {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.warn {
  margin: 0;
  font-size: 11.5px;
  line-height: 1.6;
  color: var(--c-danger);
}
.warn code {
  font-family: var(--font-mono);
}

.examples {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.example {
  display: flex;
  align-items: baseline;
  gap: 8px;
  padding: 5px 10px;
  border-radius: var(--radius-sm);
  background: var(--c-bg-raised);
  font-size: 11.5px;
}
.ex-tag {
  flex-shrink: 0;
  color: var(--c-text-3);
}
.example code {
  font-family: var(--font-mono);
  color: var(--c-primary);
  word-break: break-all;
}
.example.muted .ex-tag,
.example.muted code {
  color: var(--c-text-4);
}

/* 弹层用 raw 模式，样式完全自管 —— naive 的 popover 内边距塞不下搜索框 + 分组列表 */
.picker {
  display: flex;
  flex-direction: column;
  gap: 8px;
  width: 380px;
  max-width: calc(100vw - 24px);
  padding: 10px;
  border: 1px solid var(--c-border);
  border-radius: var(--radius);
  background: var(--c-bg-elevated);
  box-shadow: var(--shadow-card);
}
.picker-opts {
  display: flex;
  flex-direction: column;
  gap: 6px;
  font-size: 11.5px;
  color: var(--c-text-3);
}
.picker-list {
  max-height: min(340px, 50vh);
  overflow-y: auto;
  overscroll-behavior: contain;
  scrollbar-gutter: stable;
}
.picker-group {
  position: sticky;
  top: 0;
  z-index: 1;
  padding: 6px 4px 3px;
  background: var(--c-bg-elevated);
  font-size: 11px;
  font-weight: 600;
  color: var(--c-text-4);
}
.picker-item {
  all: unset;
  display: flex;
  align-items: baseline;
  gap: 8px;
  width: 100%;
  box-sizing: border-box;
  padding: 5px 8px;
  border-radius: var(--radius-sm);
  cursor: pointer;
  font-size: 12px;
  color: var(--c-text-2);
}
.picker-item:hover {
  background: var(--c-primary-soft);
}
.pi-label {
  flex-shrink: 0;
}
.pi-token {
  flex: 1;
  min-width: 0;
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--c-primary);
  overflow-wrap: anywhere;
}
.pi-example {
  flex-shrink: 0;
  max-width: 34%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 11px;
  color: var(--c-text-4);
}
.picker-empty {
  padding: 14px 8px;
  text-align: center;
  font-size: 12px;
  color: var(--c-text-4);
}
</style>
