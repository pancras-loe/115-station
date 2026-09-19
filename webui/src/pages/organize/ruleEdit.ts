/**
 * 规则生成器的落笔部分：把表单拼成 YAML，并插进现有规则的正确位置。
 *
 * 为什么不是「插到光标处」：这两份 YAML 靠缩进表达层级，插错一层就是保存失败，
 * 而分类规则还讲顺序——插到兜底后面等于白写。所以插入位置由结构决定：
 * 分类规则插在对应 movie/tv 段里、兜底之前；洗版策略追加到文末。
 */
import { parseCategory } from './ruleLint'

export interface DraftCond {
  key: string
  value: string
}

export interface CategoryDraft {
  media: 'movie' | 'tv'
  name: string
  conds: DraftCond[]
}

export interface WashDraft {
  name: string
  mode: string
  scope: string
  mediaType: string
  category: string
  oldTarget: string
  levels: DraftCond[][]
}

const cleanConds = (conds: DraftCond[]) =>
  conds.filter(c => c.key && c.value.trim()).map(c => ({ key: c.key, value: c.value.trim() }))

export function categorySnippet(d: CategoryDraft, ruleIndent = 2, condIndent = 4): string {
  const pad = ' '.repeat(ruleIndent)
  const cpad = ' '.repeat(condIndent)
  const lines = [`${pad}${d.name.trim()}:`]
  for (const c of cleanConds(d.conds)) lines.push(`${cpad}${c.key}: '${c.value}'`)
  return lines.join('\n')
}

export function washSnippet(d: WashDraft): string {
  const lines = [`${d.name.trim()}:`]
  const put = (k: string, v: string) => {
    if (v.trim()) lines.push(`  ${k}: ${v.trim()}`)
  }
  put('mode', d.mode)
  put('scope', d.scope)
  put('media_type', d.mediaType)
  put('category', d.category)
  put('old_version_target', d.oldTarget)

  const levels = d.levels.map(cleanConds).filter(l => l.length)
  if (levels.length) {
    lines.push('  priority_level:')
    for (const lv of levels) {
      lv.forEach((c, i) => lines.push(`  ${i === 0 ? '-' : ' '} ${c.key}: "${c.value}"`))
    }
  }
  return lines.join('\n')
}

/** 往上跳过紧贴着这条规则的注释行，免得把「# 未匹配时的兜底」和它的规则劈开 */
function skipLeadingComments(lines: string[], idx: number): number {
  let i = idx
  while (i > 0 && lines[i - 1].trim().startsWith('#')) i--
  return i
}

export function insertCategoryRule(src: string, d: CategoryDraft): string {
  const model = parseCategory(src)
  const section = model.sections.find(s => s.media === d.media)
  const lines = src.split('\n')

  if (!section) {
    const body = [`${d.media}:`, categorySnippet(d)].join('\n')
    const head = src.replace(/\s*$/, '')
    return head ? `${head}\n\n${body}\n` : `${body}\n`
  }

  const snippet = categorySnippet(d, section.ruleIndent, section.condIndent)
  const fallback = section.rules.find(r => r.fallback)
  // 插在兜底之前：兜底匹配一切，排它后面的规则永远轮不到
  const at = fallback ? skipLeadingComments(lines, fallback.line - 1) : section.endLine
  lines.splice(at, 0, ...snippet.split('\n'))
  return lines.join('\n')
}

export function appendWashStrategy(src: string, d: WashDraft): string {
  const head = src.replace(/\s*$/, '')
  const body = washSnippet(d)
  return head ? `${head}\n\n${body}\n` : `${body}\n`
}
