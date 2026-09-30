export interface StrmTab {
  /** 地址栏 ?tab= 的值，用户可能收藏过，别改名 */
  key: string
  label: string
}

/**
 * 页签只放配置；「现在怎样」和「开始同步」在页面顶部的状态总览（StrmOverview.vue）。
 * 原来的大号分段页签（图标 + 一句说明）承担了「让人知道点进去会发生什么」的职责，
 * 状态总览接手之后页签换回普通的 HTabs。
 */
export const STRM_TABS: StrmTab[] = [
  { key: 'config', label: 'STRM 配置' },
  { key: 'full', label: '全量同步' },
  { key: 'incr', label: '增量同步' },
  // 深度删除不挂在全量同步下面：它与全量没有依赖关系（失效 STRM 检测才有——
  // 那个标记就是全量同步末尾打的）。摆在一起会让人以为要先跑全量才生效
  { key: 'deepdel', label: '深度删除' },
]
