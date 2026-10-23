/**
 * 二级分类简单模式的前端部分（2026-10-23，后端 catsimple.go）。
 *
 * 表单里的条件值是 YAML 原样的逗号串（'16,99'、'CN,TW,HK'）。这里只负责把常用的几组值做成点选标签，
 * 标签表示不了的值（其他国家代码、!CN 这种排除）原样留在「其他」里，所以来回转不丢东西。
 * 判定（会进哪个目录）一律问后端（/category/schemes/preview），前端不另算一份。
 */
import type { CatSimpleForm, CatSimpleRule } from '@/api/library'
import { countryName, genreName, languageName } from './categoryRef'

export type Media = 'movie' | 'tv'

/** 一个点选标签 = 一组值，全在才算选中 */
export interface CondChip {
  label: string
  codes: string[]
}

export const GENRE_CHIPS: Record<Media, CondChip[]> = {
  movie: [
    { label: '动画', codes: ['16'] },
    { label: '纪录片', codes: ['99'] },
    { label: '音乐 / 演唱会', codes: ['10402'] },
    { label: '家庭', codes: ['10751'] },
    { label: '科幻', codes: ['878'] },
    { label: '恐怖', codes: ['27'] },
  ],
  tv: [
    { label: '动画', codes: ['16'] },
    { label: '纪录片', codes: ['99'] },
    { label: '综艺', codes: ['10764', '10767'] },
    { label: '儿童', codes: ['10762'] },
    { label: '科幻奇幻', codes: ['10765'] },
  ],
}

const WEST = ['US', 'GB', 'FR', 'DE', 'ES', 'IT', 'NL', 'PT', 'RU']

export const REGION_CHIPS: CondChip[] = [
  { label: '华语', codes: ['CN', 'TW', 'HK'] },
  { label: '日本', codes: ['JP'] },
  { label: '韩国', codes: ['KR', 'KP'] },
  { label: '欧美', codes: WEST },
  { label: '泰国', codes: ['TH'] },
  { label: '印度', codes: ['IN'] },
  { label: '新加坡', codes: ['SG'] },
]

export const LANG_CHIPS: CondChip[] = [
  { label: '中文（含粤语）', codes: ['zh', 'cn', 'bo', 'za'] },
  { label: '日语', codes: ['ja'] },
  { label: '韩语', codes: ['ko'] },
  { label: '英语', codes: ['en'] },
]

export const splitTokens = (v: string) =>
  v
    .split(/[,，]/)
    .map((s) => s.trim())
    .filter(Boolean)

const same = (a: string, b: string) => a.toUpperCase() === b.toUpperCase()
const has = (tokens: string[], code: string) => tokens.some((t) => same(t, code))

export const chipOn = (value: string, chip: CondChip) => {
  const t = splitTokens(value)
  return chip.codes.every((c) => has(t, c))
}

/** 点一下标签：选中就去掉这组值，没选中就补齐（原有的先后不动） */
export function toggleChip(value: string, chip: CondChip): string {
  const t = splitTokens(value)
  if (chip.codes.every((c) => has(t, c))) return t.filter((x) => !chip.codes.some((c) => same(c, x))).join(',')
  return [...t, ...chip.codes.filter((c) => !has(t, c))].join(',')
}

/** 选中的标签盖不住的那些值（其他代码、排除写法） */
export function leftoverOf(value: string, chips: CondChip[]): string[] {
  const t = splitTokens(value)
  const covered = chips.filter((ch) => ch.codes.every((c) => has(t, c))).flatMap((ch) => ch.codes)
  return t.filter((x) => !covered.some((c) => same(c, x)))
}

/** 改「其他」：标签盖住的照留，其余换成新的 */
export function setLeftover(value: string, chips: CondChip[], rest: string[]): string {
  const t = splitTokens(value)
  const covered = chips.filter((ch) => ch.codes.every((c) => has(t, c))).flatMap((ch) => ch.codes)
  const keep = t.filter((x) => covered.some((c) => same(c, x)))
  return [...keep, ...rest.filter((x) => !keep.some((k) => same(k, x)))].join(',')
}

