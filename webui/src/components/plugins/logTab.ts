import { ref, watch, type Ref } from 'vue'
import type { TabItem } from '@/components/hero/HTabs.vue'

/** 没有页签的插件弹窗加一组「设置 / 日志」；每次打开回到设置 */
export function usePluginLogTab(show: Ref<boolean>) {
  const tab = ref<'set' | 'log'>('set')
  const logTabs: TabItem<'set' | 'log'>[] = [
    { value: 'set', label: '设置' },
    { value: 'log', label: '日志' },
  ]
  watch(show, (v) => {
    if (v) tab.value = 'set'
  })
  return { tab, logTabs }
}
