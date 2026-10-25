import { http } from './client'
import type { QueuedReply } from './tasks'

export type FullSyncMode = 'normal' | 'fast'

/** 同步接口的请求体（/sync/full 与 /sync/incremental 共用） */
export interface SyncExtConfig {
  cid: string
  local_path: string
  video_ext: string[]
  image_ext: string[]
  data_ext: string[]
  /** 全量同步模式；增量同步忽略此字段 */
  mode?: FullSyncMode
}

/** setting「full」的持久化结构（与后端 fullSyncCfg 同构） */
/** 附加媒体库的一项（后端 libroots.go 的 libRootEntry） */
export interface ExtraLib {
  cid: string
  /** 仅用于配置页回显；同步仍以 cid 为准 */
  path?: string
}

export interface FullSyncConfig {
  /** 主媒体库：分类规则没写 library 的都整理到这里 */
  cid: string
  /** 仅用于配置页回显；同步仍以 cid 为准 */
  cid_path?: string
  /**
   * 附加媒体库：全量 / 增量同步、海报墙、深度删除和主库一样管。
   * 本地 STRM 第一层是库的本地目录名（lib_opts，默认取 115 目录名），各库不能重名（后端保存时校验）
   */
  extra_libs?: ExtraLib[]
  local_path: string
  video_ext: string[]
  image_ext: string[]
  data_ext: string[]
  mode: FullSyncMode
  /** 失效 STRM 检测：全量同步后标出「本地还在、网盘已删」的条目，只打标不删 */
  detect_orphans: boolean
  /** 全量同步结束后通知 Emby 刷新。全量传的是媒体库根 = 根下每个库整库扫一遍，很贵，默认关 */
  refresh_emby: boolean
  /** 定时全量开关。只在 detect_orphans 打开时生效，后端同样按此判定 */
  cron_enabled: boolean
  cron: string
}

/**
 * 深度删除配置（setting「deepdel」，与后端 deepDelCfg 同构）。
 *
 * 独立于 full：深度删除和全量同步没有依赖关系，界面也不在同一个页签，
 * 共用一个 setting key 会让两个页面整存整取时互相覆盖。
 *
 * 后端字段是指针（区分「没配过」和「显式填零值」），前端一律整份下发明确值，
 * 所以这里都是必填 —— 默认值见 deepDelSetting.ts。
 */
export interface DeepDeleteConfig {
  enabled: boolean
  prune_pan_dirs: boolean
  notify: boolean
}

/** 全量同步 → 入任务队列立即返回；结果（新增 STRM、失效 STRM 数、是否降级）在任务结束时由队列带回 */
export const runFull = (body: SyncExtConfig) => http.post<QueuedReply>('/sync/full', body)

/** 接管已有 STRM：预演报告的一个桶（后端 adopt.go 的 adoptBucket） */
export type AdoptBucketKey =
  | 'adopt'
  | 'rename'
  | 'already'
  | 'moved'
  | 'dup'
  | 'missing'
  | 'share'
  | 'unknown'
  | 'notlocal'

export interface AdoptSample {
  /** 本地 STRM（相对本地媒体库目录；不在它下面时是绝对路径） */
  local?: string
  /** 本站会写成的位置（相对本地媒体库目录） */
  expect?: string
  /** 网盘文件（库内相对路径） */
  remote?: string
  note?: string
}

export interface AdoptReport {
  local_root: string
  scan_dir: string
  local_strm: number
  remote_videos: number
  libs: { cid: string; name: string; videos: number; mode?: string; error?: string }[]
  /** 各项目写法各几个 */
  formats: Record<string, number>
  buckets: { key: AdoptBucketKey; count: number; samples: AdoptSample[] }[]
  /** 「位置不同」的前缀换算：本地 from 下面的东西，本站会写在 to 下面 */
  shifts: { from: string; to: string; count: number }[]
  /** 「网盘上没有」里不是这个 115 账号的个数 */
  other_account: number
  /** ok 可以接管；move 多数位置对不上；empty 一个都没对上 */
  verdict: 'ok' | 'move' | 'empty'
  /** 执行的结果（只在执行任务里有） */
  applied?: AdoptApplied
}

export interface AdoptApplied {
  /** 登记进台账的视频 */
  adopted: number
  /** 其中沿用非本站写法文件名的 */
  renamed: number
  /** 内容改写成本站直链的 */
  rewritten: number
  /** 内容本来就是本站直链的 */
  unchanged: number
  failed: number
  /** 登记的本地已有附属文件 */
  assets: number
  full_job_id?: number
}

