<script setup lang="ts">
import { onMounted, ref } from 'vue'
import HChip from '@/components/hero/HChip.vue'
import { heroTone } from '@/components/hero/tone'
import EmptyState from '@/components/ui/EmptyState.vue'
import { pluginsApi } from '@/api'
import type { PtDay, PtRecords } from '@/api/plugins'
import { toastError } from '@/composables/useFeedback'
import { RESULT_LABEL, TRIGGER_LABEL, failLabel } from './pt'

/** 记录：14 天矩阵、最近一轮明细、推出去的汇总长什么样。只读本地记账，不请求站点 */
const data = ref<PtRecords | null>(null)

onMounted(async () => {
  try {
    data.value = await pluginsApi.ptRecords()
  } catch (e) {
    toastError(e, '读记录失败')
  }
})

function dayHead(d: string, i: number, n: number): string {
  if (i === n - 1) return '今天'
  const [, m, dd] = d.split('-')
  return `${Number(m)}/${Number(dd)}`
}
function dotTip(d: string, x: PtDay | null): string {
  if (!x) return `${d} 没跑`
  return `${d} ${x.at} ${RESULT_LABEL[x.result]}：${x.message}`
}
function tone(r: string) {
  return heroTone(r === 'ok' ? 'success' : r === 'login' ? 'accent' : 'danger')
}
</script>

<template>
  <div v-if="data" class="rec">
    <section class="block">
      <h3>近 14 天</h3>
      <p class="sub">每格一天，鼠标停留看原因。只确认登录的天不算签到成功。</p>
      <EmptyState v-if="!data.rows.length" text="还没有站点。" />
      <div v-else class="scroll">
        <table>
          <thead>
            <tr>
              <th>站点</th>
              <th v-for="(d, i) in data.days" :key="d" class="c">{{ dayHead(d, i, data.days.length) }}</th>
              <th class="c">成功</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in data.rows" :key="r.site_id">
              <td class="name">{{ r.name }}</td>
              <td v-for="(x, i) in r.days" :key="i" class="c">
                <span class="dot" :class="x ? `d-${x.result}` : ''" :title="dotTip(data.days[i], x)" />
              </td>
              <td class="c num">{{ r.ok }}/{{ data.days.length }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <div class="two">
      <section class="block">
        <h3>最近一轮</h3>
        <p v-if="data.last.start" class="sub">
          {{ data.last.start }} · {{ TRIGGER_LABEL[data.last.trigger || ''] || data.last.trigger }} · {{ data.items.length }} 个站 · 用时
          {{ Math.max(1, Math.round((data.last.seconds || 0) / 60)) }} 分钟
        </p>
        <EmptyState v-if="!data.items.length" text="还没跑过。" />
        <div v-else class="items">
          <div v-for="(it, i) in data.items" :key="i" class="item">
            <span class="iname">{{ it.name }}</span>
            <HChip size="sm" :color="tone(it.result)">{{ it.result === 'fail' ? failLabel(it.reason) : RESULT_LABEL[it.result] }}</HChip>
            <span class="why">{{ it.message }}</span>
          </div>
        </div>
      </section>
      <section class="block">
        <h3>推送的通知</h3>
        <p class="sub">每轮一条，失败排最前，同样原因合并成一行。</p>
        <pre v-if="data.notify_preview" class="notify">{{ data.notify_preview }}</pre>
        <EmptyState v-else text="跑过一轮之后显示。" />
      </section>
    </div>
  </div>
</template>

<style scoped>
.rec {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.block {
  min-width: 0;
}
h3 {
  margin: 0;
  font-size: 14px;
  color: var(--foreground);
}
.sub {
  margin: 2px 0 8px;
  font-size: 12px;
  color: var(--muted);
}
.scroll {
  overflow-x: auto;
}
table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12.5px;
}
th,
td {
  padding: 5px 6px;
  border-bottom: 1px solid var(--border);
  text-align: left;
  white-space: nowrap;
}
th {
  font-weight: 500;
  font-size: 11.5px;
  color: var(--muted);
}
.c {
  text-align: center;
}
.num {
  font-variant-numeric: tabular-nums;
}
.name {
  color: var(--foreground);
}
.dot {
  display: inline-block;
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: var(--border);
}
.d-ok {
  background: var(--success);
}
.d-login {
  background: var(--accent);
}
.d-fail {
  background: var(--danger);
}
.two {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 16px;
}
@media (max-width: 720px) {
  .two {
    grid-template-columns: minmax(0, 1fr);
  }
}
.items {
  display: flex;
  flex-direction: column;
}
.item {
  display: grid;
  grid-template-columns: 84px auto minmax(0, 1fr);
  align-items: baseline;
  gap: 8px;
  padding: 6px 0;
  border-bottom: 1px solid var(--border);
  font-size: 12.5px;
}
.item:last-child {
  border-bottom: 0;
}
.iname {
  overflow: hidden;
  color: var(--foreground);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.why {
  color: var(--muted);
  overflow-wrap: anywhere;
}
.notify {
  margin: 0;
  padding: 10px 12px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  color: var(--foreground);
  font-family: inherit;
  font-size: 12.5px;
  line-height: 1.7;
  white-space: pre-wrap;
}
</style>
