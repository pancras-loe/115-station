import { http } from './client'
import type { QueuedReply } from './tasks'
import type { LibScrape } from './organize'

/**
 * 移出配置的媒体库的本地残留（后端 libleft.go）：台账第一段既不是配置中的库名、也不是分类目录。
 * 清理只删本地 STRM / 附属与台账行，网盘不动；入任务队列执行
 */
export interface LibLeftover {
  name: string
  files: number
  videos: number
  /** 本地目录还在 */
  local: boolean
}

export const getLeftovers = () => http.get<{ data: LibLeftover[]; error?: string }>('/library/leftovers')
export const cleanLeftover = (name: string) => http.post<QueuedReply>('/library/leftovers/clean', { name })

// ---- 媒体库卡片（后端 libraries.go / librariessave.go） ----

/** 一条分类规则（只读显示；改去二级分类） */
export interface RuleLibRef {
  media_type: 'movie' | 'tv'
  name: string
  /** 认不出的库时带上 YAML 里写的原值 */
  library?: string
}

/** 绑定到这个库的转存子目录 */
export interface LibInbox {
  cid: string
  name: string
  lib: string
  lib_name?: string
  problem?: string
}

/** 这个库在 Emby 里有没有被覆盖 */
export interface LibEmbyState {
  covered: boolean
  partial: boolean
  /** 按分类建 Emby 库时还没进 Emby 的分类目录（能换算时是 Emby 路径） */
  missing?: string[]
  /** 路径映射的本地一侧不包含这个库 */
  unmapped: boolean
  /** 应在 Emby 里添加的路径 */
  suggest?: string
}

export interface LibraryView {
  cid: string
  /** 本地目录名（STRM 第一层，规则 library 认的就是它）；没定过时是 115 目录名；认不出为空 */
  name: string
  path: string
  local: string
  /** 115 上现在的目录名；和 name 不同时界面提示一句（115 上改过名不影响本地） */
  pan_name: string
  /** 台账里已经有这个库的内容：本地目录名不能再改 */
  local_locked: boolean
  /** 附加账号的库（后端 acctlibs.go）：账号 id（主号 0）、账号名；cid 是 a{账号}:{cid}，raw_cid 是它在那个账号里的目录 id */
  account: number
  account_name?: string
  raw_cid?: string
  /** 上一次全量里这个库的结果（后端 libsyncstat.go）；没同步过没有这个字段 */
  last_sync?: LibSyncStat
  /** 「默认放这里」的库：二级分类里没写 library 的规则落进它（阶段 3 起不一定是首个库） */
  default: boolean
  /** 首个媒体库（full.cid）：115 目录在「媒体库位置」里改、不能移除 */
  first: boolean
  /** 谁来整理：organize 本站整理 / self 我自己整理（后端 libopts.go） */
  mode: LibMode
  /** 自己整理的库：片目在库下第几层（1–3） */
  depth: number
  /** 刮不刮：auto 自动刮新片 / manual 只手动刮 / off 不刮（交给 Emby） */
  scrape: LibScrape
  rules: RuleLibRef[]
  inboxes: LibInbox[]
  emby?: LibEmbyState
}

/** 一个库上一次全量的结果 */
export interface LibSyncStat {
  /** 结束时间（unix 秒） */
  at: number
  ok: boolean
  error?: string
  /** fast / normal */
  mode?: string
  seconds: number
  videos: number
  created: number
  failed: number
  assets: number
  orphans: number
  complete: boolean
}

export type LibMode = 'organize' | 'self'

/** 一个库让本站做什么；不在 opts 里的库按本站整理 */
export interface LibOpt {
  mode: LibMode
  depth?: number
  scrape?: LibScrape
}

export interface LibRenameFix {
  from: string
  to: string
  lib: string
}

export interface LibrariesView {
  libs: LibraryView[]
  orphan_rules: RuleLibRef[]
  rename_fixes: LibRenameFix[]
  /** 各库名字都认得出；认不全时不报「认不出的库」 */
  names_known: boolean
  staging_root: string
  local_root: string
  emby_configured: boolean
  emby_error?: string
}

/** 一个转存入口：cid 是已有子目录；cid 为空 = 按 name 在转存目录下新建（同名已存在就复用） */
export interface LibInboxReq {
  cid?: string
  name: string
  lib: string
  /** 同名子目录已存在且不空时，用户确认过 */
  confirm?: boolean
}

export interface LibSaveReq {
  extra_libs: { cid: string; path: string }[]
  inboxes: LibInboxReq[]
  /** 这次移除的库里要同时清本地的（库 cid） */
  clean_local?: string[]
  /** 每个库的整理方式（库 cid → 选项），整份替换 */
  opts: Record<string, LibOpt>
  /** 「默认放这里」的库 cid；空 = 交给后端挑第一个本站整理的库 */
  fallback?: string
  /** 要设的本地目录名（库 cid → 名字），只带用户改过的；新库不带就用 115 目录名 */
  locals?: Record<string, string>
  /** 附加账号的库，整份替换；不带 = 沿用 */
  acct_libs?: { account: number; cid: string; path: string; local: string }[]
}

/** 409：转存目录里已有同名子目录、里面有东西，要用户确认 */
export interface LibInboxConfirm {
  name: string
  lib: string
  items: number
}

export const getLibraries = () => http.get<LibrariesView>('/libraries')

/** 自己整理的库按某个层级认出来的片目（后端 libpreview.go，只读台账） */
export interface LibPreviewTitle {
  key: string
  title: string
  media_type: 'movie' | 'tv'
  videos: number
  /** 看着不像一部片的原因 */
  odd?: string
}
export interface LibPreview {
  lib: string
  depth: number
  total: number
  titles: LibPreviewTitle[]
  odd: number
  /** 比片目层还浅、不属于任何片目的视频数 */
  loose: number
}
/** name：新加、还没保存的库按它查台账 */
export const previewLibrary = (cid: string, depth: number, name?: string) =>
  http.get<LibPreview>('/libraries/preview', { params: { cid, depth, name } })
