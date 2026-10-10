import type { EmbyDetailItem, EmbyTrack, ProbeLimits, ProbeState } from '@/api/local'

/**
 * 海报墙片目详情：把 Emby 的轨道信息翻成人话（4K · HEVC · 杜比视界 / 中文 E-AC3 5.1 …），
 * 以及提前探测状态的文案。Emby 自己的 DisplayTitle 也能用，但中英混杂、不同版本写法不一，这里统一口径。
 */

export function resolutionLabel(t: EmbyTrack): string {
  const w = t.width ?? 0
  const h = t.height ?? 0
  if (!w && !h) return ''
  // 宽度优先：2.39:1 的 4K 电影高度只有 1600 上下，按高度算会被认成 2K
  if (w >= 3800 || h >= 2100) return '4K'
  if (w >= 2500 || h >= 1400) return '2K'
  if (w >= 1900 || h >= 1000) return '1080p'
  if (w >= 1260 || h >= 700) return '720p'
  return h ? `${h}p` : `${w}w`
}

const VIDEO_CODECS: Record<string, string> = {
  hevc: 'HEVC', h265: 'HEVC', h264: 'H.264', avc: 'H.264', av1: 'AV1', vp9: 'VP9', mpeg2video: 'MPEG-2',
  mpeg4: 'MPEG-4', vc1: 'VC-1',
}
const AUDIO_CODECS: Record<string, string> = {
  aac: 'AAC', ac3: 'AC3', eac3: 'E-AC3', truehd: 'TrueHD', dts: 'DTS', flac: 'FLAC', opus: 'Opus', mp3: 'MP3',
  pcm_s16le: 'PCM', pcm_s24le: 'PCM', vorbis: 'Vorbis', alac: 'ALAC',
}
const SUB_CODECS: Record<string, string> = {
  ass: 'ASS', ssa: 'SSA', subrip: 'SRT', srt: 'SRT', pgssub: 'PGS', hdmv_pgs_subtitle: 'PGS', dvdsub: 'VobSub',
  dvd_subtitle: 'VobSub', mov_text: 'TX3G', webvtt: 'VTT', vtt: 'VTT', sup: 'PGS',
}

function codec(map: Record<string, string>, c?: string) {
  if (!c) return ''
  return map[c.toLowerCase()] ?? c.toUpperCase()
}

export function rangeLabel(r?: string): string {
  if (!r) return ''
  const k = r.toLowerCase().replace(/[\s_-]/g, '')
  if (k.includes('dolbyvision') || k === 'dovi' || k === 'dv') return '杜比视界'
  if (k.includes('hdr10plus') || k.includes('hdr10+')) return 'HDR10+'
  if (k.includes('hdr10')) return 'HDR10'
  if (k.includes('hlg')) return 'HLG'
  if (k.includes('hdr')) return 'HDR'
  return '' // SDR 不必写
}

const LANGS: Record<string, string> = {
  chi: '中文', zho: '中文', zh: '中文', chs: '简中', cht: '繁中', yue: '粤语', eng: '英语', en: '英语',
  jpn: '日语', ja: '日语', kor: '韩语', ko: '韩语', fre: '法语', fra: '法语', ger: '德语', deu: '德语',
  spa: '西班牙语', rus: '俄语', ita: '意大利语', tha: '泰语', und: '',
}

/** 语言：中文字幕再细分简繁（从轨道标题猜），其余用 Emby 给的显示名或常见代码 */
export function langLabel(t: EmbyTrack): string {
  const title = `${t.title ?? ''} ${t.display ?? ''}`.toLowerCase()
  const code = (t.language ?? '').toLowerCase()
  const zh = ['chi', 'zho', 'zh', 'chs', 'cht'].includes(code) || /中|chinese/.test(title)
  if (zh) {
    if (/简|chs|\bsc\b|gb|simplified/.test(title)) return '简中'
    if (/繁|cht|\btc\b|big5|traditional/.test(title)) return '繁中'
    if (/粤|cantonese/.test(title)) return '粤语'
  }
  if (code && code in LANGS) return LANGS[code] || '未知语言'
  return t.lang_name || (code ? code.toUpperCase() : '未知语言')
}

