/**
 * 分类 / 洗版规则 YAML 的体检与大纲。
 *
 * 为什么自己扫而不引 YAML 库：前端打成单文件，为一个低频页面加一个解析器不划算。
 * 这里只做逐行扫描，够用来回答两个问题：
 *   1) 哪些行写了却不会生效（字段名拼错、排在兜底之后、编码写成了未知字段）；
 *   2) 这份规则按顺序读下来到底是什么意思（大纲，把 ID / 代码翻成人话）。
 * 真正的解析权在后端，保存时它会再解析一次并返回带行号的错误。
 */
import {
  CATEGORY_FIELD_KEYS,
  WASH_LEVEL_FIELD_KEYS,
  countryName,
  genreName,
  languageName,
} from './categoryRef'

export type IssueLevel = 'error' | 'warn' | 'info'

export interface RuleIssue {
  /** 1 起的行号，0 表示「整份规则」级别的问题 */
  line: number
  level: IssueLevel
  text: string
}

export interface Cond {
  key: string
  value: string
  line: number
}

// ---------------- 逐行扫描 ----------------

interface Entry {
  line: number
  /** 键所在的列（列表项已把 "- " 算进去，好和续行对齐） */
  indent: number
  key: string
  value: string
  dash: boolean
}

/** '#' 只有在引号外、且前面是行首或空白时才是注释（与 YamlEditor 的着色口径一致） */
function stripComment(line: string): string {
  let quote = ''
  for (let i = 0; i < line.length; i++) {
    const ch = line[i]
    if (quote) {
      if (ch === quote) quote = ''
    } else if (ch === '"' || ch === "'") {
      quote = ch
    } else if (ch === '#' && (i === 0 || /\s/.test(line[i - 1]))) {
      return line.slice(0, i)
    }
  }
  return line
}

function unquote(v: string): string {
  const t = v.trim()
  if (t.length >= 2 && (t[0] === '"' || t[0] === "'") && t[t.length - 1] === t[0]) {
    return t.slice(1, -1)
  }
  return t
}

function scan(src: string): { entries: Entry[]; tabLines: number[] } {
  const entries: Entry[] = []
  const tabLines: number[] = []
  src.split('\n').forEach((raw, i) => {
    const line = i + 1
    if (/^ *\t/.test(raw)) tabLines.push(line)
    const body = stripComment(raw)
    if (!body.trim()) return

    let indent = body.length - body.trimStart().length
    let rest = body.trimStart()
    let dash = false
    if (rest.startsWith('-')) {
      dash = true
      const after = rest.slice(1)
      const spaces = after.length - after.trimStart().length
      indent += 1 + spaces
      rest = after.trimStart()
      if (!rest) return // 单独一个 "-"，没内容可看
    }

    const m = rest.match(/^(.+?):(?:\s+(.*))?$/)
    if (m) {
      entries.push({ line, indent, key: unquote(m[1]), value: unquote(m[2] ?? ''), dash })
    } else {
      entries.push({ line, indent, key: '', value: rest.trim(), dash })
    }
  })
  return { entries, tabLines }
}

// ---------------- 二级分类 ----------------

export interface CategoryRuleView {
  name: string
  line: number
  /** 含条件的最后一行，插入新规则时要知道这条到哪结束 */
  endLine: number
  conds: Cond[]
  /** 没有任何生效条件 → 匹配一切，它就是兜底 */
  fallback: boolean
}

export interface CategorySectionView {
  media: 'movie' | 'tv'
  line: number
  endLine: number
  /** 规则键的缩进，插入时照抄，免得用户的两空格/四空格风格被打乱 */
  ruleIndent: number
  condIndent: number
  rules: CategoryRuleView[]
}

export interface CategoryModel {
  sections: CategorySectionView[]
  unknownTop: Entry[]
}

/** 后端 normalizeCategoryName 的等价物：一级目录名前缀会被剥掉 */
const MEDIA_DIR_NAMES = ['电影', '电视剧', '剧集', 'movie', 'tv']

