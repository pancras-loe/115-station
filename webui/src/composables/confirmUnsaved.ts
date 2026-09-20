import { h } from 'vue'
import { NButton, NSpace } from 'naive-ui'
import { useFeedback } from './useFeedback'

/**
 * 通用提示语：说清两个出口各自意味着什么，不提具体配置项。
 * 某个入口有更要命的后果（文件搬去哪、存进哪个目录）时，调用方自己传一句。
 */
export const UNSAVED_NOTE = '直接开始将按修改前的配置运行；要用改后的配置，请选「保存并开始」。'

/**
 * 「有未保存的配置，还开始吗」确认框。
 *
 * 几个执行入口（全量/增量同步、整理、转存）跑的配置有一部分只从库里读，
 * 表单改了没保存就点开始，任务会静默按旧配置跑完——文件搬完了才发现。
 * 所以 dirty 时统一拦一道，三个出口：保存并开始 / 直接开始 / 取消。
 *
 * 用 dialog 而不是按钮上原有的 NPopconfirm：气泡只有确定/取消两个位置，
 * 塞不下第三个出口。调用方在 dirty 时应当跳过 popconfirm，避免连弹两次。
 *
 * @param note 一句话说清「直接开始」的后果，别写成一段——没人读
 * @param save 保存动作，返回 false 视为保存失败（不继续执行）
 * @returns true = 继续执行任务，false = 用户取消或保存失败
 */
export async function confirmUnsaved(
  note: string,
  save: () => Promise<boolean | void>,
): Promise<boolean> {
  const { dialog } = useFeedback()

  const choice = await new Promise<'save' | 'skip' | 'cancel'>((resolve) => {
    // 三个按钮都走 destroy()，统一在 onAfterLeave 里回收，避免点遮罩/Esc 关掉后 Promise 悬着
    let picked: 'save' | 'skip' | 'cancel' = 'cancel'
    const d = dialog.warning({
      title: '有未保存的配置',
      content: note,
      onAfterLeave: () => resolve(picked),
      action: () =>
        h(NSpace, { size: 8 }, () => [
          h(NButton, { size: 'small', onClick: () => ((picked = 'cancel'), d.destroy()) }, () => '取消'),
          h(NButton, { size: 'small', onClick: () => ((picked = 'skip'), d.destroy()) }, () => '直接开始'),
          h(
            NButton,
            { size: 'small', type: 'primary', onClick: () => ((picked = 'save'), d.destroy()) },
            () => '保存并开始',
          ),
        ]),
    })
  })

  if (choice === 'cancel') return false
  if (choice === 'save') return (await save()) !== false
  return true
}
