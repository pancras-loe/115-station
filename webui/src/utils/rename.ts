/** 重命名模板的示例变量值，与后端 rename.go 的变量体系对齐 */
export const RENAME_VARS: Record<string, string> = {
  '{title}': '钢铁侠',
  '{en_title}': 'Iron Man',
  '{original_name}': '钢铁侠.2008.2160p.UHD.BluRay.mkv',
  '{year}': '2008',
  '{tmdb_id}': '1726',
  '{first_letter}': 'G',
  '{ext}': '.mkv',
  '{custom_regex_match}': '自定义',
  '{season_episode}': 'S01E01',
  '{season_num}': '1',
  '{episode_num}': '1',
  '{season_name}': '东海篇',
  '{episode_name}': '我是路飞',
  '{season_year}': '1999',
  '{disc_num}': '1',
  '{resource_pix}': '2160p',
  '{fps}': '60FPS',
  '{resource_version}': 'IMAX',
  '{resource_source}': 'NF',
  '{resource_type}': 'BluRay',
  '{resource_effect}': 'DV.HDR',
  '{video_encode}': 'H265.10bit',
  '{audio_encode}': 'TrueHD.7.1',
  '{resource_team}': 'TnT',
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
  const keys = Object.keys(vars).sort((a, b) => b.length - a.length)

  // 逐层处理最内层的 <...> 块，直到没有块可处理
  let prev: string
  do {
    prev = s
    const m = s.match(/<([^<>]*)>/)
    if (!m) break
    const block = m[1]
    let hasVar = false
    let rendered = block
    for (const k of keys) {
      if (block.includes(k)) {
        hasVar = true
        rendered = rendered.split(k).join(vars[k])
      }
    }
    s = s.replace(m[0], hasVar ? rendered : '')
  } while (s !== prev)

  for (const k of keys) s = s.split(k).join(vars[k])
  // [[ ]] 是花括号的转义写法
  return s.replace(/\[\[/g, '{').replace(/\]\]/g, '}')
}

export interface RenamePreset {
  folder: string
  file: string
}

export const RENAME_PRESETS: Record<'movie' | 'tv', Record<string, RenamePreset>> = {
  movie: {
    default: {
      folder: '{first_letter}-{title}-{year}-[tmdb={tmdb_id}]',
      file: '{title}.{year}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}><-{resource_team}>{ext}',
    },
    lite: { folder: '{title} ({year})', file: '{title}.{year}<.{resource_pix}>{ext}' },
    full: {
      folder: '[{first_letter}]-{title}-{year}-[tmdb={tmdb_id}]',
      file: '{title}.{year}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}><-{resource_team}><{custom_regex_match}><[tmdb{tmdb_id}]{ext}',
    },
  },
  tv: {
    default: {
      folder: '{first_letter}-{title}-{year}-[tmdb={tmdb_id}]',
      file: '{title} - {season_episode}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}><-{resource_team}>{ext}',
    },
    lite: { folder: '{title} ({year})', file: '{title} - {season_episode}<.{resource_pix}>{ext}' },
    full: {
      folder: '[{first_letter}]-{title}-{year}-[tmdb={tmdb_id}]',
      file: '{title} - {season_episode}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}><-{resource_team}><{custom_regex_match}><[tmdb{tmdb_id}]{ext}',
    },
  },
}

/** 当前模板与哪个预设完全一致；都不一致返回 '' = 自定义 */
export function matchPreset(type: 'movie' | 'tv', folder: string, file: string): string {
  for (const [k, p] of Object.entries(RENAME_PRESETS[type])) {
    if (folder.trim() === p.folder && file.trim() === p.file) return k
  }
  return ''
}
