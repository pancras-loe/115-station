import { defineStore } from 'pinia'
import { ref } from 'vue'
import { syncApi } from '@/api'
import type { TaskStatus } from '@/api/sync'

/**
 * 后台任务状态。
 *
 * 同步与整理在后端是互斥的（共用 fullSyncMu），所以「有任务在跑」是跨页面的
 * 全局状态：同步页、整理页的执行按钮都要据此禁用，否则用户点了只会拿到
 * 409「任务正在进行中」。放在 store 里，多页共享同一条轮询。
 */
export const useTaskStore = defineStore('task', () => {
  const status = ref<TaskStatus>({ running: false })
  let timer: number | undefined
  /** 引用计数：多个页面同时挂载时只保留一条轮询 */
  let refs = 0

  async function poll() {
    try {
      status.value = await syncApi.status()
    } catch {
      // 轮询失败静默：这是后台探测，弹错误只会刷屏
    }
  }

  function start() {
    refs++
    if (timer !== undefined) return
    poll()
    timer = window.setInterval(poll, 5000)
  }

  function stop() {
    refs = Math.max(0, refs - 1)
    if (refs === 0 && timer !== undefined) {
      clearInterval(timer)
      timer = undefined
    }
  }

  return { status, poll, start, stop }
})
