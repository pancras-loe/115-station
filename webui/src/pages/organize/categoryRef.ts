/**
 * 二级分类规则的参考表。
 * genre_ids / origin_country / production_countries / original_language 的取值来自 TMDB，
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
  // TMDB 没有「演唱会」类型，演唱会电影一般标在音乐下；写进名字里好让用户搜得到。
  // 但音乐传记片、歌舞片也是这个类型，演唱会单独分类要靠片名（见规则生成器的预设）
  ['10402', '音乐（含演唱会）'],
  ['9648', '悬疑'],
  ['10749', '爱情'],
  ['878', '科幻'],
  ['10770', '电视电影'],
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
  ['10762', '儿童'],
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
  ['分类名', '即媒体库根下的目录名，写什么就落到哪；可用 / 建多级；不写任何条件的那条是兜底'],
  ['genre_ids', 'TMDB 类型 ID（16 = 动漫，99 = 纪录片，10762 = 儿童），逗号分隔'],
  ['original_language', '语言代码（zh = 中文，ja = 日语，ko = 韩语），逗号分隔'],
  ['origin_country', '出品国家代码（CN、HK、JP、KR、US），逗号分隔'],
  ['production_countries', '制片国家代码，写法同上；老电影常缺 origin_country，按国家分电影时用它更稳'],
  ['release_year', '发行年份（剧集取首播年份），如 2020 或范围 2000-2009'],
  ['keywords', '片名关键词：标题、原名、别名、各语言译名里含其一就算（子串，不分大小写），如 演唱会,concert'],
  ['include_keywords', 'TMDB 标签（keywords）含其一就算（子串，不分大小写，标签多是英文），如 live concert；条目没打标签时不会命中'],
  ['series_keywords', '系列（按片名）：命中时在分类下加一层「X系列」，X 是命中的那个词，如 碟中谍,速度与激情'],
  ['series_actors', '系列（按演员）：只看前 6 位演员，如 周星驰 → 分类/周星驰系列'],
  ['series_directors', '系列（按导演）：只看前 6 位导演（剧集含主创），如 诺兰'],
  ['?字段', '字段名前加 ?（或 ？）是可选条件：可选的之间「或」、至少命中一个，没加 ? 的仍然全部要满足'],
  ['custom_regex', '正则匹配片名 / 原名，命中即归此类（不要求其他条件同时成立）'],
  ['ext', '文件后缀；当前版本不参与匹配：只写了 ext 的规则不会匹配任何条目'],
  ['library', '这一类整理进哪个媒体库：写库的本地目录名（「账号与媒体库 → 媒体库」卡片上那个，或 cid），不写就进「默认放这里」的那个库。它不是匹配条件，只写了它的规则照样是兜底。有多个媒体库时，右边的规则大纲会标出每条规则落进哪个库'],
]

export const CATEGORY_RULES: [string, string][] = [
  ['顺序匹配', '从上到下，先匹配到的先停止'],
  ['多条件', '同一分类下多个条件为「且」关系（写成 ?字段 的可选条件之间为「或」）'],
  ['分隔符', '一个条件写几个值用 , ， ; ； 或换行隔开'],
  ['候选 / 必须 / 排除', '不带前缀的是候选，命中其一即可；+ 表示必须含；- 或 ! 表示排除，如 origin_country: "!CN"'],
  ['写了候选', '只要写了不带前缀的值就得命中其中一个：「动作且非恐怖」写 +28,-27，写成 +28,12,-27 还要求是冒险片'],
  ['范围', '数字可写范围：release_year: "2000-2009"、genre_ids: "10764-10767"（片名、标签这类文本不支持）'],
  ['大小写', '代码不区分大小写，us 与 US 相同'],
  ['字段为空', '写了某个条件、条目上这项却没有数据（如 TMDB 没填国家）时不匹配，只写排除也一样'],
  ['写错字段', '认不出的条件名（如拼成 genre_id）、空值、年份写错保存时直接拒收'],
  ['兜底分类', '不写条件的那条匹配一切；保存时自动挪到这一段最后'],
  ['系列', '写了 series_* 的规则：只写系列时命中系列才归这一类；和别的条件一起写时系列只决定加不加「X系列」这一层'],
  ['没有兜底时', '全都不匹配就归入「电影/未分类」「剧集/未分类」'],
  ['不自动加层', '分类名不会被补前缀：要 电影/动画电影 就写全，要平铺就直接写 动漫番剧'],
  ['movie / tv', '两类独立配置，互不干扰；其他顶层键会被忽略'],
  ['自动建目录', '115 中不存在时自动创建'],
]

/** 分类规则的设置项：会被后端解析，但不是匹配条件（不影响「兜底」判断） */
export const CATEGORY_SETTING_KEYS = ['library'] as const