/** 入队一次只读预演，结果在任务详情的 result 里 */
export const adoptPreview = (body: { scan_dir?: string }) => http.post<QueuedReply>('/sync/adopt/preview', body)
/** 入队执行接管：后端重新扫一遍、对一遍号再接管，不拿上一次预演的结果 */
export const adoptApply = (body: { scan_dir?: string; then_full?: boolean }) =>
  http.post<QueuedReply>('/sync/adopt/apply', body)

/** 一组替换：内容以 from 开头的，这一段换成 to */
export interface StrmRebasePair {
  from: string
  to: string
}

/** 批量替换 STRM 地址：预演与执行共用的报告（后端 strmrebase.go 的 strmRebaseReport） */
export interface StrmRebaseReport {
  /** 本地媒体根 */
  root: string
  /** 扫到的 .strm */
  scanned: number
  unreadable: number
  /** 按地址开头分组（多的在前）；station = 本站格式的直链 */
  groups: { base: string; count: number; station?: boolean }[]
  /** 每组替换命中几个 */
  pairs: { from: string; to: string; count: number }[]
  matched: number
  samples: { rel: string; old: string; new: string }[]
  /** 已保存的直连域名（没填过是空） */
  domain: string
  /** 只在执行结果里有 */
  rewritten: number
  failed: number
}

/** 只读预演：读本地 STRM，零 115 请求。pairs 为空时只给分组。库大、本地是网络盘时要读一会儿 */
export const strmRebasePreview = (pairs: StrmRebasePair[] = []) =>
  http.post<StrmRebaseReport>('/sync/strm-rebase/preview', { pairs }, { timeoutMs: 180_000 })
/** 入队替换 */
export const strmRebase = (pairs: StrmRebasePair[]) => http.post<QueuedReply>('/sync/strm-rebase', { pairs })

export interface IncrSummary {
  /** 轮次号，与日志里的 [同步#N] 对应 */
  round: number
  events_total: number
  events_fresh: number
  /** 本轮实际要处理的条数（含上轮遗留） */
  events_pending: number
  relevant: number
  structural: number
  deleted: number
  moved: number
  dirs: number
  videos: number
  strm_created: number
  /** 本地已有且内容一致，没动 */
  strm_existing: number
  assets_total: number
  assets_downloaded: number
  assets_skipped: number
  assets_failed: number
  /** 非媒体库区域（待整理/已存在/冗余等）的事件 */
  ignored: number
  elapsed: string

  /** 回退遍历的范围与代价：排查「这一轮为什么跑了十几分钟」 */
  dirs_shallow: number
  dirs_deep: number
  dirs_merged: number
  dirs_visited: number
  /** 列目录请求次数，每次都要等节流 */
  list_calls: number

  /** 临时读不到：唯一会让本轮不消费、下轮重来的 */
  dirs_skipped: number
  /** 永久：目录不在媒体库内 */
  dirs_outside: number
  /** 永久：目录已在网盘上删除 */
  dirs_gone: number
  /** 永久：事件没带父目录 */
  dirs_no_parent: number

  /** 被让路中断（有别的任务在排队等锁） */
  interrupted: boolean
  yielded_to?: string
  /** 事件是否已标记消费、游标是否推进；false = 下一轮原样重放 */
  consumed: boolean
  not_consumed?: string
  pending_left: number
  stall_rounds: number
}

/** 手动增量 → 入任务队列立即返回 */
export const runIncremental = (body: SyncExtConfig) => http.post<QueuedReply>('/sync/incremental', body)

export interface SyncCapabilities {
  fast_available: boolean
  /** 不可用时的原因，直接展示给用户 */
  reason: string
}

export const capabilities = () => http.get<SyncCapabilities>('/sync/capabilities', { timeoutMs: 15_000 })

export interface OrphanEntry {
  rel_path: string
  kind: string
  size: number
  marked_at: string
}

/** 失效 STRM 报告（接口沿用 orphan 命名，界面一律叫「失效 STRM」） */
export interface OrphanReport {
  enabled: boolean
  total: number
  ledger_total: number
  /** 失效条目占台账总数的比例，异常偏高时前端要拦一下 */
  ratio: number
  sample: OrphanEntry[]
  sample_limit: number
}

export const orphans = () => http.get<OrphanReport>('/sync/orphans', { timeoutMs: 15_000 })

export const cleanOrphans = () =>
  http.post<{ message?: string; removed: number; missing: number; failed: number }>(
    '/sync/orphans/clean',
    {},
    { timeoutMs: 10 * 60_000 },
  )

export interface DeepDeleteRecord {
  id: number
  reason: string
  title: string
  video_cnt: number
  asset_cnt: number
  pan_dirs: number
  status: string
  message: string
  rel_paths: string[]
  created_at: string
  /** 删了哪几集：S02E05 / S02E01–E03 / S01；电影为空 */
  episodes?: string
  media_type?: 'movie' | 'tv' | string
  /** 片目目录里还有海报时的缩略图查询串，拼成 /api/local/poster?… */
  poster?: string
}

