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
}

export const ORG_BASIC_DEFAULTS: OrgBasic = {
  pending: '',
  pending_path: '',
  existing: '',
  existing_path: '',
  redundant: '',
  redundant_path: '',
  manual_confirm: false,
}
