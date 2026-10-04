import { http } from './client'
import type { QueuedReply } from './tasks'

/** 网盘文件页（后端 internal/api/filebrowser.go / fileorganize.go / filelibrary.go）。刮削在本地文件页（local.ts） */

/** 工作区根目录的角色 */
export type WorkspaceRole = 'library' | 'pending' | 'share' | 'existing' | 'redundant'

export interface FileItem {
  /** 文件是 fid，目录是它自己的 cid */
  id: string
  name: string
  is_dir: boolean
  size?: number
  pickcode?: string
  /** 这个目录本身是哪个工作区根 */
  root?: WorkspaceRole
  /** 按整理认的视频后缀判（只用来挑图标） */
  video?: boolean
  /**
   * 这一行能做什么：后端按面包屑、用入队时同一套校验算好的，前端别再自己判。
   * title = 媒体库里的片目目录（整理 = 重新整理，移动 = 移出媒体库）
   */
  organize?: boolean
  move?: boolean
  title?: boolean
  /** 剧集片目里的视频：能「指定季集」；此时 title_key / title_rel 是所在片目的 */
  episode?: boolean
  /** 片目在本地文件页的 key（?title=） */
  title_key?: string
  /** 片目的库内相对路径（整理记录 ?target_dir=） */
  title_rel?: string
  /** 什么都不能做的原因 */
  block?: string
  /** 能移动、不能整理的原因 */
  organize_block?: string
  /** 本页发起、还没跑完的任务里有它 */
  busy?: 'organize' | 'move'
  /** 那个任务的 id：排队中 / 执行中按任务队列的实时状态显示 */
  busy_job?: number
}

export interface FileList {
  cid: string
  data: FileItem[]
  /** 目录太大，只列了前几千项 */
  truncated?: boolean
  /** 工作区根目录：cid → 角色 */
  roots: Record<string, WorkspaceRole>
  /** 工作区根目录：cid → 网盘绝对路径（解析不出的不在表里）。用来给它的祖先目录标「含 xx」 */
  root_paths?: Record<string, string>
}

export interface Crumb {
  cid: string
  name: string
}

/** refresh 跳过后端 2 分钟的浏览缓存；chain 是面包屑，后端据此算每一行能做什么 */
export const list = (cid: string, chain: Crumb[], refresh = false) =>
  http.get<FileList>('/files/115', {
    params: { cid, chain: JSON.stringify(chain), ...(refresh ? { refresh: 1 } : {}) },
  })

export interface FileJobBody {
  /** 所选条目所在目录 */
  cid: string
  /** 面包屑（不含网盘根）：后端 Cookie 通道取不到祖先链时用它定位 */
  chain: Crumb[]
  items: Pick<FileItem, 'id' | 'name' | 'is_dir' | 'pickcode'>[]
  /** 指定 TMDB 条目（不传 = 自动识别） */
  tmdb_id?: number
  media_type?: 'movie' | 'tv'
  label?: string
}

export const organize = (body: FileJobBody) => http.post<QueuedReply>('/files/organize', body)

/** 片目里一个视频的季集 */
export interface EpisodePick {
  season: number
  episode: number
}

/**
 * 指定季集预览的一行。wash：new 新增集 / replace 洗掉库内旧版 / exists 库内更优、移「已存在」/
 * samefile 库内已有同一份 / coexist 与库内版本共存 / unchanged 没变 / conflict 目标已有同名文件
 */
export interface EpisodePreviewItem extends EpisodePick {
  id: string
  name: string
  /** 季集是按文件名推测的（认不出时拿末段数字当集号） */
  guessed?: boolean
  new_name?: string
  target_rel?: string
  wash?: 'new' | 'replace' | 'exists' | 'samefile' | 'coexist' | 'unchanged' | 'conflict'
  wash_text?: string
}

export interface EpisodePreview {
  title: string
  year: string
  title_rel: string
  /** 这个分类配了洗版策略 */
  strategy: boolean
  items: EpisodePreviewItem[]
  /** 季集或模板有问题（items 仍带着预填值，可改） */
  error?: string
}

type EpisodeBody = FileJobBody & { episodes?: Record<string, EpisodePick> }

/** 预填季集 + 新名 + 洗版判定；episodes 里没有的按文件名预填。不发 115 请求 */
export const previewEpisodes = (body: EpisodeBody) => http.post<EpisodePreview>('/files/library/episodes/preview', body)

export const submitEpisodes = (body: EpisodeBody & { episodes: Record<string, EpisodePick> }) =>
  http.post<QueuedReply>('/files/library/episodes', body)

/** 移动目标：只能是这三个工作区 */
export type MoveTarget = 'redundant' | 'existing' | 'pending'

export const move = (body: FileJobBody & { target: MoveTarget }) => http.post<QueuedReply>('/files/move', body)
