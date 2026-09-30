<script setup lang="ts">
import { computed } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import { ExternalLink } from '@lucide/vue'
import type { ResourceItem } from '@/api/transfer'
import { SOURCE_LABEL, kindGroup, kindLabel, pixLabel } from '@/utils/transferSort'

const props = defineProps<{
  item: ResourceItem
  /** 行内状态：一次搜索可能提交十几条，toast 会互相覆盖，而且要看得出哪几条已经提交过 */
  state?: { tone: 'busy' | 'ok' | 'err'; text: string }
}>()
const emit = defineEmits<{ act: [] }>()

const KIND_TONE = { share115: 'success', download: 'accent', pan: 'default' } as const

const tags = computed(() => {
  const t = props.item.tags
  const out: string[] = []
  const pix = pixLabel(t.pix)
  if (pix) out.push(pix)
  if (t.effect) out.push(t.effect)
  if (t.type && /remux/i.test(t.type)) out.push('原盘')
  else if (t.type) out.push(t.type)
  if (t.video) out.push(t.video)
  if (t.season) out.push(t.season)
  if (t.zh) out.push('中字')
  return out
})

const meta = computed(() => {
  const it = props.item
  const out: string[] = [(SOURCE_LABEL[it.source] ?? it.source) + (it.via ? ` ${it.via}` : '')]
  if (it.size) out.push(it.size)
  if (it.seeds) out.push(`做种 ${it.seeds}`)
  if (it.time) out.push(it.time)
  if (it.code) out.push(`提取码 ${it.code}`)
  return out
})

const submitted = computed(() => {
  const at = props.item.submitted_at
  if (!at) return ''
  const d = new Date(at * 1000)
  return `${d.getMonth() + 1}-${String(d.getDate()).padStart(2, '0')} 提交过`
})

const actionLabel = computed(() => {
  const it = props.item
  switch (it.action) {
    case 'transfer':
      return '转存'
    case 'offline':
      return '离线下载'
    case 'open':
      return '打开链接'
    case 'unlock':
      if (it.owned) return '获取并转存'
      return it.points != null ? `解锁 · ${it.points} 积分` : '解锁'
  }
  return ''
})
</script>

<template>
  <div class="res" :class="{ dim: !item.relevant }">
    <div class="res-main">
      <div class="res-head">
        <HChip :color="KIND_TONE[kindGroup(item)]">{{ kindLabel(item) }}</HChip>
        <span class="res-title" :title="item.title">{{ item.title }}</span>
      </div>
      <div v-if="tags.length || item.rank === 0 || submitted" class="res-tags">
        <span v-if="item.rank === 0" class="tag tag-pref" title="命中洗版策略里优先级最高的那条规则">洗版首选</span>
        <span v-for="t in tags" :key="t" class="tag">{{ t }}</span>
        <span v-if="submitted" class="tag tag-warn">{{ submitted }}</span>
      </div>
      <div class="res-meta">
        <span v-for="(m, i) in meta" :key="i">{{ m }}</span>
      </div>
      <div v-if="state?.text" class="res-state" :class="state.tone">{{ state.text }}</div>
    </div>
    <HButton
      class="res-act"
      size="sm"
      :variant="item.action === 'open' ? 'tertiary' : state?.tone === 'ok' ? 'secondary' : 'primary'"
      :loading="state?.tone === 'busy'"
      @click="emit('act')"
    >
      <template v-if="item.action === 'open'" #icon><ExternalLink :size="13" /></template>
      {{ state?.tone === 'ok' && item.action !== 'open' ? '再次提交' : actionLabel }}
    </HButton>
  </div>
</template>

<style scoped>
.res {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 11px 4px;
  border-bottom: 1px solid var(--border);
}
.res:last-child {
  border-bottom: 0;
}
.res.dim .res-title,
.res.dim .res-tags {
  opacity: 0.6;
}
.res-main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 5px;
}
.res-head {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  min-width: 0;
}
.res-title {
  min-width: 0;
  font-size: 13.5px;
  line-height: 1.5;
  color: var(--foreground);
  word-break: break-all;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.res-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}
.tag {
  padding: 0 7px;
  border-radius: var(--r-sm);
  font-size: 11.5px;
  line-height: 19px;
  background: var(--default);
  color: color-mix(in oklab, var(--foreground) 75%, var(--muted));
}
.tag-pref {
  background: var(--accent-soft);
  color: var(--accent);
}
.tag-warn {
  background: var(--warning-soft);
  color: var(--warning);
}
.res-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 2px 12px;
  font-size: 12px;
  color: var(--muted);
  font-variant-numeric: tabular-nums;
}
.res-state {
  font-size: 12px;
  color: var(--muted);
}
.res-state.ok {
  color: var(--success);
}
.res-state.err {
  color: var(--danger);
}
.res-act {
  flex: none;
}
@media (max-width: 720px) {
  .res {
    flex-direction: column;
    align-items: stretch;
  }
  .res-act {
    align-self: flex-end;
  }
}
</style>
