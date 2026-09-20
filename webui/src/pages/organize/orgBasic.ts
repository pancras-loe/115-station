/**
 * org-basic 这一个 setting key 同时装着整理的三个工作目录和「媒体补全」策略，
 * 而这两块分在「基础配置」「媒体补全」两个页签上。useSetting 是整对象覆盖存，
 * 所以两边必须共用同一份默认值——否则先打开的那个页签保存时会把另一块抹成空。
 */
export interface OrgBasic {
  pending: string
  pending_path: string
  existing: string
  existing_path: string
  redundant: string
  redundant_path: string
  enrich: {
    enabled: boolean
    mode: string
    missing: string
    conflict_low: string
    conflict_high: string
    full_named: string
  }
}

export const ORG_BASIC_DEFAULTS: OrgBasic = {
  pending: '',
  pending_path: '',
  existing: '',
  existing_path: '',
  redundant: '',
  redundant_path: '',
  enrich: {
    enabled: false,
    mode: 'standard',
    missing: 'rename',
    conflict_low: 'rename',
    conflict_high: 'rename',
    full_named: 'keep',
  },
}
