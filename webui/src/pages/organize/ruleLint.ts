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
  CATEGORY_SERIES_KEYS,
  CATEGORY_SETTING_KEYS,
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
  /** 写成 ?字段 的可选条件（只有分类规则有；key 里已去掉 ?） */
  optional?: boolean
}

/** 分类字段名去掉可选前缀 ? / ？（后端 catSplitKey） */
function splitOptKey(raw: string): { key: string; optional: boolean } {
  const m = raw.trim().match(/^[?？]\s*(.*)$/)
  return m ? { key: m[1], optional: true } : { key: raw.trim(), optional: false }
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
  /** library：这一类整理进哪个媒体库（不是匹配条件，不进 conds） */
  library?: Cond
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

/**
 * 后端 libSubPath(name) 的等价物：分类名就是库内目录名（多级用 / 分隔），
 * 只做去空白/去首尾斜杠。**不剥「电影/」「剧集/」前缀** —— 写什么就落到哪，
 * 剥掉再由后端补一层会把平铺写法（tv 下并列 动漫番剧/综艺/剧集）
 * 整理成 剧集/动漫番剧
 */
export function categoryDirName(name: string): string {
  return name.trim().replace(/^\/+|\/+$/g, '')
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
    // 块状列表（genre_ids: 换行 - 16 - 99）并进上一个条件，后端同样按逗号拼起来
    const last = rule.conds[rule.conds.length - 1]
    if (e.dash && !e.key && last) {
      last.value = last.value.trim() ? `${last.value},${e.value}` : e.value
      rule.endLine = e.line
      if (e.value.trim()) rule.fallback = false
      continue
    }
    if (section.condIndent < 0) section.condIndent = e.indent
    const { key, optional } = splitOptKey(e.key)
    if ((CATEGORY_SETTING_KEYS as readonly string[]).includes(key)) {
      rule.library = { key, value: e.value, line: e.line } // 不是条件：不影响兜底判断
      rule.endLine = e.line
      continue
    }
    rule.conds.push({ key, value: e.value, line: e.line, optional })
    rule.endLine = e.line
    // 认不出的条件后端也不当兜底（这条规则不匹配任何条目），这里同口径
    if (e.value.trim() || !(CATEGORY_FIELD_KEYS as readonly string[]).includes(key)) rule.fallback = false
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
    let lastReal = -1
    s.rules.forEach((r, i) => {
      if (!r.fallback) lastReal = i
    })
    for (const [ri, r] of s.rules.entries()) {
      if (!categoryDirName(r.name)) {
        issues.push({
          line: r.line,
          level: 'info',
          text: `分类名为空，这条会整理到 ${label}/ 这一层`,
        })
      }
      if (r.fallback) {
        if (fallbackAt < 0) fallbackAt = r.line
        // 后端 catMoveFallbacks：保存时没写条件的挪到这一段最后，不会再吞掉后面的规则
        if (ri < lastReal) issues.push({ line: r.line, level: 'info', text: '没写条件的兜底，保存时会自动挪到这一段最后' })
      }

      if (r.library && !r.library.value.trim()) {
        issues.push({ line: r.library.line, level: 'warn', text: 'library 没有值，这一类照常进主媒体库' })
      }
      const seenKeys = new Set<string>()
      for (const c of r.conds) {
        // 以下 error 级别的都与后端 catValidateYAML 同口径：保存会被拒收
        if (c.key === 'year') {
          issues.push({ line: c.line, level: 'error', text: 'year 已不用了，改成 release_year（写法 2020 或 2000-2009）' })
          continue
        }
        if (!(CATEGORY_FIELD_KEYS as readonly string[]).includes(c.key)) {
          issues.push({
            line: c.line,
            level: 'error',
            text: `「${c.key}」不是分类条件字段，保存会被拒收（可用：${CATEGORY_FIELD_KEYS.join(' / ')}）`,
          })
          continue
        }
        if (seenKeys.has(c.key)) {
          issues.push({ line: c.line, level: 'error', text: `${c.key} 写了两次（带不带 ? 都算同一个），合成一行` })
          continue
        }
        seenKeys.add(c.key)
        if (!c.value.trim()) {
          issues.push({ line: c.line, level: 'error', text: `${c.key} 没有值，不要这个条件就把这一行删掉` })
          continue
        }
        if (c.optional && (CATEGORY_SERIES_KEYS as readonly string[]).includes(c.key)) {
          issues.push({ line: c.line, level: 'info', text: '系列字段带 ? 没有效果，照常按系列算' })
        }
        if (c.key !== 'custom_regex' && c.key !== 'ext' && splitTerms(c.value, true).some((t) => !t.v)) {
          issues.push({ line: c.line, level: 'error', text: `${c.key} 里「+」「-」「!」后面要跟值` })
          continue
        }
        if (c.key === 'ext') {
          const others = r.conds.some(o => o.key !== 'ext' && o.value.trim())
          issues.push({
            line: c.line,
            level: others ? 'info' : 'warn',
            text: others ? 'ext 当前不参与匹配，按其他条件判断' : 'ext 当前不参与匹配：只写了 ext 的规则不会匹配任何条目',
          })
        }
        if (c.key === 'genre_ids') {
          for (const { v } of splitTerms(c.value)) {
            const ids = v.split('-').map(x => x.trim())
            if (!ids.every(x => /^\d+$/.test(x))) {
              issues.push({ line: c.line, level: 'warn', text: `genre_ids 要填数字 ID 或范围，「${v}」不是` })
            } else if (ids.length === 1 && !genreName(s.media, v)) {
              issues.push({ line: c.line, level: 'info', text: `类型 ID ${v} 不在${label}类型表里，确认下 TMDB 的取值` })
            }
          }
        }
        if (c.key === 'origin_country' || c.key === 'production_countries') {
          for (const { v } of splitTerms(c.value)) {
            if (!v.includes('-') && !countryName(v.toUpperCase())) {
              issues.push({ line: c.line, level: 'info', text: `国家代码「${v}」不在参考表里` })
            }
          }
        }
        if (c.key === 'original_language') {
          for (const { v } of splitTerms(c.value)) {
            if (!v.includes('-') && !languageName(v.toLowerCase())) {
              issues.push({ line: c.line, level: 'info', text: `语言代码「${v}」不在参考表里` })
            }
          }
        }
        if (c.key === 'release_year') {
          for (const { v } of splitTerms(c.value)) {
            if (!/^\d{4}(\s*-\s*\d{4})?$/.test(v)) {
              issues.push({ line: c.line, level: 'error', text: `release_year 要写四位年份或范围（2000-2009），「${v}」不是` })
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
  /** prefer 下的各项（画质偏好，简单设置那一套）；没写 prefer 为 null */
  prefer: Record<string, Cond> | null
}

export function parseWash(src: string): WashStrategyView[] {
  const { entries } = scan(src)
  const list: WashStrategyView[] = []
  let st: WashStrategyView | null = null
  let inLevels = false
  let levelIndent = -1
  let preferIndent = -1

  for (const e of entries) {
    if (e.indent === 0 && !e.dash) {
      st = { name: e.key, line: e.line, endLine: e.line, fields: {}, levels: [], hasLevelKey: false, prefer: null }
      list.push(st)
      inLevels = false
      levelIndent = -1
      preferIndent = -1
      continue
    }
    if (!st) continue
    st.endLine = e.line

    if (preferIndent >= 0) {
      if (e.indent > preferIndent && !e.dash) {
        st.prefer![e.key] = { key: e.key, value: e.value, line: e.line }
        continue
      }
      preferIndent = -1
    }
    if (!e.dash && e.key === 'prefer') {
      st.prefer = {}
      st.fields.prefer = { key: e.key, value: e.value, line: e.line }
      preferIndent = e.indent
      inLevels = false
      continue
    }

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

const WASH_TOP_KEYS = ['mode', 'scope', 'media_type', 'category', 'priority_level', 'old_version_target', 'prefer']
const WASH_PREFER_KEYS = ['max_pix', 'dv', 'size', 'avoid', 'avoid_words', 'teams']
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
      issues.push({ line: modeCond?.line ?? st.line, level: 'info', text: `${mode} 按 replace 处理，画质命中同一条时留体积${mode === 'max_size' ? '大' : '小'}的` })
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

    if (st.prefer) {
      for (const c of Object.values(st.prefer)) {
        const v = unquote(c.value)
        if (!WASH_PREFER_KEYS.includes(c.key)) {
          issues.push({ line: c.line, level: 'warn', text: `「${c.key}」不是 prefer 的字段（可用：${WASH_PREFER_KEYS.join(' / ')}）` })
        } else if (c.key === 'dv' && v && !['prefer', 'any', 'avoid'].includes(v)) {
          issues.push({ line: c.line, level: 'warn', text: 'dv 只认 prefer / any / avoid，其他值按 any' })
        } else if (c.key === 'size' && v && !['small', 'large', 'any'].includes(v)) {
          issues.push({ line: c.line, level: 'warn', text: 'size 只认 small / large，其他值不看体积' })
        } else if (c.key === 'max_pix' && v && !['1080p', '1080'].includes(v.toLowerCase())) {
          issues.push({ line: c.line, level: 'warn', text: 'max_pix 只认 1080p，不写 = 越高越好' })
        }
      }
      if (st.levels.length) {
        issues.push({ line: st.levels[0].line, level: 'warn', text: '写了 prefer，priority_level 不再生效' })
      }
    } else if (!st.levels.length && mode !== 'coexist' && mode !== 'skip') {
      issues.push({ line: st.line, level: 'info', text: mode === 'replace' ? '没有 priority_level：按默认画质比（分辨率 → 片源 → HDR → 视频编码），新版更好才替换' : '没有 priority_level：此模式跳过洗版' })
    }
    for (let i = 0; i < st.levels.length; i++) {
      const lv = st.levels[i]
      // size_prefer 是比法不是条件，只写了它的一条照样匹配所有版本
      const valid = lv.conds.filter(
        c => (WASH_LEVEL_FIELD_KEYS as readonly string[]).includes(c.key) && c.key !== 'size_prefer' && c.value.trim(),
      )
      for (const c of lv.conds) {
        if (!(WASH_LEVEL_FIELD_KEYS as readonly string[]).includes(c.key)) {
          issues.push({
            line: c.line,
            level: 'warn',
            text: `「${c.key}」不是优先级字段，保存后会被丢弃（可用：${WASH_LEVEL_FIELD_KEYS.join(' / ')}）`,
          })
        }
        const v = unquote(c.value)
        if (c.key === 'size_prefer' && v && v !== 'small' && v !== 'large') {
          issues.push({ line: c.line, level: 'warn', text: 'size_prefer 只认 small / large，其他值不比体积' })
        }
        if (c.key === 'resource_size' && v) {
          const bad = v.split(',').map(s => s.trim()).filter(s => s && !SIZE_RANGE_RE.test(s))
          if (bad.length) {
            issues.push({
              line: c.line,
              level: 'warn',
              text: `体积条件写法认不出：${bad.join('、')}（这几段永远命中不了）；写成 <30G、>=5G 或 5G-30G`,
            })
          }
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

// 与后端 washquality.go 的 parseWashSizeRange 同口径：<30G / >=5G / 5G-30G / 5-30G（单位 T G M，不写按 G）
const SIZE_NUM = String.raw`\d+(?:\.\d+)?\s*(?:[TGM]i?B?)?`
const SIZE_RANGE_RE = new RegExp(String.raw`^(?:(?:<=|>=|<|>)\s*${SIZE_NUM}|${SIZE_NUM}\s*-\s*${SIZE_NUM})$`, 'i')

// ---------------- 大纲 ----------------

export interface OutlineItem {
  line: number
  label: string
  tags: string[]
  chips: string[]
  /** 永远匹配不到的规则压暗显示 */
  muted?: boolean
  /** 落进哪个库（多个媒体库时才有，ruleDest.ts）。default = 没写 library、落在默认库；bad = library 认不出 */
  dest?: { text: string; tone: 'default' | 'set' | 'bad'; hint?: string }
}

export interface OutlineGroup {
  title: string
  subtitle?: string
  line: number
  items: OutlineItem[]
}

function splitList(v: string): string[] {
  return v
    .replace(/^\s*\[|\]\s*$/g, '') // 行内列表 [16, 99]，后端同样按逗号拼
    .split(/[,，;；]/) // 与后端 catTerms 同一组分隔符
    .map(s => s.trim().replace(/^(['"])(.*)\1$/, '$2'))
    .filter(Boolean)
}

/**
 * 拆成单个取值并剥掉前缀：- / ! 排除、+ 必须（范围 a-b 原样保留，开头的 - 才是排除）。
 * keepEmpty：前缀后面没值的也留着（体检要报出来）
 */
function splitTerms(v: string, keepEmpty = false): { v: string; neg: boolean; must: boolean }[] {
  return splitList(v)
    .map((t) => {
      const neg = /^[-!！]/.test(t)
      const must = t.startsWith('+')
      return { v: neg || must ? t.replace(/^[-!！+]/, '').trim() : t, neg, must }
    })
    .filter((t) => keepEmpty || t.v)
}

/** 把 ID / 代码翻成「人话(原值)」，认不出就只留原值 */
function decode(key: string, value: string, media: string): string {
  const names: Record<string, (v: string) => string> = {
    genre_ids: v => genreName(media, v),
    origin_country: v => countryName(v.toUpperCase()),
    production_countries: v => countryName(v.toUpperCase()),
    original_language: v => languageName(v.toLowerCase()),
    release_year: () => '',
  }
  const fn = names[key]
  if (!fn) return value
  return splitTerms(value)
    .map(({ v, neg, must }) => {
      const name = v.includes('-') ? '' : fn(v)
      const text = name ? `${name}(${v})` : v
      return neg ? `非 ${text}` : must ? `必须 ${text}` : text
    })
    .join('、')
}

const CATEGORY_COND_LABEL: Record<string, string> = {
  genre_ids: '类型',
  original_language: '语言',
  origin_country: '国家',
  production_countries: '制片国家',
  release_year: '年份',
  keywords: '片名含',
  include_keywords: 'TMDB 标签',
  series_keywords: '系列（片名）',
  series_actors: '系列（演员）',
  series_directors: '系列（导演）',
  custom_regex: '片名正则',
  ext: '后缀',
}

export function outlineCategory(src: string): OutlineGroup[] {
  return parseCategory(src).sections.map(s => {
    const top = s.media === 'movie' ? '电影' : '剧集'
    let lastReal = -1
    s.rules.forEach((r, i) => {
      if (!r.fallback) lastReal = i
    })
    return {
      title: top,
      subtitle: `${s.rules.length} 条规则，从上往下匹配`,
      line: s.line,
      items: s.rules.map((r, ri) => {
        const name = categoryDirName(r.name)
        // 与后端 matchCategory 同口径：有认不出的条件，或者只写了 ext
        const set = r.conds.filter(c => c.value.trim())
        const dead =
          r.conds.some(c => !(CATEGORY_FIELD_KEYS as readonly string[]).includes(c.key)) ||
          (set.length > 0 && set.every(c => c.key === 'ext'))
        return {
          line: r.line,
          label: name ? `${name}/` : `${top}/`,
          tags: [
            // 兜底保存时会挪到最后（后端 catMoveFallbacks），不再把后面的规则灰掉
            ...(r.fallback ? [ri < lastReal ? '兜底（保存时挪到最后）' : '兜底'] : []),
            ...(r.conds.some((c) => (CATEGORY_SERIES_KEYS as readonly string[]).includes(c.key) && c.value.trim())
              ? ['加系列子目录']
              : []),
            ...(name ? [] : ['无分类目录']),
            ...(dead ? ['不会匹配'] : []),
            ...(r.library?.value.trim() ? [`→ ${r.library.value.trim().replace(/^['"]|['"]$/g, '')} 库`] : []),
          ],
          chips: r.conds
            .filter(c => c.value.trim())
            .map(c => `${c.optional ? '可选·' : ''}${CATEGORY_COND_LABEL[c.key] ?? c.key}：${decode(c.key, c.value, s.media)}`),
          muted: dead,
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
  resource_size: '体积',
  size_prefer: '体积偏好',
}

const MODE_LABEL: Record<string, string> = {
  coexist: '共存',
  skip: '跳过',
  replace: '替换',
  max_size: '替换（留大的）',
  min_size: '替换（留小的）',
}

const PREFER_LABEL: Record<string, string> = {
  max_pix: '分辨率封顶',
  dv: '杜比视界',
  size: '体积',
  avoid: '不要',
  avoid_words: '不要的关键词',
  teams: '偏好发布组',
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
    if (st.prefer) bits.push('画质偏好')
    else if (mode === 'replace' && !st.levels.length) bits.push('无优先级，按默认画质比')
    if (st.prefer) {
      return {
        title: st.name,
        subtitle: bits.join(' · '),
        line: st.line,
        items: Object.values(st.prefer)
          .filter(c => c.value.trim())
          .map(c => ({ line: c.line, label: PREFER_LABEL[c.key] ?? c.key, tags: [], chips: [unquote(c.value)] })),
      }
    }
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
