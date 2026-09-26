import { ref } from 'vue'
import { organizeApi } from '@/api'

/**
 * 整理记录各状态条数。页签标题上的「待确认」角标和记录页的筛选栏共用这一份：
 * 记录页里确认/忽略之后刷新一次，页签角标跟着变，不必各拉各的。
 */
export const recordStats = ref<Record<string, number>>({})

export async function refreshRecordStats() {
  try {
    recordStats.value = (await organizeApi.recordStats()).data ?? {}
  } catch {
    // 角标拉不到不影响使用，列表本身会报错
  }
}
