/**
 * 重命名模板的变量清单与示例渲染。
 *
 * 这里是变量表的唯一出处：规则说明页、「插入变量」面板、示例预览都从
 * RENAME_VAR_GROUPS 派生。此前三处各抄一份，加变量时漏改一处就会出现
 * 「说明页有、插入面板没有」或者示例渲染不出值的割裂。
 */

export interface RenameVar {
  /** 模板里的写法，如 {title} */
  token: string
  /** 插入面板里的短名 */
  label: string
  /** 示例值，同时供预览渲染使用 */
  example: string
  /** 仅剧集可用（电影字段不展示） */
  tv?: true
  /** 只在文件名里有意义（文件夹字段不展示） */
  fileOnly?: true
  /** 可能取不到值 —— 建议包进 <> 块，示例预览的「信息缺失」一行会清空它 */
  optional?: true
}

export interface RenameVarGroup {
  key: string
  title: string
  vars: RenameVar[]
}

export const RENAME_VAR_GROUPS: RenameVarGroup[] = [
  {
    key: 'basic',
    title: '基本信息',
    vars: [
      { token: '{title}', label: 'TMDB 标题', example: '钢铁侠' },
      { token: '{en_title}', label: '英文标题', example: 'Iron.Man', optional: true },
      { token: '{year}', label: '上映年份', example: '2008', optional: true },
      { token: '{tmdb_id}', label: 'TMDB ID', example: '1726', optional: true },
      { token: '{first_letter}', label: '拼音首字母（大写）', example: 'G' },
      { token: '{ext}', label: '扩展名（含点）', example: '.mkv', fileOnly: true },
      { token: '{original_name}', label: '原文件名', example: '钢铁侠.2008.2160p.UHD.BluRay.mkv', fileOnly: true },
      { token: '{custom_regex_match}', label: '自定义正则匹配', example: '自定义', optional: true },
    ],
  },
  {
    key: 'tv',
    title: '剧集专用',
    vars: [
      { token: '{season_episode}', label: '季集 SxxExx', example: 'S01E01', tv: true },
      { token: '{season_num}', label: '季号', example: '1', tv: true },
      { token: '{episode_num}', label: '集号', example: '1', tv: true },
      { token: '{season_name}', label: '季名', example: '东海篇', tv: true, optional: true },
      { token: '{season_year}', label: '季年份', example: '1999', tv: true, optional: true },
      { token: '{episode_name}', label: '集名', example: '我是路飞', tv: true, optional: true },
      { token: '{disc_num}', label: '盘号', example: '1', tv: true, optional: true },
    ],
  },
  {
    key: 'resource',
    title: '资源信息',
    vars: [
      { token: '{resource_pix}', label: '分辨率', example: '2160p', optional: true },
      { token: '{fps}', label: '帧率', example: '60FPS', optional: true },
      { token: '{resource_version}', label: '资源版本', example: 'IMAX', optional: true },
      { token: '{resource_source}', label: '资源来源', example: 'NF', optional: true },
      { token: '{resource_type}', label: '资源质量', example: 'BluRay', optional: true },
      { token: '{resource_effect}', label: '特效', example: 'DV.HDR', optional: true },
      { token: '{video_encode}', label: '视频编码', example: 'H265.10bit', optional: true },
      { token: '{audio_encode}', label: '音频编码', example: 'TrueHD.7.1', optional: true },
      { token: '{resource_team}', label: '发布组', example: 'TnT', optional: true },
    ],
  },
]

const ALL_VARS: RenameVar[] = RENAME_VAR_GROUPS.flatMap(g => g.vars)

/** 变量 → 示例值，示例预览的「信息齐全」一行 */
export const RENAME_VARS: Record<string, string> = Object.fromEntries(
  ALL_VARS.map(v => [v.token, v.example]),
)

/** 变量 → 示例值，但可选变量全部为空：用来演示 <> 块在缺信息时整块省略 */
export const RENAME_VARS_SPARSE: Record<string, string> = Object.fromEntries(
  ALL_VARS.map(v => [v.token, v.optional ? '' : v.example]),
)

/** 后端 rename.go templateExprRe 的等价物：只支持这三种无副作用的字符串变换 */
const EXPRESSION_RE = /\{([a-z_]+)(?:\.replace\('([^']*)',\s*'([^']*)'\)|\.(lower|upper)\(\))\}/g