export function normalizeCategoryName(name: string): string {
  let n = name.trim().replace(/^\/+|\/+$/g, '')
  for (;;) {
    let trimmed = false
    for (const p of MEDIA_DIR_NAMES) {
      if (n === p) return ''
      if (n.startsWith(p + '/')) {
        n = n.slice(p.length + 1)
        trimmed = true
        break
      }
    }
    if (!trimmed) break
  }
  return n
}

export function parseCategory(src: string): CategoryModel {
  const { entries } = scan(src)
  const sections: CategorySectionView[] = []
  const unknownTop: Entry[] = []
  let section: CategorySectionView | null = null
  let rule: CategoryRuleView | null = null

  for (const e of entries) {
    if (e.indent === 0 && !e.dash) {
      rule = null
      if (e.key === 'movie' || e.key === 'tv') {
        section = { media: e.key, line: e.line, endLine: e.line, ruleIndent: -1, condIndent: -1, rules: [] }
        sections.push(section)
      } else {
        section = null
        unknownTop.push(e)
      }
      continue
    }
    if (!section) continue
    section.endLine = e.line

    if (section.ruleIndent < 0) section.ruleIndent = e.indent
    if (e.indent <= section.ruleIndent) {
      rule = { name: e.key, line: e.line, endLine: e.line, conds: [], fallback: true }
      section.rules.push(rule)
      continue
    }
    if (!rule) continue
    if (section.condIndent < 0) section.condIndent = e.indent
    rule.conds.push({ key: e.key, value: e.value, line: e.line })
    rule.endLine = e.line
    if (e.value.trim()) rule.fallback = false
  }

  for (const s of sections) {
    if (s.ruleIndent < 0) s.ruleIndent = 2
    if (s.condIndent < 0) s.condIndent = s.ruleIndent + 2
  }
  return { sections, unknownTop }
}

/** 0 行（整份规则级别）排最前，其余按行号；同一行保持写入顺序 */
function sortIssues(issues: RuleIssue[]): RuleIssue[] {
  return issues.sort((a, b) => (a.line || -1) - (b.line || -1))
}

/**
 * Tab 缩进会让整份结构失真（一个 Tab 只算一列），这时继续推导只会刷出一堆
 * 因错位产生的假警告——先让用户把 Tab 换掉，再谈别的
 */
function tabIssues(src: string): RuleIssue[] {
  return scan(src).tabLines.map(line => ({
    line,
    level: 'error' as const,
    text: 'YAML 不接受 Tab 缩进，请改成空格（这一行会让整份规则解析失败）',
  }))
}

