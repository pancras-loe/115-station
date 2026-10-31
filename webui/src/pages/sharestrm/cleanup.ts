import { GONE_CONFIRM, type ShareSource } from '@/api/sharestrm'

/** 待清理：认定失效（连续 3 次）、没点过「先留着」的分享。页签角标与待清理列表共用 */
export function cleanupList(list: ShareSource[]) {
  return list.filter((s) => s.state === 'gone' && s.gone_streak >= GONE_CONFIRM && !s.ignored)
}
