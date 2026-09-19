/**
 * 二级分类规则的参考表。
 * genre_ids / origin_country / original_language 的取值来自 TMDB，
 * 这里只是速查，改动前先对照 TMDB 官方文档。
 */

export const MOVIE_GENRES: [string, string][] = [
  ['28', '动作'],
  ['12', '冒险'],
  ['16', '动画'],
  ['35', '喜剧'],
  ['80', '犯罪'],
  ['99', '纪录片'],
  ['18', '剧情'],
  ['10751', '家庭'],
  ['14', '奇幻'],
  ['36', '历史'],
  ['27', '恐怖'],
  ['10402', '音乐'],
  ['9648', '悬疑'],
  ['10749', '爱情'],
  ['878', '科幻'],
  ['53', '惊悚'],
  ['10752', '战争'],
  ['37', '西部'],
]

export const TV_GENRES: [string, string][] = [
  ['10759', '动作冒险'],
  ['16', '动漫 / 番剧'],
  ['35', '喜剧'],
  ['80', '犯罪'],
  ['99', '纪录片'],
  ['18', '剧情'],
  ['10751', '家庭'],
  ['9648', '悬疑'],
  ['10763', '新闻'],
  ['10764', '综艺（旧）'],
  ['10765', '科幻奇幻'],
  ['10766', '肥皂剧'],
  ['10767', '综艺（新）'],
  ['10768', '战争政治'],
  ['37', '西部'],
]

export const COUNTRIES: { group: string; items: [string, string][] }[] = [
  {
    group: '亚洲',
    items: [
      ['CN', '中国大陆'],
      ['HK', '中国香港'],
      ['TW', '中国台湾'],
      ['JP', '日本'],
      ['KR', '韩国'],
      ['KP', '朝鲜'],
      ['TH', '泰国'],
      ['IN', '印度'],
      ['SG', '新加坡'],
      ['MY', '马来西亚'],
      ['ID', '印度尼西亚'],
      ['PH', '菲律宾'],
      ['VN', '越南'],
      ['TR', '土耳其'],
      ['IL', '以色列'],
      ['AE', '阿联酋'],
      ['IR', '伊朗'],
      ['KZ', '哈萨克斯坦'],
    ],
  },
  {
    group: '欧美',
    items: [
      ['US', '美国'],
      ['GB', '英国'],
      ['CA', '加拿大'],
      ['AU', '澳大利亚'],
      ['NZ', '新西兰'],
      ['IE', '爱尔兰'],
      ['FR', '法国'],
      ['DE', '德国'],
      ['ES', '西班牙'],
      ['IT', '意大利'],
      ['PT', '葡萄牙'],
      ['NL', '荷兰'],
      ['BE', '比利时'],
      ['CH', '瑞士'],
      ['SE', '瑞典'],
      ['NO', '挪威'],
      ['DK', '丹麦'],
      ['FI', '芬兰'],
      ['PL', '波兰'],
      ['CZ', '捷克'],
      ['RU', '俄罗斯'],
      ['UA', '乌克兰'],
      ['HU', '匈牙利'],
      ['GR', '希腊'],
      ['RO', '罗马尼亚'],
      ['AT', '奥地利'],
      ['IS', '冰岛'],
    ],
  },
  {
    group: '其他',
    items: [
      ['BR', '巴西'],
      ['MX', '墨西哥'],
      ['AR', '阿根廷'],
      ['CO', '哥伦比亚'],
      ['CL', '智利'],
      ['ZA', '南非'],
      ['EG', '埃及'],
      ['NG', '尼日利亚'],
      ['JO', '约旦'],
      ['SA', '沙特'],
      ['QA', '卡塔尔'],
      ['LB', '黎巴嫩'],
      ['MA', '摩洛哥'],
      ['PE', '秘鲁'],
      ['VE', '委内瑞拉'],
      ['UY', '乌拉圭'],
      ['KE', '肯尼亚'],
      ['GH', '加纳'],
      ['DZ', '阿尔及利亚'],
      ['TN', '突尼斯'],
      ['IQ', '伊拉克'],
      ['KW', '科威特'],
      ['BD', '孟加拉国'],
      ['PK', '巴基斯坦'],
      ['LK', '斯里兰卡'],
      ['MM', '缅甸'],
      ['KH', '柬埔寨'],
      ['LA', '老挝'],
      ['MN', '蒙古'],
      ['NP', '尼泊尔'],
    ],
  },
]

