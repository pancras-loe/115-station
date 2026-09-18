import { onMounted, ref } from 'vue'
import { configApi } from '@/api'
import { toastError, useFeedback } from './useFeedback'

/**
 * 绑定一个 Setting 表的配置项。
 *
 * 把「读 → 填表单 → 存 → 重置为默认值」这套在旧前端里每个配置卡都抄一遍的
 * 流程收成一处。默认值对象同时承担三个角色：类型来源、缺字段时的兜底、
 * 以及「重置配置」按下时写回的内容。
 */
export function useSetting<T extends object>(key: string, defaults: T) {
  const model = ref<T>({ ...defaults })
  const loading = ref(true)
  const saving = ref(false)

  async function load() {
    loading.value = true
    try {
      model.value = await configApi.getSetting<T>(key, { ...defaults })
    } catch (e) {
      toastError(e, '配置读取失败')
    } finally {
      loading.value = false
    }
  }

  async function save(transform?: (v: T) => unknown) {
    saving.value = true
    try {
      await configApi.saveSetting(key, transform ? transform(model.value as T) : model.value)
      useFeedback().message.success('保存成功')
      return true
    } catch (e) {
      toastError(e, '保存失败')
      return false
    } finally {
      saving.value = false
    }
  }

  /** 写回默认值并落库——旧前端的「重置配置」也是直接存默认值，不是仅清空表单 */
  async function reset() {
    saving.value = true
    try {
      await configApi.saveSetting(key, defaults)
      model.value = { ...defaults }
      useFeedback().message.success('配置已恢复默认值')
    } catch (e) {
      toastError(e, '重置失败')
    } finally {
      saving.value = false
    }
  }

  onMounted(load)

  return { model, loading, saving, load, save, reset }
}