export type DeepDeleteStatus = 'done' | 'rejected' | 'failed'

export interface DeepDeletePage {
  data: DeepDeleteRecord[]
  total: number
  page: number
  size: number
  /** 近 30 天：各状态次数，已删的视频数与顺带清掉的网盘空目录数 */
  stats30: { done: number; rejected: number; failed: number; videos: number; pan_dirs: number }
  /** 各状态全部的条数（筛选按钮上的数字） */
  counts: Partial<Record<DeepDeleteStatus | 'all', number>>
}

export const deepDeleteRecords = (page = 1, size = 20, status?: DeepDeleteStatus) =>
  http.get<DeepDeletePage>('/sync/deep-delete/records', {
    params: status ? { page, size, status } : { page, size },
    timeoutMs: 15_000,
  })

/** 增量事件流状态：回答「为什么没同步」 */
export interface IncrStatus {
  life_gate: { ok: boolean; message: string; checked_at?: string }
  /** 当前走的是主通道还是被限流后的备用通道 */
  endpoint: string
  cursor: { from_id?: string; from_time?: string }
  last_round: { at?: string; error?: string; summary?: IncrSummary }
  /** 还没消费完的事件数；持续不降说明有目录一直读不出来 */
  pending_events: number
  path_cache: number
  interval_sec: number
  /** 任务互斥锁：整理/同步/全量都排这一条队，谁占着、谁在等 */
  task_lock: {
    busy: boolean
    holder: string
    describe: string
    running_sec?: number
    waiting?: string[]
    waited_sec?: number
  }
  /** 连续多少轮没消费掉事件；>0 说明日志里反复出现的内容是重放 */
  stall: { rounds: number; reason: string; since?: string }
}

export const incrStatus = () => http.get<IncrStatus>('/sync/incr-status', { timeoutMs: 15_000 })

export interface ProbeEvent {
  id: string
  type: string
  name: string
  kind: string
  at: string
  /** 浏览/标星类，拉取时会被丢掉 */
  ignored: boolean
}

/** 只拉不处理：不碰游标、不落库、不动本地文件 */
export const incrProbe = () =>
  http.post<{ message: string; events: ProbeEvent[] }>('/sync/incr-probe', {}, { timeoutMs: 60_000 })

export const cronPreview = (cron: string) => http.post<{ next: string[] }>('/sync/cron-preview', { cron })


/** 最近的增量事件（后端 increvents.go）：只读事件表，零 115 请求 */
export interface IncrEvent {
  id: number
  at: string
  /** 115 生活事件类型：upload_file / receive_files / move_file / file_rename / delete_file … */
  type: string
  is_dir: boolean
  name: string
  /** 所在目录：路径缓存里有才给 */
  path?: string
  size: number
  /** 0 = 主号 */
  account: number
  status: 'pending' | 'applied' | string
  /** self = 整理自己产生、已跳过；outside = 不涉及媒体库；空 = 照常处理 */
  note: '' | 'self' | 'outside' | string
}

export type IncrEventGroup = 'all' | 'add' | 'del' | 'move'

export const incrEvents = (params: { group: IncrEventGroup; hide_self: boolean; page: number; size: number }) =>
  http.get<{ data: IncrEvent[]; total: number; counts: Record<IncrEventGroup, number>; hidden: number }>(
    '/sync/incr-events',
    { params: { ...params, hide_self: params.hide_self ? 1 : 0 }, timeoutMs: 15_000 },
  )

/** 最近有事的增量轮次（内存里，重启清空） */
export interface IncrRound {
  at: string
  error?: string
  summary?: IncrSummary
}

export const incrRounds = () =>
  http.get<{ rounds: IncrRound[]; total: number; idle: number; since?: string }>('/sync/incr-rounds', { timeoutMs: 15_000 })

/** Emby Webhook 收到过什么（后端 embywebhookseen.go）：Emby 查不到它的 Webhook 配置，只能看本站收到过什么 */
export interface EmbyWebhookStatus {
  /** 本站生成了 Webhook token 没有（到「系统配置 → EMBY 管理」打开一次就会生成） */
  token_set: boolean
  /** 最近一次收到任何事件 / 删除事件 / Emby 的测试事件（RFC3339，没有为空） */
  any: string
  delete: string
  test: string
  /** 最近一次因 token / 格式不对被拒 */
  reject_at: string
  reject_why: string
}

export const embyWebhookStatus = () => http.get<EmbyWebhookStatus>('/emby/webhook-status', { timeoutMs: 15_000 })

/** 上一次全量用的三组后缀（setting full_last_scan，全量结束时写） */
export interface FullLastScan {
  at: number
  video_ext: string[]
  image_ext: string[]
  data_ext: string[]
}
