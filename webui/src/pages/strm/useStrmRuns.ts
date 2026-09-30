import { computed, onMounted, ref } from 'vue'
import { syncApi } from '@/api'
import type { FullSyncConfig, FullSyncMode } from '@/api/sync'
import type { FullSetting } from './fullSetting'
import { useQueueStore } from '@/stores/queue'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { UNSAVED_NOTE, confirmUnsaved } from '@/composables/confirmUnsaved'

/**
 * 「开始全量同步」「开始增量同步」两个动作。
 * 页面顶部的状态总览要放这两个按钮（最常用的操作一眼看得到，不用翻到表单底部），
 * 全量页签又要知道快速模式可不可用 —— 所以抽出来由 SyncPage 持有一份，两处共用。
 */
export function useStrmRuns(full: FullSetting) {
  const { message } = useFeedback()
  const queue = useQueueStore()

  /**
   * 快速模式只在 Cookie 通道可用（downfolders 没有开放平台对应端点），
   * 判定规则放在后端，这里只负责展示——前端自己按 openapi_enabled 推会和后端走偏。
   */
  const fastAvailable = ref(false)
  const fastReason = ref('')
  onMounted(async () => {
    try {
      const d = await syncApi.capabilities()
      fastAvailable.value = d.fast_available
      fastReason.value = d.reason
    } catch {
      // 拿不到就当不可用，退回标准模式，不打断页面
    }
  })

  /** 不可用时强制回落，避免配置里残留 fast 却静默走标准模式 */
  const fullMode = computed<FullSyncMode>(() => (fastAvailable.value ? full.model.value.mode : 'normal'))

  const runningFull = ref(false)
  const runningIncr = ref(false)
  /** 队列里已有同类手动任务（排队或执行中）：再点也只是合并成同一个任务 */
  const busyFull = computed(() => runningFull.value || !!queue.activeManualOf('full'))
  const busyIncr = computed(() => runningIncr.value || !!queue.activeManualOf('incr'))

  /**
   * 配置改过没保存时先问：保存后再跑，还是直接用库里那份跑。
   * 选「直接开始」时这次跑的是库里那份配置，校验和请求体都得照着那份来，
   * 否则界面上填错的值会去拦一次跑得通的同步。返回 null = 用户取消。
   */
  async function effectiveConfig(): Promise<FullSyncConfig | null> {
    let src = full.model.value
    if (full.dirty.value) {
      if (!(await confirmUnsaved(UNSAVED_NOTE, () => full.save()))) return null
      // 保存成功后 dirty 归零，界面值即已保存值；仍然脏 = 用户选了「直接开始」
      if (full.dirty.value) src = full.saved.value
    }
    const cid = src.cid.trim()
    if (!cid || cid === '0') {
      message.error('请先到「账号与媒体库」配置并保存 115 媒体库目录')
      return null
    }
    return src
  }

  async function runFull() {
    const src = await effectiveConfig()
    if (!src) return
    if (!src.video_ext.length) {
      message.warning('请至少保留一个视频文件后缀')
      return
    }
    runningFull.value = true
    try {
      const d = await syncApi.runFull({
        cid: src.cid.trim(),
        local_path: src.local_path,
        video_ext: src.video_ext,
        image_ext: src.image_ext,
        data_ext: src.data_ext,
        mode: fastAvailable.value ? src.mode : 'normal',
      })
      message.success(d.message)
      await queue.submitted(d.job_id)
    } catch (e) {
      toastError(e, '提交全量同步失败')
    } finally {
      runningFull.value = false
    }
  }

  async function runIncr() {
    const src = await effectiveConfig()
    if (!src) return
    runningIncr.value = true
    try {
      const d = await syncApi.runIncremental({
        cid: src.cid.trim(),
        local_path: src.local_path,
        video_ext: src.video_ext,
        image_ext: src.image_ext,
        data_ext: src.data_ext,
      })
      message.success(d.message)
      await queue.submitted(d.job_id)
    } catch (e) {
      toastError(e, '提交增量同步失败')
    } finally {
      runningIncr.value = false
    }
  }

  return { fastAvailable, fastReason, fullMode, runningFull, runningIncr, busyFull, busyIncr, runFull, runIncr }
}

export type StrmRuns = ReturnType<typeof useStrmRuns>
