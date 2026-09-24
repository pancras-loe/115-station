<script setup lang="ts">
import { computed } from 'vue'
import { ChevronLeft, ChevronRight } from '@lucide/vue'
import HSelect from './HSelect.vue'

/**
 * 分页：HeroUI 的 pagination 外观，页码折叠逻辑自己算（它本来就是纯展示组件）。
 * 窄屏只留「上一页 / 当前页 / 下一页」，页码串在手机上一排放不下。
 */
const props = withDefaults(
  defineProps<{ total: number; pageSizes?: number[] }>(),
  { pageSizes: () => [20, 50, 100] },
)
const page = defineModel<number>('page', { required: true })
const size = defineModel<number>('pageSize', { required: true })

const pages = computed(() => Math.max(1, Math.ceil(props.total / size.value)))

/** 1 … 4 5 [6] 7 8 … 20：首尾常驻，当前页左右各两页 */
const items = computed<(number | 'gap')[]>(() => {
  const n = pages.value
  const cur = page.value
  if (n <= 7) return Array.from({ length: n }, (_, i) => i + 1)
  const out: (number | 'gap')[] = [1]
  const lo = Math.max(2, cur - 2)
  const hi = Math.min(n - 1, cur + 2)
  if (lo > 2) out.push('gap')
  for (let i = lo; i <= hi; i++) out.push(i)
  if (hi < n - 1) out.push('gap')
  out.push(n)
  return out
})

function go(p: number) {
  const next = Math.min(pages.value, Math.max(1, p))
  if (next !== page.value) page.value = next
}

const sizeOptions = computed(() => props.pageSizes.map((n) => ({ label: `${n} 条/页`, value: n })))
</script>

<template>
  <nav class="pagination pagination--sm h-pager" aria-label="分页">
    <div class="pagination__summary">
      <span>共 {{ total }} 条</span>
      <div class="h-pager-size"><HSelect v-model="size" :options="sizeOptions" aria-label="每页条数" /></div>
    </div>
    <ul class="pagination__content h-pager-list">
      <li class="pagination__item">
        <button
          type="button"
          class="pagination__link pagination__link--nav"
          :disabled="page <= 1"
          aria-label="上一页"
          @click="go(page - 1)"
        >
          <ChevronLeft :size="16" data-slot="pagination-previous-icon" />
        </button>
      </li>
      <li v-for="(it, i) in items" :key="i" class="pagination__item h-pager-num">
        <span v-if="it === 'gap'" class="pagination__ellipsis">…</span>
        <button
          v-else
          type="button"
          class="pagination__link"
          :data-active="it === page || undefined"
          :aria-current="it === page ? 'page' : undefined"
          @click="go(it)"
        >
          {{ it }}
        </button>
      </li>
      <li class="pagination__item h-pager-compact">
        <span class="pagination__ellipsis h-pager-compact-text">{{ page }} / {{ pages }}</span>
      </li>
      <li class="pagination__item">
        <button
          type="button"
          class="pagination__link pagination__link--nav"
          :disabled="page >= pages"
          aria-label="下一页"
          @click="go(page + 1)"
        >
          <ChevronRight :size="16" data-slot="pagination-next-icon" />
        </button>
      </li>
    </ul>
  </nav>
</template>

<style scoped>
.h-pager-size {
  width: 112px;
}
.h-pager-compact {
  display: none;
}
.h-pager-compact-text {
  width: auto;
  padding: 0 6px;
  font-variant-numeric: tabular-nums;
}

@media (max-width: 720px) {
  /* 手机上摘要与翻页并排一行：左边条数/每页，右边上一页·页码·下一页 */
  .h-pager {
    flex-direction: row;
  }
  .h-pager-num {
    display: none;
  }
  .h-pager-compact {
    display: inline-flex;
  }
  .h-pager-list {
    margin-left: auto;
    align-self: center;
  }
}
</style>