export const LANGUAGES: [string, string][] = [
  ['zh', '中文（普通话）'],
  ['cn', '粤语'],
  ['bo', '藏语'],
  ['za', '壮语'],
  ['ja', '日语'],
  ['ko', '韩语'],
  ['en', '英语'],
  ['fr', '法语'],
  ['de', '德语'],
  ['es', '西班牙语'],
  ['it', '意大利语'],
  ['pt', '葡萄牙语'],
  ['ru', '俄语'],
  ['th', '泰语'],
  ['hi', '印地语'],
  ['ta', '泰米尔语'],
  ['te', '泰卢固语'],
  ['ml', '马拉雅拉姆语'],
  ['kn', '卡纳达语'],
  ['ar', '阿拉伯语'],
  ['he', '希伯来语'],
  ['fa', '波斯语'],
  ['tr', '土耳其语'],
  ['pl', '波兰语'],
  ['nl', '荷兰语'],
  ['sv', '瑞典语'],
  ['no', '挪威语'],
  ['da', '丹麦语'],
  ['fi', '芬兰语'],
  ['cs', '捷克语'],
  ['el', '希腊语'],
  ['hu', '匈牙利语'],
  ['ro', '罗马尼亚语'],
  ['uk', '乌克兰语'],
  ['vi', '越南语'],
  ['id', '印尼语'],
  ['ms', '马来语'],
  ['tl', '菲律宾语'],
  ['km', '高棉语'],
]

export const CATEGORY_FIELDS: [string, string][] = [
  ['分类名', '即 115 目录名，可用 / 建多级；不写任何条件的那条是兜底'],
  ['不分二级', '分类名直接写「电影」/「剧集」，表示不建二级目录，整理到一级目录下'],
  ['genre_ids', 'TMDB 类型 ID（16 = 动漫，99 = 纪录片），逗号分隔'],
  ['original_language', '语言代码（zh = 中文，ja = 日语，ko = 韩语），逗号分隔'],
  ['origin_country', '国家代码（CN、HK、JP、KR、US），逗号分隔'],
  ['custom_regex', '正则匹配片名 / 原名，命中即归此类（不要求其他条件同时成立）'],
  ['ext', '文件后缀；当前版本不参与匹配，只会让该条不再充当兜底'],
]

export const CATEGORY_RULES: [string, string][] = [
  ['顺序匹配', '从上到下，先匹配到的先停止'],
  ['多条件', '同一分类下多个条件为「且」关系，同一条件内逗号为「或」'],
  ['兜底分类', '不写条件的那条匹配一切，它之后的规则永远轮不到'],
  ['没有兜底时', '全都不匹配就归入「未分类」子目录 —— 想直接放一级目录下要写一条「电影」/「剧集」兜底'],
  ['一级目录前缀', '分类名里的「电影/」「剧集/」前缀会被自动剥掉，不会整理成 电影/电影/片名'],
  ['movie / tv', '两类独立配置，互不干扰；其他顶层键会被忽略'],
  ['自动建目录', '115 中不存在时自动创建'],
]

/** 分类条件字段（保存时真正被解析的键） */
export const CATEGORY_FIELD_KEYS = [
  'genre_ids',
  'original_language',
  'origin_country',
  'custom_regex',
  'ext',
] as const

