import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { tasksApi } from '@/api'
import type { TaskJob, TaskJobList } from '@/api/tasks'
import { useFeedback } from '@/composables/useFeedback'

/**
 * 任务队列（重新整理 / 确认入库 …）。
 *
 * 轮询挂在布局层（AppLayout 挂载即启动），**不依赖某个页面挂着**：
 * 此前的 stores/task.ts 轮询挂在 TaskStatusBar 上，切页签它被卸载，状态就冻住了（a25715a 修过一处），
 * 阶段 3 起两者都已删除，全站只剩这一条轮询。
 * 有活跃任务时 2 秒一轮，空闲 15 秒一轮。
 *
 * 任务结束时弹一条提示，并通知订阅者（整理记录页据此刷新列表）。
 */
const FINISHED = new Set(['success', 'failed', 'canceled', 'interrupted'])
const ACTIVE_MS = 2000
const IDLE_MS = 15000

export const useQueueStore = defineStore('queue', () => {
  const jobs = ref<TaskJob[]>([])
  const running = ref(0)
  const queued = ref(0)
  const lock = ref<TaskJobList['lock']>({ busy: false })
  const loaded = ref(false)

  const active = computed(() => jobs.value.filter((j) => j.status === 'running' || j.status === 'queued'))
  const finished = computed(() => jobs.value.filter((j) => FINISHED.has(j.status)))

  /** 已知状态：用来识别「刚刚结束」的任务 */
  const lastStatus = new Map<number, string>()
  /** 本页提交过的任务：两次轮询之间就跑完的（从没见过它排队）也要通知 */
  const tracked = new Set<number>()
  const listeners = new Set<(job: TaskJob) => void>()

  let timer: number | undefined
  let refs = 0

  async function poll() {
    try {
      const d = await tasksApi.list()
      const first = !loaded.value
      for (const j of d.data ?? []) {
        const prev = lastStatus.get(j.id)
        const justFinished =
          FINISHED.has(j.status) && ((prev !== undefined && !FINISHED.has(prev)) || (tracked.has(j.id) && prev === undefined))
        if (!first && justFinished) notify(j)
        if (FINISHED.has(j.status)) tracked.delete(j.id)
        lastStatus.set(j.id, j.status)
      }
      jobs.value = d.data ?? []
      running.value = d.running ?? 0
      queued.value = d.queued ?? 0
      lock.value = d.lock ?? { busy: false }
      loaded.value = true
    } catch {
      // 轮询失败静默：后台探测，弹错误只会刷屏
    }
  }

  function notify(j: TaskJob) {
    for (const fn of listeners) fn(j)
    // 后台任务（定时整理 / 转存触发）跑完不弹提示：10 分钟一轮，开着页面就一直弹；
    // 结果在任务队列面板里看。本页提交过的、或被手动操作并进来的（优先级 0）照常提示
    if (j.priority !== 0 && !tracked.has(j.id)) return
    const { message } = useFeedback()
    if (j.status === 'success') message.success(`✓ ${j.title}${j.message ? `：${j.message}` : ''}`)
    else if (j.status === 'canceled') message.warning(`已停止：${j.title}${j.message ? `（${j.message}）` : ''}`)
    else message.error(`✗ ${j.title}：${j.message || '失败'}`)
  }

  function schedule() {
    if (timer !== undefined) clearTimeout(timer)
    if (refs === 0) {
      timer = undefined
      return
    }
    const ms = running.value + queued.value > 0 ? ACTIVE_MS : IDLE_MS
    timer = window.setTimeout(async () => {
      await poll()
      schedule()
    }, ms)
  }

  function start() {
    refs++
    if (refs > 1) return
    void poll().then(schedule)
  }

  function stop() {
    refs = Math.max(0, refs - 1)
    if (refs === 0 && timer !== undefined) {
      clearTimeout(timer)
      timer = undefined
    }
  }

  /** 刚提交了任务：记下 id 并立刻刷新一次，进入快轮询 */
  async function submitted(jobId?: number) {
    if (jobId) tracked.add(jobId)
    await poll()
    schedule()
  }

  /** 订阅任务结束（返回取消订阅函数） */
  function onFinished(fn: (job: TaskJob) => void) {
    listeners.add(fn)
    return () => listeners.delete(fn)
  }

  /** 某类任务是否在队列里（排队中 / 执行中）：同类按钮据此显示「已在队列」 */
  function activeOf(kind: string) {
    return active.value.find((j) => j.kind === kind)
  }

  /**
   * 同类任务里「再点也没用」的那个：正在跑的，或排着的手动任务。
   * 排着的**后台**任务（定时整理 / 定时全量）不算 —— 这时再点一次会把它并成手动任务、
   * 提到手动优先级（排到别的后台任务前面），所以按钮要保持可点
   */
  function activeManualOf(kind: string) {
    return active.value.find((j) => j.kind === kind && (j.status === 'running' || j.priority === 0))
  }

  /** 某条整理记录是否在队列里（排队中 / 执行中），记录页行上据此显示状态 */
  function recordJob(recordId: number) {
    return active.value.find((j) => j.record_ids?.includes(recordId))
  }

  async function cancel(id: number) {
    const { message } = useFeedback()
    try {
      const d = await tasksApi.cancel(id)
      message.success(d.message || '已取消')
    } catch (e) {
      message.error(e instanceof Error ? e.message : '取消失败')
    }
    await poll()
  }

  async function retry(id: number) {
    const { message } = useFeedback()
    try {
      const d = await tasksApi.retry(id)
      message.success(d.message || '已重新加入队列')
    } catch (e) {
      message.error(e instanceof Error ? e.message : '重试失败')
    }
    await poll()
    schedule()
  }

  async function clearFinished() {
    const { message } = useFeedback()
    try {
      const d = await tasksApi.clear()
      message.success(d.message || '已清理')
    } catch (e) {
      message.error(e instanceof Error ? e.message : '清理失败')
    }
    await poll()
  }

  return {
    jobs,
    running,
    queued,
    lock,
    loaded,
    active,
    finished,
    poll,
    start,
    stop,
    submitted,
    onFinished,
    recordJob,
    activeOf,
    activeManualOf,
    cancel,
    retry,
    clearFinished,
  }
})