export const saveLibraries = (req: LibSaveReq) =>
  http.put<{ message: string; jobs?: number[]; subs_moved: number; data: LibrariesView }>('/libraries', req)
/** 库在 115 上改了名：把分类规则里写的旧名换成新名（新名由后端认，只传旧名） */
export const fixRename = (from: string) =>
  http.post<{ message: string; data: LibrariesView }>('/libraries/fix-rename', { from })

// ---- 媒体库页面（V2：一个库一条，后端 libapiv2.go / catschemeapi.go） ----

/** 一张库卡片 */
export interface MediaLib {
  id: number
  name: string
  account: number
  account_name: string
  cid: string
  path: string
  /** 媒体根下的相对路径 */
  local: string
  /** 媒体根 + 本地目录 */
  local_abs: string
  staging_cid: string
  staging_path: string
  /** 0 = 不分类 */
  scheme_id: number
  scheme_name: string
  /** 只同步、不分类的库：片目在第几层 */
  depth: number
  scrape: LibScrape
  /** 本地新产生的 NFO / 图片回传这个库的 115 目录（监控上传，每库一个开关） */
  upload: boolean
  /** 台账里已经有这个库的内容：本地目录不能再改 */
  local_locked: boolean
  last_sync?: LibSyncStat
  problem?: string
  /** 设了转存文件夹：本站往里整理 */
  organize: boolean
  emby?: LibEmbyState
}

export interface SchemeView {
  id: number
  name: string
  used_by: string[]
  movie: number
  tv: number
}

export interface AcctOption {
  id: number
  name: string
  main: boolean
}

export interface MediaLibsView {
  media_root: string
  libs: MediaLib[]
  schemes: SchemeView[]
  accounts: AcctOption[]
  notice: string[]
  emby_configured: boolean
  emby_error?: string
  root_problem?: string
}

/** 新建 / 修改一个库 */
export interface MediaLibReq {
  name: string
  account: number
  cid: string
  path: string
  local: string
  staging_cid: string
  staging_path: string
  scheme_id: number
  depth: number
  scrape: LibScrape
  /** 不带 = 不改 */
  upload?: boolean
}

export const getMediaLibs = () => http.get<MediaLibsView>('/media-libs')
export const createMediaLib = (req: MediaLibReq) => http.post<MediaLibsView>('/media-libs', req)
/** 一次建几个库：只给 staging_path 的转存文件夹由后端按路径找、没有就建；全成或全不成 */
export const createMediaLibsBatch = (libs: MediaLibReq[]) => http.post<MediaLibsView>('/media-libs/batch', { libs })
export const updateMediaLib = (id: number, req: MediaLibReq) => http.put<MediaLibsView>(`/media-libs/${id}`, req)
export const deleteMediaLib = (id: number) => http.del<MediaLibsView>(`/media-libs/${id}`)
export const dismissMediaLibNotice = () => http.post<{ ok: boolean }>('/media-libs/notice/dismiss', {})

/** 二级分类简单模式的一条分类（后端 catsimple.go；条件值是原样的逗号串） */
export interface CatSimpleRule {
  name: string
  genre_ids: string
  origin_country: string
  original_language: string
  production_countries: string
  release_year: string
  custom_regex: string
  ext: string
}
export interface CatSimpleForm {
  movie: CatSimpleRule[]
  tv: CatSimpleRule[]
  /** 「其余都放这里」：固定在每个媒体类型最后 */
  fallback: { movie: string; tv: string }
}
export interface SchemeDetail {
  id: number
  name: string
  yaml: string
  /** 简单模式的表单；认不回来时为 null，原因在 simple_why */
  simple: CatSimpleForm | null
  simple_why: string[]
  /** 存着的 YAML 里有注释（简单模式保存会去掉） */
  comments: boolean
  /** 还没有任何分类规则 */
  empty: boolean
}
/** 删掉 / 改了名的分类，原来那个目录里已经有片（保存时 409 带回来） */
export interface CatDirChange {
  media_type: 'movie' | 'tv'
  name: string
  titles: number
}
export interface CatTryResult {
  dest: string
  /** 命中第几条（0 起），-1 = 进了「其余都放这里」 */
  index: number
  skips: { index: number; name: string; reason: string }[]
}
export interface CatTryMedia {
  title: string
  year: string
  media_type: 'movie' | 'tv'
  genre_ids: number[]
  original_language: string
  origin_country: string[] | null
  production_countries: string[] | null
}

export const getScheme = (id: number) => http.get<SchemeDetail>(`/category/schemes/${id}`)
/** 纯计算：给 simple 返回生成的 YAML；带 tmdb_id 时按这份（未保存的）规则算这部片进哪个目录 */
export const previewScheme = (body: { simple?: CatSimpleForm; yaml?: string; tmdb_id?: number; media_type?: string }) =>
  http.post<{ yaml?: string; try?: CatTryResult; media?: CatTryMedia }>('/category/schemes/preview', body)
export const createScheme = (name: string, copyFrom: number) =>
  http.post<{ id: number; schemes: SchemeView[] }>('/category/schemes', { name, copy_from: copyFrom })
export const updateScheme = (
  id: number,
  body: { name?: string; yaml?: string; simple?: CatSimpleForm; confirm_cat_change?: boolean },
) =>
  http.put<{ message: string; schemes: SchemeView[] }>(`/category/schemes/${id}`, body)
export const deleteScheme = (id: number) => http.del<{ schemes: SchemeView[] }>(`/category/schemes/${id}`)
