/** org-basic 这个 setting key 的形状：整理的三个工作目录 + 人工确认开关（「基础配置」页签） */
export interface OrgBasic {
  pending: string
  pending_path: string
  existing: string
  existing_path: string
  redundant: string
  redundant_path: string
  /** 人工确认：识别完先停在整理记录里，确认后才搬移入库 */
  manual_confirm: boolean
  /** 同集多份（改名后会重名）等了多少小时没人选就自动处理；0 = 一直等 */
  dup_auto_hours: number
  /** 超时怎么处理：keep_all 都保留（#A #B）/ recommend 有推荐留推荐、没有就都保留 */
  dup_auto_action: 'keep_all' | 'recommend'
}

export const ORG_BASIC_DEFAULTS: OrgBasic = {
  pending: '',
  pending_path: '',
  existing: '',
  existing_path: '',
  redundant: '',
  redundant_path: '',
  manual_confirm: false,
  dup_auto_hours: 0,
  dup_auto_action: 'keep_all',
}
