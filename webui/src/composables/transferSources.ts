import { ref } from 'vue'
import { transferApi } from '@/api'
import type { SourceKey, TransferSources } from '@/api/transfer'
import { toastError } from '@/composables/useFeedback'

/**
 * 影视转存的来源状态，搜索页与来源设置页共用一份：
 * 在设置页登录 / 关掉某个来源，切回搜索页马上生效，不用刷新。
 */
const state = ref<TransferSources | null>(null)

async function reload() {
  try {
    state.value = await transferApi.sources()
  } catch (e) {
    toastError(e, '读取来源状态失败')
  }
}

async function setEnabled(key: SourceKey, on: boolean) {
  const list = state.value?.sources ?? []
  const disabled = list.filter((s) => (s.key === key ? !on : !s.enabled)).map((s) => s.key)
  try {
    await transferApi.saveSources(disabled)
    for (const s of list) if (s.key === key) s.enabled = on
  } catch (e) {
    toastError(e, '保存失败')
  }
}

export function useTransferSources() {
  if (!state.value) reload()
  return { state, reload, setEnabled }
}