export function channelsLabel(t: EmbyTrack): string {
  switch (t.channels) {
    case undefined:
    case 0:
      return t.layout ?? ''
    case 1:
      return '单声道'
    case 2:
      return '2.0'
    case 6:
      return '5.1'
    case 8:
      return '7.1'
    default:
      return `${t.channels} 声道`
  }
}

export function bitrateLabel(b?: number): string {
  if (!b) return ''
  if (b >= 1_000_000) return `${(b / 1_000_000).toFixed(1)} Mbps`
  return `${Math.round(b / 1000)} kbps`
}

export function durationLabel(sec?: number): string {
  if (!sec) return ''
  const h = Math.floor(sec / 3600)
  const m = Math.round((sec % 3600) / 60)
  return h ? `${h} 小时 ${m} 分` : `${m} 分钟`
}

/** 视频轨一行：4K · HEVC · 杜比视界 · 10bit · 23.976fps */
export function videoParts(t: EmbyTrack): string[] {
  const parts = [resolutionLabel(t), codec(VIDEO_CODECS, t.codec), rangeLabel(t.range)]
  if (t.bit_depth && t.bit_depth > 8) parts.push(`${t.bit_depth}bit`)
  if (t.fps) parts.push(`${Math.round(t.fps * 1000) / 1000}fps`)
  return parts.filter(Boolean)
}

/** 音轨：中文 · E-AC3 · 5.1 */
export function audioParts(t: EmbyTrack): string[] {
  return [langLabel(t), codec(AUDIO_CODECS, t.codec), channelsLabel(t)].filter(Boolean)
}

/** 字幕：简中 · ASS */
export function subtitleParts(t: EmbyTrack): string[] {
  return [langLabel(t), codec(SUB_CODECS, t.codec)].filter(Boolean)
}

/** 列表里一行的视频摘要：4K HEVC 杜比视界 */
export function videoBrief(it: EmbyDetailItem): string {
  const v = it.video[0]
  if (!v) return ''
  return [resolutionLabel(v), codec(VIDEO_CODECS, v.codec), rangeLabel(v.range)].filter(Boolean).join(' ')
}

/** 列表里的音轨 / 字幕语言摘要：去重后最多三个 */
export function langBrief(ts: EmbyTrack[]): string {
  const seen = [...new Set(ts.map(langLabel))]
  return seen.length > 3 ? `${seen.slice(0, 3).join(' / ')} 等` : seen.join(' / ')
}

// ---- 提前探测状态 ----

export type Tone = 'default' | 'accent' | 'success' | 'warning' | 'danger'

export const PROBE_TEXT: Record<ProbeState, string> = {
  done: '已探测',
  none: '未探测',
  queued: '排队中',
  running: '探测中',
  retry: '可重试',
  wait: '近期已请求',
  exhausted: '自动已停',
  disc: '不探测',
}

export const PROBE_TONE: Record<ProbeState, Tone> = {
  done: 'success',
  none: 'default',
  queued: 'accent',
  running: 'accent',
  retry: 'warning',
  wait: 'warning',
  exhausted: 'danger',
  disc: 'default',
}

function when(s?: string) {
  if (!s) return ''
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return s
  const same = d.toDateString() === new Date().toDateString()
  const hm = d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false })
  return same ? `今天 ${hm}` : `${d.getMonth() + 1} 月 ${d.getDate()} 日 ${hm}`
}

/** 一句话说清楚这个条目为什么是这个状态、接下来会怎样 */
export function probeHint(p: EmbyDetailItem['probe'], limits?: ProbeLimits): string {
  const err = p.last_err && p.last_err !== '请求中' ? `（${p.last_err}）` : ''
  // 手动探测只看防抖：什么时候能点
  const manual =
    p.manual_at && new Date(p.manual_at).getTime() > Date.now()
      ? `${when(p.manual_at)} 之后可以手动探测（同一视频 ${limits?.debounce_minutes ?? 5} 分钟内不重复请求）`
      : '现在就可以手动探测'
  switch (p.state) {
    case 'done':
      return 'Emby 已经有这个视频的音视频信息，播放时不用再现场探测。'
    case 'none':
      return 'Emby 还没有这个视频的媒体信息：第一次播放时会现场探测，起播会慢几秒。'
    case 'queued':
      return '已在提前探测队列里，后台逐个进行。'
    case 'running':
      return '正在请 Emby 探测这个视频。'
    case 'retry':
      return `上次请求失败${err}。再次入库确认时会自动再试一次（第 ${(p.attempts ?? 0) + 1} 次，也是自动的最后一次）；${manual}。`
    case 'wait':
      return `已请求过 ${p.attempts ?? 1} 次${err}。自动探测 ${when(p.retry_at)} 之后才会再试；${manual}。`
    case 'exhausted':
      if (p.ignored) return `上次请求失败${err}，已在任务中心忽略，不再自动探测；${manual}（手动探测后若再失败，会重新出现在任务中心）。`
      return `已请求 ${p.attempts ?? limits?.max_attempts ?? 2} 次都没成功${err}，不再自动探测；${manual}，或先在 Emby 里播放一次看能否正常读取。`
    case 'disc':
      return '光盘结构（ISO / BDMV），Emby 探测不了，不占用 115 请求。'
  }
}

