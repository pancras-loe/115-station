import { computed, onMounted, ref, shallowRef } from 'vue'
import { configApi, libraryApi, syncApi } from '@/api'
import type { FullLastScan, FullSyncConfig, FullSyncMode } from '@/api/sync'
import type { MediaLib } from '@/api/library'
import type { FullSetting } from './fullSetting'
import { useQueueStore } from '@/stores/queue'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { UNSAVED_NOTE, confirmUnsaved } from '@/composables/confirmUnsaved'

/**
 * 「开始全量同步」「开始增量同步」两个动作。
 * 页面顶部的状态总览要放这两个按钮（最常用的操作一眼看得到，不用翻到表单底部），
 * 全量页签又要知道快速模式可不可用 —— 所以抽出来由 SyncPage 持有一份，两处共用。
 */
/**
 * 「开始全量」确认框要摆出来的东西（components/strm/FullRunConfirm.vue）：扫哪几个库、三组后缀、
 * 失效检测与刷新 Emby 开没开，以及比上一次全量少了哪些后缀。
 * 2026-10-23 起扫描范围的设置排在页签下半截，新用户可能没看到就点了开始，所以开跑那一刻把范围写全
 */
export interface FullRunPlan {
  cfg: FullSyncConfig
  mode: FullSyncMode
  libs: MediaLib[] | null
  legacyScan: FullLastScan | null
  resolve: (ids: number[] | null) => void
}

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

  /** 正在等用户确认的全量；SyncPage 挂着的 FullRunConfirm 据此弹出 */
  const fullPlan = shallowRef<FullRunPlan | null>(null)

  const runningFull = ref(false)
  const runningIncr = ref(false)
  // 不同范围的全量可以分别排队，页面只防止同时开两个确认流程。
  const busyFull = ref(false)
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
    return src
  }

  async function runFull() {
    if (busyFull.value) return
    busyFull.value = true
    try {
      const src = await effectiveConfig()
      if (!src) return
      if (!src.video_ext.length) {
        message.warning('请至少保留一个视频文件后缀')
        return
      }
      const mode: FullSyncMode = fastAvailable.value ? src.mode : 'normal'
      const [libs, legacyScan] = await Promise.all([
        libraryApi.getMediaLibs().then((d) => d.libs).catch(() => null),
        configApi
          .getSetting<FullLastScan>('full_last_scan', { at: 0, video_ext: [], image_ext: [], data_ext: [] })
          .then((v) => (v.at ? v : null))
          .catch(() => null),
      ])
      const libraryIds = await new Promise<number[] | null>((resolve) => {
        fullPlan.value = {
          cfg: src,
          mode,
          libs,
          legacyScan,
          resolve: (ids) => {
            fullPlan.value = null
            resolve(ids)
          },
        }
      })
      if (!libraryIds?.length) return
      runningFull.value = true
      const d = await syncApi.runFull({
        library_ids: libraryIds,
        local_path: src.local_path,
        video_ext: src.video_ext,
        image_ext: src.image_ext,
        data_ext: src.data_ext,
        mode,
      })
      message.success(d.message)
      await queue.submitted(d.job_id)
    } catch (e) {
      toastError(e, '提交全量同步失败')
    } finally {
      runningFull.value = false
      busyFull.value = false
    }
  }

  async function runIncr() {
    const src = await effectiveConfig()
    if (!src) return
    const cid = src.cid.trim()
    if (!cid || cid === '0') {
      message.error('请先到「账号与媒体库 → 媒体库」配置并保存 115 媒体库目录')
      return
    }
    runningIncr.value = true
    try {
      const d = await syncApi.runIncremental({
        cid,
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

  return { fastAvailable, fastReason, fullMode, runningFull, runningIncr, busyFull, busyIncr, fullPlan, runFull, runIncr }
}

export type StrmRuns = ReturnType<typeof useStrmRuns>