export function lintCategory(src: string): RuleIssue[] {
  const tabs = tabIssues(src)
  if (tabs.length) return tabs
  const issues: RuleIssue[] = []

  const { sections, unknownTop } = parseCategory(src)
  for (const e of unknownTop) {
    issues.push({ line: e.line, level: 'warn', text: `顶层只解析 movie / tv，「${e.key}」及其下面的规则会被整块忽略` })
  }
  if (!sections.length) {
    issues.push({ line: 0, level: 'error', text: '没有 movie / tv 顶层键，保存会被后端拒绝' })
    return sortIssues(issues)
  }

  for (const s of sections) {
    const label = s.media === 'movie' ? '电影' : '剧集'
    if (!s.rules.length) {
      issues.push({ line: s.line, level: 'warn', text: `${label}下没有任何分类规则` })
      continue
    }
    let fallbackAt = -1
    for (const r of s.rules) {
      if (!normalizeCategoryName(r.name)) {
        issues.push({
          line: r.line,
          level: 'info',
          text: `「${r.name}」就是一级目录名本身，这条表示不加二级分类，直接整理到 ${label}/ 这一层`,
        })
      }
      if (fallbackAt >= 0) {
        issues.push({ line: r.line, level: 'warn', text: `排在第 ${fallbackAt} 行的兜底分类之后，永远匹配不到` })
      } else if (r.fallback) {
        fallbackAt = r.line
      }

      for (const c of r.conds) {
        if (!(CATEGORY_FIELD_KEYS as readonly string[]).includes(c.key)) {
          issues.push({
            line: c.line,
            level: 'warn',
            text: `「${c.key}」不是分类条件字段，保存后会被丢弃（可用：${CATEGORY_FIELD_KEYS.join(' / ')}）`,
          })
          continue
        }
        if (!c.value.trim()) {
          issues.push({ line: c.line, level: 'warn', text: `${c.key} 没有值，等于没写这个条件` })
          continue
        }
        if (c.key === 'ext') {
          issues.push({ line: c.line, level: 'info', text: 'ext 当前不参与匹配，只会让这条不再充当兜底' })
        }
        if (c.key === 'genre_ids') {
          for (const v of splitList(c.value)) {
            if (!/^\d+$/.test(v)) {
              issues.push({ line: c.line, level: 'warn', text: `genre_ids 要填数字 ID，「${v}」不是` })
            } else if (!genreName(s.media, v)) {
              issues.push({ line: c.line, level: 'info', text: `类型 ID ${v} 不在${label}常见类型表里，确认下 TMDB 的取值` })
            }
          }
        }
        if (c.key === 'origin_country') {
          for (const v of splitList(c.value)) {
            if (v !== v.toUpperCase()) {
              issues.push({ line: c.line, level: 'warn', text: `国家代码要大写，「${v}」应写成 ${v.toUpperCase()}` })
            } else if (!countryName(v)) {
              issues.push({ line: c.line, level: 'info', text: `国家代码「${v}」不在参考表里` })
            }
          }
        }
        if (c.key === 'original_language') {
          for (const v of splitList(c.value)) {
            if (v !== v.toLowerCase()) {
              issues.push({ line: c.line, level: 'warn', text: `语言代码要小写，「${v}」应写成 ${v.toLowerCase()}` })
            } else if (!languageName(v)) {
              issues.push({ line: c.line, level: 'info', text: `语言代码「${v}」不在参考表里` })
            }
          }
        }
        if (c.key === 'custom_regex') {
          try {
            new RegExp(c.value)
          } catch {
            issues.push({ line: c.line, level: 'error', text: '正则写法有误，整理时这条正则会被忽略' })
          }
        }
      }
    }
    if (fallbackAt < 0) {
      issues.push({ line: s.line, level: 'info', text: `${label}没有兜底分类，匹配不上的会归入「未分类」` })
    }
  }
  return sortIssues(issues)
}

// ---------------- 洗版 ----------------

export interface WashLevelView {
  line: number
  conds: Cond[]
}

export interface WashStrategyView {
  name: string
  line: number
  endLine: number
  fields: Record<string, Cond>
  levels: WashLevelView[]
  hasLevelKey: boolean
}

export function parseWash(src: string): WashStrategyView[] {
  const { entries } = scan(src)
  const list: WashStrategyView[] = []
  let st: WashStrategyView | null = null
  let inLevels = false
  let levelIndent = -1

  for (const e of entries) {
    if (e.indent === 0 && !e.dash) {
      st = { name: e.key, line: e.line, endLine: e.line, fields: {}, levels: [], hasLevelKey: false }
      list.push(st)
      inLevels = false
      levelIndent = -1
      continue
    }
    if (!st) continue
    st.endLine = e.line

    if (e.dash) {
      st.levels.push({ line: e.line, conds: e.key ? [{ key: e.key, value: e.value, line: e.line }] : [] })
      levelIndent = e.indent
      inLevels = true
      continue
    }
    if (inLevels && levelIndent >= 0 && e.indent >= levelIndent && st.levels.length) {
      st.levels[st.levels.length - 1].conds.push({ key: e.key, value: e.value, line: e.line })
      continue
    }
    inLevels = e.key === 'priority_level'
    if (inLevels) st.hasLevelKey = true
    else st.fields[e.key] = { key: e.key, value: e.value, line: e.line }
  }
  return list
}

const WASH_TOP_KEYS = ['mode', 'scope', 'media_type', 'category', 'priority_level', 'old_version_target']
const WASH_MODE_VALUES = ['coexist', 'skip', 'replace', 'max_size', 'min_size']

