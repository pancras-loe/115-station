/**
 * 资源条件的预设标签：界面上点选，存的仍是洗版规则那种逗号串（「!」开头排除），后端 subcond.go 解析。
 *
 * 一个标签对应一组同义写法：后端按「归一值包含」或「名字里单独出现」判命中，
 * 所以 HEVC / x265 / H.265 都要列上（H.265 靠归一值 H265 命中），中文资源名常见的写法（蓝光、杜比视界、全景声）也列上
 */
export type CondKey = 'pix' | 'type' | 'effect' | 'video' | 'audio'

export interface CondPreset {
  label: string
  tokens: string[]
}

export const COND_PRESETS: Record<CondKey, { label: string; presets: CondPreset[] }> = {
  pix: {
    label: '分辨率',
    presets: [
      { label: '4K', tokens: ['2160p', '4k'] },
      { label: '1080p', tokens: ['1080p', '1080i'] },
      { label: '720p', tokens: ['720p'] },
      { label: '8K', tokens: ['4320p', '8k'] },
    ],
  },
  type: {
    label: '质量',
    presets: [
      { label: 'WEB-DL', tokens: ['web-dl', 'webdl'] },
      { label: 'WEBRip', tokens: ['webrip', 'web-rip'] },
      { label: '蓝光', tokens: ['bluray', 'blu-ray', '蓝光'] },
      { label: 'REMUX', tokens: ['remux'] },
      { label: 'HDTV', tokens: ['hdtv'] },
    ],
  },
  effect: {
    label: '特效',
    presets: [
      { label: '杜比视界', tokens: ['dv', 'dovi', '杜比视界'] },
      { label: 'HDR10+', tokens: ['hdr10+'] },
      { label: 'HDR', tokens: ['hdr'] },
      { label: 'SDR', tokens: ['sdr'] },
    ],
  },
  video: {
    label: '视频编码',
    presets: [
      { label: 'H265 / HEVC', tokens: ['h265', 'x265', 'hevc'] },
      { label: 'H264 / AVC', tokens: ['h264', 'x264', 'avc'] },
      { label: 'AV1', tokens: ['av1'] },
    ],
  },
  audio: {
    label: '音频编码',
    presets: [
      { label: '杜比全景声', tokens: ['atmos', '全景声'] },
      { label: 'TrueHD', tokens: ['truehd'] },
      { label: 'DTS-HD / DTS:X', tokens: ['dts-hd', 'dtshd', 'dts-x', 'dts.x'] },
      { label: 'DTS', tokens: ['dts'] },
      { label: 'DDP / E-AC3', tokens: ['ddp', 'eac3', 'dd+'] },
      { label: 'AAC', tokens: ['aac'] },
      { label: 'FLAC', tokens: ['flac'] },
    ],
  },
}

/** 一个标签的状态：不管 / 要 / 不要 */
export type PresetState = 'off' | 'want' | 'not'

export interface ParsedCond {
  states: PresetState[]
  /** 认不出属于哪个标签的写法（以前手填的），原样保留、可以删 */
  extra: string[]
}

function splitParts(s: string): string[] {
  return s
    .split(/[,，]/)
    .map((p) => p.trim())
    .filter(Boolean)
}

/** 逗号串 → 每个标签的状态 + 剩下的写法。一个标签的写法全在「要」里算要，全在「不要」里算不要 */
export function parseCond(s: string, presets: CondPreset[]): ParsedCond {
  const parts = splitParts(s)
  const pos = new Set(parts.filter((p) => !p.startsWith('!')).map((p) => p.toLowerCase()))
  const neg = new Set(parts.filter((p) => p.startsWith('!')).map((p) => p.slice(1).trim().toLowerCase()))
  const used = new Set<string>()
  const states = presets.map((p): PresetState => {
    if (p.tokens.every((t) => pos.has(t))) {
      p.tokens.forEach((t) => used.add(t))
      return 'want'
    }
    if (p.tokens.every((t) => neg.has(t))) {
      p.tokens.forEach((t) => used.add('!' + t))
      return 'not'
    }
    return 'off'
  })
  const extra = parts.filter((p) => {
    const key = p.startsWith('!') ? '!' + p.slice(1).trim().toLowerCase() : p.toLowerCase()
    return !used.has(key)
  })
  return { states, extra }
}

export function serializeCond(c: ParsedCond, presets: CondPreset[]): string {
  const out: string[] = []
  presets.forEach((p, i) => {
    if (c.states[i] === 'want') out.push(...p.tokens)
    if (c.states[i] === 'not') out.push(...p.tokens.map((t) => '!' + t))
  })
  out.push(...c.extra)
  return out.join(',')
}

export const nextState = (s: PresetState): PresetState => (s === 'off' ? 'want' : s === 'want' ? 'not' : 'off')