export interface ProbeErrAdvice {
  /** 一句话结论：是谁的问题 */
  title: string
  /** 为什么会这样 */
  why: string
  /** 用户能做什么 */
  todo: string
}

/**
 * 探测失败原因 → 给用户看的解释与处理建议。认的是后端写死的两句原文
 * （internal/api/embyextract.go 的 embyProbeErrUnreadable / embyProbeErrNoStreams），
 * 多版本时前面会带「版本名：」，所以按包含判断。认不出的返回 null，界面只显示原文。
 *
 * 不给提示的话，用户只看到「失败」，要去翻 Emby 日志才知道是文件里有 Emby 不支持的编码，
 * 不少人会以为是本站坏了（2026-10-02《鱿鱼游戏》S02 的 xHE-AAC 音轨）。
 */
export function probeErrAdvice(err?: string): ProbeErrAdvice | null {
  if (!err) return null
  const unreadable = '直链正常，Emby 的 ffprobe 读不了这个文件'
  const at = err.indexOf(unreadable)
  if (at >= 0) {
    // 后端在 Emby 日志里找到了具体原因时，接在后面：「：音轨 #4（chi「台配国语」AAC）：Emby 内置的 ffprobe 不支持 …」
    // 多个版本的失败原因之间用「；」隔开，只取这一段
    const rest = err.slice(at + unreadable.length)
    const detail = rest.startsWith('：') ? rest.slice(1).split('；')[0].trim() : ''
    const fix = '先在 Emby 里试播一次：能播就点「忽略」，只是 Emby 里看不到轨道信息；播不了就换一个资源，'
    if (detail) {
      return {
        title: '文件本身的问题，不是本站或 115 的问题',
        why: `Emby 日志里的报错：${detail}。本站已经正常给出 115 直链，是 Emby 读不了文件里的这条轨道，重试多少次结果都一样。`,
        todo: fix + '或者用 mkvmerge 去掉这条轨道后重新上传（不用重新编码）。',
      }
    }
    return {
      title: '文件本身的问题，不是本站或 115 的问题',
      why:
        '本站已经正常给出 115 直链，Emby 也读到了文件，但它自带的 ffprobe 解析不了。' +
        '常见原因是文件里有它不支持的编码，比如 xHE-AAC 音轨：Emby 内置的 ffprobe 是 5.1 版，要 7.1 以上才支持。重试多少次结果都一样。',
      todo:
        fix +
        '或者用 mkvmerge 去掉出问题的那条轨道后重新上传（不用重新编码）。想知道是哪条轨道，' +
        '在 Emby 日志里搜这个文件名，看 ffprobe 报错里带「not implemented」的那几行（本站读 Emby 日志需要管理员权限的 API Key）。',
    }
  }
  if (err.includes('没提取到音视频轨道')) {
    return {
      title: 'Emby 没能通过 STRM 读到文件',
      why: 'Emby 返回了，但没有任何轨道，而且探测期间本站没有给出过 115 直链：Emby 多半没访问到 STRM 里的地址，或者本站取直链失败了。',
      todo:
        '检查 STRM 里的地址（本站的地址和端口）在 Emby 所在机器上能不能访问；再看本站日志同一时间有没有「[播放] ✗ 取链失败」。' +
        '本站更新前记下的失败分不清是这种情况还是文件本身读不了，重试一次就能分清。',
    }
  }
  return null
}