/** 把模板里出现的 {var.replace(...)} / {var.lower()} 等表达式预先求值成普通变量 */
function resolveExpressions(rule: string, vars: Record<string, string>): Record<string, string> {
  const resolved = { ...vars }
  for (const m of rule.matchAll(EXPRESSION_RE)) {
    const base = vars[`{${m[1]}}`]
    if (base === undefined) continue
    if (m[4] === 'lower') resolved[m[0]] = base.toLowerCase()
    else if (m[4] === 'upper') resolved[m[0]] = base.toUpperCase()
    else resolved[m[0]] = base.split(m[2]).join(m[3])
  }
  return resolved
}

/**
 * 与后端 ApplyTemplate + sanitizePath 同款的输出清洗。
 * 预览不做这一步的话，缺年份时页面显示 `钢铁侠..2160p`，实际入库却是
 * `钢铁侠.2160p` —— 用户照着预览调模板等于在跟一个假结果较劲。
 */
function cleanupRendered(s: string): string {
  while (s.includes('(.)')) s = s.split('(.)').join('')
  s = s.replace(/\((\.[\w.-]+)\)/g, '$1')
  while (s.includes('..')) s = s.split('..').join('.')
  while (s.includes('--')) s = s.split('--').join('-')
  s = s.replace(/^[.-]+/, '').replace(/[.-]+$/, '').trim()
  // [[ ]] 是花括号的转义写法，必须放在变量替换之后
  s = s.split('[[').join('{').split(']]').join('}')
  // 逐段再剪一次，对应后端 sanitizePath：多级文件夹模板里中间那段缺变量
  // 时会留下 "钢铁侠." 这样的尾巴
  return s
    .split('/')
    .map(part => part.trim().replace(/^[.-]+/, '').replace(/[.-]+$/, '').trim())
    .filter(part => part !== '')
    .join('/')
}

/**
 * 按模板渲染示例文件名。
 *
 * 模板语法：`{var}` 取值；`<...>` 是块，块内变量全为空时整块丢弃。
 * 变量名按长度降序替换 —— 否则 `{season}` 会先于 `{season_episode}` 命中，
 * 把后者截成 `S01E01_episode`。
 */
export function renderRenameExample(rule: string, vars = RENAME_VARS): string {
  let s = rule || ''
  const resolved = resolveExpressions(s, vars)
  const keys = Object.keys(resolved).sort((a, b) => b.length - a.length)

  // 逐层处理最内层的 <...> 块，直到没有块可处理
  let prev: string
  do {
    prev = s
    const m = s.match(/<([^<>]*)>/)
    if (!m) break
    const block = m[1]
    let keep = block.includes('{')
    let rendered = block
    for (const k of keys) {
      if (!block.includes(k)) continue
      if (resolved[k] === '') keep = false
      rendered = rendered.split(k).join(resolved[k])
    }
    // 还有未替换的 {xxx} → 后端按「变量为空」丢整块，预览保持一致
    if (rendered.includes('{')) keep = false
    s = s.replace(m[0], keep ? rendered : '')
  } while (s !== prev)
  s = s.split('<').join('').split('>').join('')

  for (const k of keys) s = s.split(k).join(resolved[k])
  return cleanupRendered(s)
}

/**
 * 找出模板里后端认不出来的 `{...}`。
 * 后端遇到未知变量会原样留在文件名里（`{titel}.2008.mkv`），等发现时
 * 整批文件已经改完名了，所以在保存前就把它标出来。
 */
export function findUnknownTokens(rule: string): string[] {
  const bad = new Set<string>()
  for (const m of (rule || '').matchAll(/\{([^{}]*)\}/g)) {
    const inner = m[1].trim()
    const base = inner.split('.')[0].trim()
    if (RENAME_VARS[`{${base}}`] === undefined) {
      bad.add(m[0])
      continue
    }
    // 带后缀的必须是支持的三种变换之一，{title.strip()} 这类要拦下来
    if (inner !== base && !new RegExp(`^${EXPRESSION_RE.source}$`).test(m[0])) bad.add(m[0])
  }
  return [...bad]
}

export interface RenameConfig {
  movie_folder: string
  movie_file: string
  tv_folder: string
  tv_file: string
}

/** 与后端 defaultRenameConfig() 保持一致，改这里要同步改 internal/api/organize.go */
export const DEFAULT_RENAME: RenameConfig = {
  movie_folder: '{title}.{year}<.[[tmdbid={tmdb_id}]]>',
  movie_file: "{title}<.{en_title.replace('.', ' ')}>.{year}<.{resource_type}><.{resource_effect.replace('.', ' ')}><.{resource_pix}><.{video_encode}><.{audio_encode}>{ext}",
  tv_folder: '{title}.{year}<.[[tmdbid={tmdb_id}]]>/Season {season_num}',
  tv_file: "{title}<.{en_title.replace('.', ' ')}>.{season_episode}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}>{ext}",
}
