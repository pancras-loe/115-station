import { configApi } from '@/api'

/**
 * setting `incr` 的两个字段——后端一个 key，前端分在两个页面上。
 *
 * - `cron`：**自动整理**的调度开关，界面在「自动整理 → 基础配置」。
 *   它只管整理流水线，和增量没关系（interval_sec 填 0 的逃生门除外）
 * - `interval_sec`：增量独立轮询间隔，界面在「Strm 管理 → 增量同步」
 *
 * 历史上两件事绑在同一条 cron 上，所以字段同住一个 key；增量拆成独立轮询后
 * key 没动（不值得为搬个输入框做数据迁移），只是配置界面各回各家。
 */
export interface IncrCfg {
  cron: string
  interval_sec: number
}

export const INCR_DEFAULTS: IncrCfg = { cron: '*/10 8-23 * * *', interval_sec: 30 }

export const loadIncrCfg = () => configApi.getSetting<IncrCfg>('incr', INCR_DEFAULTS)

/**
 * 只改自己那个字段。
 *
 * 不能像 useSetting 那样整存整取：两个页面各拿着一份完整对象，
 * 后保存的一边会把另一边刚改的字段写回成自己加载时的旧值
 * （两个浏览器标签页、或者同一会话里先后改两处，都会撞上）。
 * 保存前重新读一次再合并，代价是一次读请求。
 */
export async function patchIncrCfg(patch: Partial<IncrCfg>) {
  const cur = await loadIncrCfg()
  await configApi.saveSetting('incr', { ...cur, ...patch })
}
