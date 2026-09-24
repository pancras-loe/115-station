<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'

/**
 * 零依赖 YAML 编辑器：透明 textarea 叠在高亮层上，左侧行号槽。
 *
 * 为什么不用 CodeMirror：本项目前端打成单文件是为了在跨境明文链路上「一次请求
 * 拿完整个前端」。引入 CM6 会让包体涨三成，而旧版正是为了避开这个代价才把
 * CM5 做成「面板可见才懒加载」。规则文件编辑是低频操作，一个高亮 + 行号 +
 * Tab 缩进的文本域足够，换来的是首屏体积不被拖累。
 */
export interface EditorMarker {
  /** 1 起的行号 */
  line: number
  level: 'error' | 'warn' | 'info'
  text: string
}

const props = defineProps<{ modelValue: string; rows?: number; markers?: EditorMarker[] }>()
const emit = defineEmits<{ 'update:modelValue': [string] }>()

const ta = ref<HTMLTextAreaElement | null>(null)
const scroller = ref<HTMLElement | null>(null)
const gutter = ref<HTMLElement | null>(null)

const lines = computed(() => props.modelValue.split('\n'))

/** 行号槽上的体检标记：同一行多条时取最重的一档着色，提示文案全挂在 title 上 */
const RANK = { info: 0, warn: 1, error: 2 }
const marks = computed(() => {
  const map: Record<number, { level: 'error' | 'warn' | 'info'; texts: string[] }> = {}
  for (const m of props.markers ?? []) {
    if (m.line <= 0) continue
    const cur = map[m.line]
    if (!cur) map[m.line] = { level: m.level, texts: [m.text] }
    else {
      cur.texts.push(m.text)
      if (RANK[m.level] > RANK[cur.level]) cur.level = m.level
    }
  }
  return map
})

