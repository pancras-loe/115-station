import { computed, ref } from 'vue'
import { systemApi } from '@/api'
import type { UpdateStatus } from '@/api/system'

/**
 * 更新检测结果。侧栏底部的「有新版本」小点和设置页的「版本更新」页签共用这一份：
 * 设置页里点了「检查更新」，侧栏跟着变。后端每 12 小时查一次 GitHub，这里只读缓存。
 */
export const updateStatus = ref<UpdateStatus | null>(null)

export const hasUpdate = computed(() => !!updateStatus.value?.has_update)

export async function refreshUpdateStatus() {
  try {
    updateStatus.value = await systemApi.updateStatus()
  } catch {
    // 拿不到不影响使用
  }
}

/** 立刻查一次，失败抛给调用方提示 */
export async function checkUpdateNow() {
  updateStatus.value = await systemApi.checkUpdate()
  return updateStatus.value
}

/**
 * 界面上显示的版本号：正式版 v1.2.0 原样；两版之间的构建 v1.2.0-3-gabc1234 原样（能看出比 v1.2.0 新几个提交）；
 * 还没打过 tag 时后端给的是提交号，截成 7 位；本地构建是 dev
 */
export function displayVersion(v?: string) {
  if (!v) return ''
  if (/^[0-9a-f]{12,}$/.test(v)) return v.slice(0, 7)
  return v
}