export const emptyRule = (name = ''): CatSimpleRule => ({
  name,
  genre_ids: '',
  origin_country: '',
  original_language: '',
  production_countries: '',
  release_year: '',
  custom_regex: '',
  ext: '',
})

export const ruleHasCond = (r: CatSimpleRule) =>
  [r.genre_ids, r.origin_country, r.original_language, r.production_countries, r.release_year, r.custom_regex, r.ext].some(
    (v) => v.trim() !== '',
  )

/** 给人看的一个值：TMDB 代码翻成中文，!X 写成「排除 X」 */
function tokenText(field: 'genre' | 'country' | 'lang', media: Media, tok: string): string {
  const neg = tok.startsWith('!')
  const code = neg ? tok.slice(1).trim() : tok
  const name =
    field === 'genre' ? genreName(media, code) : field === 'country' ? countryName(code.toUpperCase()) : languageName(code)
  const text = name ? name.replace(/（.*）$/, '') : code
  return neg ? `排除${text}` : text
}

function describe(value: string, chips: CondChip[], field: 'genre' | 'country' | 'lang', media: Media): string {
  const parts = chips.filter((ch) => chipOn(value, ch)).map((ch) => ch.label)
  parts.push(...leftoverOf(value, chips).map((t) => tokenText(field, media, t)))
  return parts.join(' / ')
}

/** 一条分类的条件摘要（不在编辑时显示成一串小标签） */
export function ruleSummary(r: CatSimpleRule, media: Media): string[] {
  const out: string[] = []
  if (r.genre_ids.trim()) out.push(`类型 ${describe(r.genre_ids, GENRE_CHIPS[media], 'genre', media)}`)
  if (r.origin_country.trim()) out.push(`地区 ${describe(r.origin_country, REGION_CHIPS, 'country', media)}`)
  if (r.original_language.trim()) out.push(`原语言 ${describe(r.original_language, LANG_CHIPS, 'lang', media)}`)
  if (r.production_countries.trim()) out.push(`制片国家 ${describe(r.production_countries, [], 'country', media)}`)
  if (r.release_year.trim()) out.push(`年份 ${r.release_year.trim()}`)
  if (r.custom_regex.trim()) out.push(`片名正则 ${r.custom_regex.trim()}`)
  if (r.ext.trim()) out.push(`文件后缀 ${r.ext.trim()}`)
  return out
}

/** 一条分类的体检结果：error 挡保存，warn 只提示 */
export interface RuleLint {
  error?: string
  warn?: string
}

const normName = (s: string) => s.trim().replace(/^\/+|\/+$/g, '').toLowerCase()

/** 清单体检，与后端 validate 同口径（重名 / 空名），另外标出不会生效的 */
export function lintRules(list: CatSimpleRule[], fallback: string): { rules: RuleLint[]; fallback: RuleLint } {
  const seen = new Map<string, number>()
  let catchAll = -1
  const rules = list.map((r, i): RuleLint => {
    const name = normName(r.name)
    const lint: RuleLint = {}
    if (!name) lint.error = '填一个目录名'
    else if (seen.has(name)) lint.error = `和第 ${seen.get(name)! + 1} 条是同一个目录，合成一条（条件写在一起是「或」）`
    else seen.set(name, i)
    if (catchAll >= 0) lint.warn = `不会生效：第 ${catchAll + 1} 条没写条件，已经接走了所有片`
    else if (!ruleHasCond(r)) {
      lint.warn = '没写条件：会接走所有片，下面的分类和「其余」都不会生效'
      catchAll = i
    } else if (
      r.ext.trim() &&
      ![r.genre_ids, r.origin_country, r.original_language, r.production_countries, r.release_year, r.custom_regex].some(
        (v) => v.trim(),
      )
    )
      lint.warn = '只写了文件后缀：后缀目前不参与匹配，这条永远不会命中'
    return lint
  })
  const fb = normName(fallback)
  const fbLint: RuleLint = {}
  if (!fb) fbLint.error = '填一个目录名'
  else if (seen.has(fb)) fbLint.error = `和第 ${seen.get(fb)! + 1} 条是同一个目录`
  if (catchAll >= 0) fbLint.warn = `不会用到：第 ${catchAll + 1} 条没写条件，已经接走了所有片`
  return { rules, fallback: fbLint }
}