/** 极简 YAML 着色：注释 / 键 / 字符串 / 数字与布尔。够用即可，不追求完备解析 */
function highlight(line: string): string {
  const esc = (s: string) =>
    s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')

  const comment = line.indexOf('#')
  // '#' 只有在引号外才是注释，这里按「前面有偶数个引号」粗判
  if (comment >= 0) {
    const before = line.slice(0, comment)
    const quotes = (before.match(/['"]/g) ?? []).length
    if (quotes % 2 === 0) {
      return `${highlight(before)}<span class="c">${esc(line.slice(comment))}</span>`
    }
  }

  const m = line.match(/^(\s*-?\s*)([^:\s][^:]*?)(\s*:)(\s*)(.*)$/)
  if (m) {
    const [, indent, key, colon, space, rest] = m
    return `${esc(indent)}<span class="k">${esc(key)}</span>${esc(colon)}${esc(space)}${value(rest)}`
  }
  return value(line)
}

function value(v: string): string {
  const esc = (s: string) =>
    s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
  if (!v) return ''
  if (/^['"].*['"]$/.test(v.trim())) return `<span class="s">${esc(v)}</span>`
  if (/^(true|false|null|~)$/i.test(v.trim())) return `<span class="b">${esc(v)}</span>`
  if (/^-?\d+(\.\d+)?$/.test(v.trim())) return `<span class="n">${esc(v)}</span>`
  return esc(v)
}

const html = computed(() => lines.value.map(highlight).join('\n') + '\n')

/** Tab 插两个空格而不是切走焦点——YAML 全靠缩进，跳焦点在这里毫无用处 */
function onKeydown(e: KeyboardEvent) {
  if (e.key !== 'Tab') return
  e.preventDefault()
  const el = ta.value
  if (!el) return
  const { selectionStart: s, selectionEnd: en } = el
  const next = props.modelValue.slice(0, s) + '  ' + props.modelValue.slice(en)
  emit('update:modelValue', next)
  nextTick(() => el.setSelectionRange(s + 2, s + 2))
}

// 高亮层与行号槽都不自己滚，靠 textarea 的滚动位置同步，三层才不会错位
function onScroll() {
  const el = ta.value
  const box = scroller.value
  if (!el || !box) return
  box.scrollTop = el.scrollTop
  box.scrollLeft = el.scrollLeft
  if (gutter.value) gutter.value.scrollTop = el.scrollTop
}

watch(() => props.modelValue, () => nextTick(onScroll))

/** 体检结果点一下跳到对应行：光标落到行首并把它滚到视野中间 */
function focusLine(line: number) {
  const el = ta.value
  if (!el) return
  const at = lines.value.slice(0, Math.max(0, line - 1)).reduce((n, l) => n + l.length + 1, 0)
  el.focus()
  el.setSelectionRange(at, at + (lines.value[line - 1]?.length ?? 0))
  const lh = parseFloat(getComputedStyle(el).lineHeight) || 21
  el.scrollTop = Math.max(0, (line - 1) * lh - el.clientHeight / 2)
  onScroll()
}

defineExpose({ focusLine })
</script>

<template>
  <div class="editor" :style="{ '--rows': String(rows ?? 24) }">
    <div ref="gutter" class="gutter" aria-hidden="true">
      <span
        v-for="(_, i) in lines"
        :key="i"
        :class="marks[i + 1]?.level"
        :title="marks[i + 1]?.texts.join('\n')"
      >{{ i + 1 }}</span>
    </div>

    <div class="pane">
      <pre ref="scroller" class="hl" aria-hidden="true"><code v-html="html" /></pre>
      <textarea
        ref="ta"
        class="ye-input"
        spellcheck="false"
        autocapitalize="off"
        autocomplete="off"
        wrap="off"
        :value="modelValue"
        @input="emit('update:modelValue', ($event.target as HTMLTextAreaElement).value)"
        @keydown="onKeydown"
        @scroll="onScroll"
      />
    </div>
  </div>
</template>

<style scoped>
.editor {
  display: flex;
  border: 1px solid var(--c-border);
  border-radius: var(--radius);
  background: var(--c-bg-raised);
  overflow: hidden;
  font-family: var(--font-mono);
  font-size: 12.5px;
  line-height: 1.7;
}
.editor:focus-within {
  border-color: var(--c-primary);
}

.gutter {
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  padding: 10px 8px 10px 12px;
  text-align: right;
  color: var(--c-text-4);
  background: var(--c-bg-elevated);
  border-right: 1px solid var(--c-border);
  user-select: none;
  max-height: calc(var(--rows) * 1.7em + 20px);
  overflow: hidden;
}
/* 有体检提示的行号点亮，hover 出 title —— 错在哪一行不用自己数 */
.gutter span.error {
  color: var(--c-danger);
  font-weight: 700;
}
.gutter span.warn {
  color: var(--c-warning);
  font-weight: 700;
}
.gutter span.info {
  color: var(--c-primary);
}

.pane {
  position: relative;
  flex: 1;
  min-width: 0;
  height: calc(var(--rows) * 1.7em + 20px);
}

/* 高亮层与输入层必须字体、行高、内边距完全一致，否则字符会错位 */
.hl,
.ye-input {
  margin: 0;
  padding: 10px 12px;
  font: inherit;
  line-height: inherit;
  white-space: pre;
  tab-size: 2;
  border: 0;
}

.hl {
  position: absolute;
  inset: 0;
  overflow: hidden;
  pointer-events: none;
  color: var(--c-text-1);
}

.ye-input {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  resize: none;
  overflow: auto;
  background: transparent;
  /* 文字透明、光标可见：真正显示的是底下的高亮层 */
  color: transparent;
  caret-color: var(--c-text-1);
  outline: none;
}
.ye-input::selection {
  background: color-mix(in srgb, var(--c-primary) 30%, transparent);
}

.hl :deep(.c) {
  color: var(--c-text-3);
  font-style: italic;
}
.hl :deep(.k) {
  color: var(--c-primary);
}
.hl :deep(.s) {
  color: var(--c-success);
}
.hl :deep(.n) {
  color: var(--c-warning);
}
.hl :deep(.b) {
  color: var(--c-danger);
}
</style>
