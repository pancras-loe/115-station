/**
 * 「识别规则」页的数据形状、预设与预览。
 *
 * 替换规则原先是一整块 `原文本=>替换后` 文本，后端却按 `[{from,to}]` 解析——
 * 两边对不上，用户写多少条都不生效。现在统一成结构化规则数组，
 * 常用的几条做成预设，新用户不必自己从零琢磨正则。
 */

export interface ReplaceRule {
  from: string
  to: string
  /** true = from 按正则（后端是 Go RE2）解释，to 里可用 $1 引用捕获组 */
  regex: boolean
}

export interface RecognizeConfig {
  replace_rules: ReplaceRule[]
  release_groups: string[]
  /** MB，0 = 不限制 */
  min_size: number
}

export const RECOGNIZE_DEFAULTS: RecognizeConfig = {
  replace_rules: [],
  release_groups: [],
  min_size: 0,
}

/**
 * 类型兜底。这三项曾经存的是纯文本（多行字符串），字段名没变但形状变了，
 * 读回来直接丢进 v-for 会把页面打崩——对不上就回到空值，不做格式迁移：
 * 旧的文本规则后端从来没解析成功过，留着也不曾生效。
 */
export function normalizeRecognize(c: RecognizeConfig): RecognizeConfig {
  const rules = Array.isArray(c.replace_rules) ? c.replace_rules : []
  return {
    replace_rules: rules
      .filter((r) => r && typeof r === 'object')
      .map((r) => ({ from: String(r.from ?? ''), to: String(r.to ?? ''), regex: !!r.regex })),
    release_groups: Array.isArray(c.release_groups) ? c.release_groups.map(String) : [],
    min_size: Number(c.min_size) || 0,
  }
}

export interface RulePreset {
  key: string
  label: string
  desc: string
  /** 「XXX → YYY」一行示例，让人不看正则也知道这条干什么 */
  sample: string
  rule: ReplaceRule
}

/**
 * 常用规则。都是实际整理里天天遇到的几类脏名字；
 * 正则一律写成 RE2 也认的形式（不用断言、不用 \d 这类简写），
 * 前端预览与后端执行才不会一个认一个不认。
 */
export const RULE_PRESETS: RulePreset[] = [
  {
    key: 'bracket-ad',
    label: '去掉【】宣传块',
    desc: '发布站塞在名字最前面的整块广告',
    sample: '【高清影视之家】蜘蛛侠.2021.mkv → 蜘蛛侠.2021.mkv',
    rule: { from: '【[^】]*】', to: '', regex: true },
  },
  {
    key: 'site-prefix',
    label: '去掉站点水印前缀',
    desc: 'www.xxx.com@ / xxx.cc- 这类域名前缀',
    sample: 'www.abc.com@蜘蛛侠.2021.mkv → 蜘蛛侠.2021.mkv',
    rule: {
      from: '^(?:www[.])?[A-Za-z0-9-]+[.](?:com|net|cc|me|xyz|top|tv|org|cn)[@_-]?',
      to: '',
      regex: true,
    },
  },
  {
    key: 'sub-lang',
    label: '去掉字幕语种标注',
    desc: '国语中字、中英字幕等标注会被当成片名的一部分',
    sample: '蜘蛛侠.2021.国语中字.1080p.mkv → 蜘蛛侠.2021..1080p.mkv',
    rule: {
      from: '(?:国语中字|国英双语|中英字幕|中文字幕|简繁中字|简体中字|双语字幕|无字幕)',
      to: '',
      regex: true,
    },
  },
  {
    key: 'cn-suffix',
    label: '去掉中文宣传后缀',
    desc: '完整版 / 未删减版 / 抢先版 这类不属于片名的词',
    sample: '蜘蛛侠.高清完整版.2021.mkv → 蜘蛛侠..2021.mkv',
    rule: {
      from: '(?:高清)?(?:完整|未删减|抢先|清晰|珍藏|典藏)版',
      to: '',
      regex: true,
    },
  },
  {
    key: 'underscore',
    label: '下划线换成点',
    desc: '下划线分隔的名字，解析器切不出年份与画质',
    sample: '蜘蛛侠_2021_1080p.mkv → 蜘蛛侠.2021.1080p.mkv',
    rule: { from: '_', to: '.', regex: false },
  },
  {
    key: 'dup-dot',
    label: '收敛连续的点',
    desc: '前面几条删干净之后常留下 ".." ——放在最后一条',
    sample: '蜘蛛侠..2021...1080p.mkv → 蜘蛛侠.2021.1080p.mkv',
    rule: { from: '[.]{2,}', to: '.', regex: true },
  },
]

/**
 * JS 认、Go 的 RE2 不认的写法：先行/后行断言、反向引用。
 * 这类规则在下面的预览里能跑出结果，实际整理时后端却会整条跳过——
 * 所以界面上要当场点名，而不是等整理完发现没生效。
 */
const RE2_UNSUPPORTED = /\(\?=|\(\?!|\(\?<[=!]|\[1-9]/

export function re2Unsupported(pattern: string): boolean {
  return RE2_UNSUPPORTED.test(pattern)
}

export interface PreviewHit {
  index: number
  rule: ReplaceRule
  after: string
}

export interface PreviewResult {
  result: string
  /** 真正改动了文件名的规则（按套用顺序），没命中的不列出来 */
  hits: PreviewHit[]
  /** 正则写错的规则下标——这些在后端也会被跳过 */
  invalid: number[]
}

/** 按配置顺序逐条套用，后一条作用在前一条的结果上（与后端 applyReplaceRules 一致） */
export function previewRules(name: string, rules: ReplaceRule[]): PreviewResult {
  let cur = name
  const hits: PreviewHit[] = []
  const invalid: number[] = []

  rules.forEach((r, index) => {
    if (!r.from) return
    let next = cur
    if (r.regex) {
      try {
        next = cur.replace(new RegExp(r.from, 'g'), r.to)
      } catch {
        invalid.push(index)
        return
      }
    } else {
      next = cur.split(r.from).join(r.to)
    }
    if (next !== cur) hits.push({ index, rule: r, after: next })
    cur = next
  })

  return { result: cur, hits, invalid }
}

/** 预览默认拿一个「脏得有代表性」的名字，省得用户自己编一个 */
export const PREVIEW_SAMPLE = '【高清影视之家发布】www.abc.com@蜘蛛侠_2021_国语中字_1080p.WEB-DL.H265-TnT.mkv'