/** 分类条件字段（保存时真正被解析的键） */
export const CATEGORY_FIELD_KEYS = [
  'genre_ids',
  'original_language',
  'origin_country',
  'production_countries',
  'release_year',
  'keywords',
  'include_keywords',
  'series_keywords',
  'series_actors',
  'series_directors',
  'custom_regex',
  'ext',
] as const

/** 系列字段：命中时加子目录，不参与必填 / 可选（后端 catFields 的 series） */
export const CATEGORY_SERIES_KEYS = ['series_keywords', 'series_actors', 'series_directors'] as const

export const WASH_MODES: [string, string][] = [
  ['coexist', '共存，两个版本都留着，新版照常入库'],
  ['skip', '跳过，库内已有就不再收新的'],
  ['replace', '新版更好才替换，全部打平不换（怎么比见下面「匹配规则」）'],
  ['max_size', '按 replace 处理，命中同一条时留体积大的（第一条规则自动补 size_prefer: large）'],
  ['min_size', '按 replace 处理，命中同一条时留体积小的（第一条规则自动补 size_prefer: small）'],
]

export const WASH_STRATEGY_FIELDS: [string, string][] = [
  ['mode', '洗版模式，见上表；留空按 replace'],
  ['scope', 'all = 同一影片（剧集同一集）跨分辨率比较；group = 只比较同分辨率'],
  ['media_type', 'movie / tv，留空匹配所有'],
  ['category', '限定二级分类名，逗号分隔，留空匹配所有'],
  ['prefer', '画质偏好（简单设置那一套）：max_pix / dv / size / avoid / avoid_words / teams；写了它 priority_level 不生效'],
  ['priority_level', '可选，分档规则；不填就按默认画质比（分辨率 → 片源 → HDR → 视频编码）。一条都没命中算最差'],
  ['old_version_target', '旧版去向：redundant 冗余目录 / existing 已存在目录 / delete 115 回收站'],
]

export const WASH_FIELDS: [string, string][] = [
  ['resource_pix', '分辨率（2160p、1080p、720p）'],
  ['resource_type', '资源质量（BluRay、WEB-DL、HDTV、REMUX）'],
  ['resource_effect', '特效（DV.HDR、HDR10+，!DV 排除）'],
  ['video_encode', '视频编码（H265、x264、HEVC、AV1）'],
  ['audio_encode', '音频编码（TrueHD、DTS-HD、Atmos、AAC）'],
  ['resource_team', '发布组（WiKi、TnT、FRDS）'],
  ['resource_size', '体积（<30G、>=5G、5G-30G，单位 T / G / M，不写按 G；逗号分隔命中任一）'],
  ['size_prefer', '体积偏好（small 小的优先 / large 大的优先）：都命中这一条时先比体积，再比画质'],
]

/** 优先级条目里真正被解析的键 */
export const WASH_LEVEL_FIELD_KEYS = [
  'resource_pix',
  'resource_type',
  'resource_effect',
  'video_encode',
  'audio_encode',
  'resource_team',
  'resource_size',
  'size_prefer',
] as const

export const WASH_RULES: [string, string][] = [
  ['策略匹配', '顶层策略从上到下，按 media_type / category 命中第一条就用它'],
  ['默认比法', '分辨率 → 片源 → HDR → 视频编码，前面分出高下就不看后面：2160p > 1080p，REMUX > BluRay > WEB-DL > WEBRip > HDTV，DV > HDR10+ > HDR10 > HDR，H265 / AV1 > H264'],
  ['没写的', '分辨率 / 片源 / 编码有一边名字里没写就不比这一项（不知道不算更差）；HDR 没写算 SDR'],
  ['优先级', 'priority_level 从上到下算分：命中越靠上的一条分越高，一条都没命中最低；分高的赢'],
  ['同分时', '命中同一条（或都没命中）再按默认比法比；这条规则里写了的字段，最多按命中的那个值算，没写 DV 的规则里 DV 不占便宜'],
  ['多条件', '同一条优先级下多个条件为「且」关系'],
  ['多值', '逗号分隔：正值命中任一即算符合，! 前缀命中任一即排除；先后顺序不影响比较'],
  ['体积', 'resource_size 是条件，和其他字段一样「且」；size_prefer 写在某一条上，两份都命中这一条时先比体积（相差不到 5% 算一样大，接着比画质）。订阅按集挑分享里的文件、订阅洗版也带体积比；只有标题、不知道单个文件体积时（找资源排名、按标题筛候选）这两项不看'],
  ['判不出时', '全部打平 → 新版按「已存在」处理；没写 size_prefer 时不拿体积分胜负'],
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
  resource_size: ['<10G', '<20G', '<30G', '<50G', '>=5G', '5G-30G'],
  size_prefer: ['small', 'large'],
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