export function lintWash(src: string): RuleIssue[] {
  const tabs = tabIssues(src)
  if (tabs.length) return tabs
  const issues: RuleIssue[] = []

  const list = parseWash(src)
  if (!list.length) {
    issues.push({ line: 0, level: 'info', text: '还没有任何策略，整理时不会做洗版判定' })
    return sortIssues(issues)
  }

  // 匹配是「第一条命中就用它」：不限分类的策略会吃掉同类媒体的后续策略
  const catchAll: Record<string, WashStrategyView> = {}
  for (const st of list) {
    const mode = st.fields.mode?.value.trim() || 'replace'
    const mediaType = st.fields.media_type?.value.trim() ?? ''
    const category = st.fields.category?.value.trim() ?? ''

    const shadow = catchAll[''] ?? (mediaType ? catchAll[mediaType] : undefined)
    if (shadow) {
      issues.push({
        line: st.line,
        level: 'warn',
        text: `第 ${shadow.line} 行的「${shadow.name}」不限分类、已吃下同类媒体，这条策略永远轮不到`,
      })
    }

    for (const key of Object.keys(st.fields)) {
      if (!WASH_TOP_KEYS.includes(key)) {
        issues.push({
          line: st.fields[key].line,
          level: 'warn',
          text: `「${key}」不是策略字段，保存后会被丢弃（可用：${WASH_TOP_KEYS.join(' / ')}）`,
        })
      }
    }

    const modeCond = st.fields.mode
    if (modeCond && !WASH_MODE_VALUES.includes(mode)) {
      issues.push({ line: modeCond.line, level: 'error', text: `mode 只能是 ${WASH_MODE_VALUES.join(' / ')}` })
    } else if (mode === 'max_size' || mode === 'min_size') {
      issues.push({ line: modeCond?.line ?? st.line, level: 'info', text: `${mode} 目前按 replace 处理，体积比较尚未实现` })
    }

    const scope = st.fields.scope
    if (scope && !['all', 'group'].includes(scope.value.trim())) {
      issues.push({ line: scope.line, level: 'warn', text: 'scope 只认 all / group，其他值按 all 处理' })
    }
    if (mediaType && !['movie', 'tv'].includes(mediaType)) {
      issues.push({ line: st.fields.media_type.line, level: 'warn', text: 'media_type 只认 movie / tv，留空表示不限' })
    }
    const target = st.fields.old_version_target
    if (target) {
      const v = target.value.trim()
      if (!['redundant', 'existing', 'delete'].includes(v)) {
        issues.push({ line: target.line, level: 'warn', text: 'old_version_target 只认 redundant / existing / delete' })
      } else if (v === 'delete') {
        issues.push({ line: target.line, level: 'info', text: 'delete 将旧版移入 115 回收站，同时清理旧 STRM 并通知 Emby' })
      }
    }

    if (!st.levels.length && mode !== 'coexist' && mode !== 'skip') {
      issues.push({ line: st.line, level: 'info', text: mode === 'replace' ? '没有 priority_level：同一影片（剧集同一集）采用新替旧' : '没有 priority_level：此模式跳过洗版' })
    }
    for (let i = 0; i < st.levels.length; i++) {
      const lv = st.levels[i]
      const valid = lv.conds.filter(c => (WASH_LEVEL_FIELD_KEYS as readonly string[]).includes(c.key) && c.value.trim())
      for (const c of lv.conds) {
        if (!(WASH_LEVEL_FIELD_KEYS as readonly string[]).includes(c.key)) {
          issues.push({
            line: c.line,
            level: 'warn',
            text: `「${c.key}」不是优先级字段，保存后会被丢弃（可用：${WASH_LEVEL_FIELD_KEYS.join(' / ')}）`,
          })
        }
      }
      if (!valid.length) {
        issues.push({ line: lv.line, level: 'warn', text: `第 ${i + 1} 条优先级没有有效条件，等于匹配所有版本` })
      }
    }
    if (!category && !catchAll[mediaType]) catchAll[mediaType] = st
  }
  return sortIssues(issues)
}