// ---- 模板 ----

const R = (name: string, c: Partial<CatSimpleRule> = {}): CatSimpleRule => ({ ...emptyRule(name), ...c })
const CN = 'CN,TW,HK'

export interface CatTemplate {
  key: string
  name: string
  desc: string
  tree: string
  build: () => CatSimpleForm
}

export const CAT_TEMPLATES: CatTemplate[] = [
  {
    key: 'flat',
    name: '只分电影 / 剧集',
    desc: '不细分，电影一个目录、剧集一个目录。',
    tree: '电影/  剧集/',
    build: () => ({ movie: [], tv: [], fallback: { movie: '电影', tv: '剧集' } }),
  },
  {
    key: 'region',
    name: '按地区',
    desc: '华语、欧美、日韩各一个目录。',
    tree: '电影/华语电影  剧集/国产剧 …',
    build: () => ({
      movie: [
        R('电影/华语电影', { origin_country: CN }),
        R('电影/欧美电影', { origin_country: WEST.join(',') }),
        R('电影/日韩电影', { origin_country: 'JP,KR,KP' }),
      ],
      tv: [
        R('剧集/国产剧', { origin_country: CN }),
        R('剧集/欧美剧', { origin_country: WEST.join(',') }),
        R('剧集/日韩剧', { origin_country: 'JP,KR,KP' }),
      ],
      fallback: { movie: '电影/其他', tv: '剧集/其他' },
    }),
  },
  {
    // 与 defaultRules.ts 的默认 YAML 分得一样（改一边要改另一边）
    key: 'default',
    name: '按类型 + 地区（默认）',
    desc: '动画单独放，再按地区分。和默认规则一样。',
    tree: '电视剧/国漫  电视剧/日番 …',
    build: () => ({
      movie: [R('电影/动画电影', { genre_ids: '16' }), R('电影/华语电影', { original_language: 'zh,cn,bo,za' })],
      tv: [
        R('电视剧/国漫', { genre_ids: '16', origin_country: CN }),
        R('电视剧/日番', { genre_ids: '16', origin_country: 'JP' }),
        R('电视剧/国产剧', { origin_country: CN }),
        R('电视剧/欧美剧', { origin_country: 'US,FR,GB,DE,ES,IT,NL,PT,RU' }),
        R('电视剧/日韩剧', { origin_country: 'JP,KP,KR,TH,IN,SG' }),
      ],
      fallback: { movie: '电影/外语电影', tv: '电视剧/未分类' },
    }),
  },
  {
    key: 'rich',
    name: '细分（纪录片 / 综艺 / 儿童）',
    desc: '先按类型分出纪录片、综艺、儿童、动画，再按地区分。',
    tree: '纪录片/  综艺/  动漫/日番 …',
    build: () => ({
      movie: [
        R('电影/纪录片', { genre_ids: '99' }),
        R('电影/动画电影', { genre_ids: '16' }),
        R('电影/华语电影', { origin_country: CN }),
        R('电影/欧美电影', { origin_country: WEST.join(',') }),
      ],
      tv: [
        R('纪录片', { genre_ids: '99' }),
        R('综艺', { genre_ids: '10764,10767' }),
        R('儿童', { genre_ids: '10762' }),
        R('动漫/国漫', { genre_ids: '16', origin_country: CN }),
        R('动漫/日番', { genre_ids: '16', origin_country: 'JP' }),
        R('剧集/国产剧', { origin_country: CN }),
        R('剧集/欧美剧', { origin_country: WEST.join(',') }),
      ],
      fallback: { movie: '电影/其他', tv: '剧集/其他' },
    }),
  },
]

/** 去掉空白差异后比较两张表（判断「当前是哪个模板」「有没有改动」） */
export function formKey(f: CatSimpleForm): string {
  const norm = (r: CatSimpleRule) =>
    Object.fromEntries(Object.entries(r).map(([k, v]) => [k, k === 'name' ? normName(v) : k === 'custom_regex' ? v.trim() : splitTokens(v).join(',')]))
  return JSON.stringify({
    movie: f.movie.map(norm),
    tv: f.tv.map(norm),
    fallback: { movie: normName(f.fallback.movie), tv: normName(f.fallback.tv) },
  })
}