export const WASH_MODES: [string, string][] = [
  ['coexist', '共存，两个版本都留着，新版照常入库'],
  ['skip', '跳过，库内已有就不再收新的'],
  ['replace', '替换，按优先级比较，新版更优才顶掉旧版'],
  ['max_size', '按 replace 处理；体积比较尚未实现'],
  ['min_size', '按 replace 处理；体积比较尚未实现'],
]

export const WASH_STRATEGY_FIELDS: [string, string][] = [
  ['mode', '洗版模式，见上表；留空按 replace'],
  ['scope', 'all = 全局只留一个最优；group = 按分辨率分组各留一个'],
  ['media_type', 'movie / tv，留空匹配所有'],
  ['category', '限定二级分类名，逗号分隔，留空匹配所有'],
  ['priority_level', '优先级规则列表，从上到下越靠前越优先'],
  ['old_version_target', '旧版去向：redundant 冗余目录 / existing 已存在目录'],
]

export const WASH_FIELDS: [string, string][] = [
  ['resource_pix', '分辨率（2160p、1080p、720p）'],
  ['resource_type', '资源质量（BluRay、WEB-DL、HDTV、REMUX）'],
  ['resource_effect', '特效（DV.HDR、HDR10+，!DV 排除）'],
  ['video_encode', '视频编码（H265、x264、HEVC、AV1）'],
  ['audio_encode', '音频编码（TrueHD、DTS-HD、Atmos、AAC）'],
  ['resource_team', '发布组（WiKi、TnT、FRDS）'],
]

/** 优先级条目里真正被解析的键 */
export const WASH_LEVEL_FIELD_KEYS = [
  'resource_pix',
  'resource_type',
  'resource_effect',
  'video_encode',
  'audio_encode',
  'resource_team',
] as const

export const WASH_RULES: [string, string][] = [
  ['策略匹配', '顶层策略从上到下，按 media_type / category 命中第一条就用它'],
  ['优先级', 'priority_level 从上到下，第一条能分出高下的决定胜负'],
  ['多条件', '同一条优先级下多个条件为「且」关系'],
  ['多值', '逗号分隔：正值命中任一即算符合，! 前缀命中任一即排除'],
  ['判不出时', '所有优先级都平手 → 保守不替换，新版按「已存在」处理'],
  ['剧集', '只与同一集比较；库内没有这一集时直接入库，不做洗版'],
]

/** 洗版条件里的常见取值，供规则生成器下拉用（可手填其他值） */
export const WASH_VALUE_OPTIONS: Record<string, string[]> = {
  resource_pix: ['2160p', '1080p', '720p', '4k'],
  resource_type: ['BluRay', 'WEB-DL', 'REMUX', 'HDTV', 'DVDRip'],
  resource_effect: ['DV.HDR', 'DV', 'HDR10+', 'HDR', 'SDR', '!DV.HDR', '!DV', '!SDR'],
  video_encode: ['H265', 'H264', 'HEVC', 'x265', 'x264', 'AV1'],
  audio_encode: ['TrueHD', 'Atmos', 'DTS-HD', 'DTS', 'EAC3', 'AAC', 'FLAC'],
  resource_team: ['WiKi', 'FRDS', 'TnT', 'CMCT', 'HDS', 'MTeam'],
}

const GENRE_NAME: Record<string, Record<string, string>> = {
  movie: Object.fromEntries(MOVIE_GENRES),
  tv: Object.fromEntries(TV_GENRES),
}
const COUNTRY_NAME: Record<string, string> = Object.fromEntries(COUNTRIES.flatMap(c => c.items))
const LANGUAGE_NAME: Record<string, string> = Object.fromEntries(LANGUAGES)

/** 规则大纲把代码翻成人话：认不出的原样回显，不猜 */
export const genreName = (media: string, id: string) => GENRE_NAME[media]?.[id.trim()] ?? ''
export const countryName = (code: string) => COUNTRY_NAME[code.trim()] ?? ''
export const languageName = (code: string) => LANGUAGE_NAME[code.trim()] ?? ''
