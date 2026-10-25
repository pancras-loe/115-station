/**
 * org-basic 这个 setting key 的形状：已存在 / 冗余两个工作目录 + 人工确认开关（「基础配置」页签）。
 * 原来的 pending（待整理）已并进 setting `share`（转存/待整理；媒体库表 V2 下改为每个库各设一个），后端保存时会剥掉它
 */
export interface OrgBasic {
  existing: string
  existing_path: string
  redundant: string
  redundant_path: string
  /** 人工确认：识别完先停在整理记录里，确认后才搬移入库 */
  manual_confirm: boolean
  /** 关掉「同名待确认」：TMDB 上同名的几部分不出来时照旧取第一个（反着存，老配置没有这个键 = 开着） */
  no_twin_hold: boolean
  /** 洗版待确认：手动提交（订阅以外）的不如库里的，先不移「已存在」，停下来问要不要强制洗版 */
  wash_ask: boolean
  /** 同集多份（改名后会重名）等了多少小时没人选就自动处理；0 = 一直等 */
  dup_auto_hours: number
  /** 超时怎么处理：keep_all 都保留（#A #B）/ recommend 有推荐留推荐、没有就都保留 */
  dup_auto_action: 'keep_all' | 'recommend'
}

export const ORG_BASIC_DEFAULTS: OrgBasic = {
  existing: '',
  existing_path: '',
  redundant: '',
  redundant_path: '',
  manual_confirm: false,
  no_twin_hold: false,
  wash_ask: false,
  dup_auto_hours: 0,
  dup_auto_action: 'keep_all',
}
