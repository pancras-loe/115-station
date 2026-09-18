import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'

/**
 * 把当前页签同步到地址栏 `?tab=`。
 *
 * 旧前端的页签只存在内存里，刷新就回到第一个 —— 而这些配置页动辄 5 个页签，
 * 「保存后刷新验证」是高频操作。用 replace 而不是 push，免得返回键要按很多次
 * 才能退出当前页。
 */
export function useTabQuery(defaultTab: string) {
  const route = useRoute()
  const router = useRouter()

  return computed({
    get: () => (route.query.tab as string) || defaultTab,
    set: (v: string) => {
      router.replace({ query: v === defaultTab ? {} : { tab: v } })
    },
  })
}