// ---------------- 大纲 ----------------

export interface OutlineItem {
  line: number
  label: string
  tags: string[]
  chips: string[]
  /** 永远匹配不到的规则压暗显示 */
  muted?: boolean
}

export interface OutlineGroup {
  title: string
  subtitle?: string
  line: number
  items: OutlineItem[]
}

function splitList(v: string): string[] {
  return v
    .split(',')
    .map(s => s.trim())
    .filter(Boolean)
}

/** 把 ID / 代码翻成「人话(原值)」，认不出就只留原值 */
function decode(key: string, value: string, media: string): string {
  const names: Record<string, (v: string) => string> = {
    genre_ids: v => genreName(media, v),
    origin_country: v => countryName(v),
    original_language: v => languageName(v),
  }
  const fn = names[key]
  if (!fn) return value
  return splitList(value)
    .map(v => {
      const name = fn(v)
      return name ? `${name}(${v})` : v
    })
    .join('、')
}

const CATEGORY_COND_LABEL: Record<string, string> = {
  genre_ids: '类型',
  original_language: '语言',
  origin_country: '国家',
  custom_regex: '片名正则',
  ext: '后缀',
}

export function outlineCategory(src: string): OutlineGroup[] {
  return parseCategory(src).sections.map(s => {
    const top = s.media === 'movie' ? '电影' : '剧集'
    let seenFallback = false
    return {
      title: top,
      subtitle: `${s.rules.length} 条规则，从上往下匹配`,
      line: s.line,
      items: s.rules.map(r => {
        const muted = seenFallback
        if (r.fallback) seenFallback = true
        const name = normalizeCategoryName(r.name)
        return {
          line: r.line,
          label: name || `${top}/`,
          tags: [...(r.fallback ? ['兜底'] : []), ...(name ? [] : ['不加二级分类'])],
          chips: r.conds
            .filter(c => c.value.trim())
            .map(c => `${CATEGORY_COND_LABEL[c.key] ?? c.key}：${decode(c.key, c.value, s.media)}`),
          muted,
        }
      }),
    }
  })
}

const WASH_COND_LABEL: Record<string, string> = {
  resource_pix: '分辨率',
  resource_type: '质量',
  resource_effect: '特效',
  video_encode: '视频编码',
  audio_encode: '音频编码',
  resource_team: '发布组',
}

const MODE_LABEL: Record<string, string> = {
  coexist: '共存',
  skip: '跳过',
  replace: '替换',
  max_size: '替换（体积未实现）',
  min_size: '替换（体积未实现）',
}

export function outlineWash(src: string): OutlineGroup[] {
  return parseWash(src).map(st => {
    const mode = st.fields.mode?.value.trim() || 'replace'
    const mediaType = st.fields.media_type?.value.trim() ?? ''
    const category = st.fields.category?.value.trim() ?? ''
    const scope = st.fields.scope?.value.trim() || 'all'
    const target = st.fields.old_version_target?.value.trim() || 'redundant'
    const bits = [
      `${MODE_LABEL[mode] ?? mode}`,
      mediaType === 'movie' ? '仅电影' : mediaType === 'tv' ? '仅剧集' : '电影 + 剧集',
      scope === 'group' ? '按分辨率各留一个' : '全局只留一个',
      `旧版去 ${target === 'delete' ? '115 回收站' : target === 'existing' ? '已存在' : '冗余'}`,
    ]
    if (category) bits.push(`限分类 ${category}`)
    if (mode === 'replace' && !st.levels.length) bits.push('无优先级，新替旧')
    return {
      title: st.name,
      subtitle: bits.join(' · '),
      line: st.line,
      items: st.levels.map((lv, i) => ({
        line: lv.line,
        label: `第 ${i + 1} 优先`,
        tags: [],
        chips: lv.conds
          .filter(c => c.value.trim())
          .map(c => `${WASH_COND_LABEL[c.key] ?? c.key}：${c.value}`),
      })),
    }
  })
}
