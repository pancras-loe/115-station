import { computed, onMounted, ref } from 'vue'
import { configApi } from '@/api'
import { toastError, useFeedback } from './useFeedback'

/**
 * 绑定一个 Setting 表的配置项。
 *
 * 把「读 → 填表单 → 存 → 重置为默认值」这套在旧前端里每个配置卡都抄一遍的
 * 流程收成一处。默认值对象同时承担三个角色：类型来源、缺字段时的兜底、
 * 以及「重置配置」按下时写回的内容。
 */
export function useSetting<T extends object>(
  key: string,
  defaults: T,
  /**
   * 读回来的值先过一道整形。两个用途：
   * 一是字段形状变过时别让旧值把页面打崩（该回默认值的回默认值），
   * 二是给数组/对象字段做一次深拷贝——`getSetting` 的兜底是浅展开，
   * 库里缺这个字段时 model 会和 defaults 共用同一个数组引用，
   * 用户在界面上增删一条就把「默认值」本身改了（见 defaultFull 的同款脚注）。
   */
  opts?: { normalize?: (v: T) => T },
) {
  const norm = (v: T): T => (opts?.normalize ? opts.normalize(v) : v)
  const model = ref<T>(norm({ ...defaults }))
  const loading = ref(true)
  const saving = ref(false)

  /**
   * 最近一次「与库里一致」的快照，用来判断表单有没有被改过。
   * 序列化比对而不是逐字段 diff：配置对象都是纯数据（含后缀数组、enrich 子对象），
   * 字段顺序由 load 时那份响应固定下来，之后用户只改值不改顺序。
   */
  const snapshot = ref(JSON.stringify(model.value))
  const dirty = computed(() => JSON.stringify(model.value) !== snapshot.value)

  /**
   * 库里那份配置。表单改脏之后仍要按已保存的值执行任务时用它
   * （执行入口的「直接开始」），这样界面上的改动不必先丢掉。
   */
  const saved = computed(() => JSON.parse(snapshot.value) as T)

  async function load() {
    loading.value = true
    try {
      model.value = norm(await configApi.getSetting<T>(key, { ...defaults }))
      snapshot.value = JSON.stringify(model.value)
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
      snapshot.value = JSON.stringify(model.value)
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
      const fresh = norm({ ...defaults })
      await configApi.saveSetting(key, fresh)
      model.value = fresh
      snapshot.value = JSON.stringify(model.value)
      useFeedback().message.success('配置已恢复默认值')
    } catch (e) {
      toastError(e, '重置失败')
    } finally {
      saving.value = false
    }
  }

  onMounted(load)

  return { model, loading, saving, dirty, saved, load, save, reset }
}
