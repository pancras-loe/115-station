<script setup lang="ts">
import { computed, nextTick, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import HSwitch from '@/components/hero/HSwitch.vue'
import HChip from '@/components/hero/HChip.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import GySource from '@/components/transfer/sources/GySource.vue'
import PansouSource from '@/components/transfer/sources/PansouSource.vue'
import TgSource from '@/components/transfer/sources/TgSource.vue'
import MukakuSource from '@/components/transfer/sources/MukakuSource.vue'
import Re0Source from '@/components/transfer/sources/Re0Source.vue'
import type { SourceKey } from '@/api/transfer'
import { useTransferSources } from '@/composables/transferSources'

const route = useRoute()
const { state, reload, setEnabled } = useTransferSources()

const CARDS: { key: SourceKey; title: string; hint: string }[] = [
  { key: 'gy', title: '观影', hint: '站内种子 → 115 离线下载' },
  { key: 'pansou', title: '盘搜 PanSou', hint: '聚合全网网盘分享，免登录' },
  { key: 'tg', title: 'TG 频道', hint: '搜 Telegram 公开频道的网页版，不用登录' },
  { key: 'mukaku', title: '不太灵影视', hint: '站内资源需 VIP Token' },
  { key: 're0', title: 'RE0', hint: '官方 OpenAPI，解锁花站内积分' },
]

const byKey = computed(() => Object.fromEntries((state.value?.sources ?? []).map((s) => [s.key, s])))

// 旧地址 ?tab=gy 这类进来时，滚到对应的卡片
onMounted(async () => {
  const focus = route.query.focus as string | undefined
  if (!focus) return
  await nextTick()
  document.getElementById(`src-${focus}`)?.scrollIntoView({ block: 'start', behavior: 'smooth' })
})
</script>

<template>
  <div class="stack">
    <SectionCard v-for="c in CARDS" :id="`src-${c.key}`" :key="c.key" :title="c.title" :hint="c.hint">
      <template #extra>
        <div class="src-extra">
          <HChip v-if="byKey[c.key]?.reason" color="warning">{{ byKey[c.key]?.reason }}</HChip>
          <HChip v-else-if="byKey[c.key]" color="success">可用</HChip>
          <label class="src-switch">
            <span>{{ byKey[c.key]?.enabled === false ? '搜索时跳过' : '参与搜索' }}</span>
            <HSwitch
              :model-value="byKey[c.key]?.enabled !== false"
              :aria-label="`${c.title} 参与搜索`"
              size="sm"
              @update:model-value="(v: boolean) => setEnabled(c.key, v)"
            />
          </label>
        </div>
      </template>
      <GySource v-if="c.key === 'gy'" @changed="reload" />
      <PansouSource v-else-if="c.key === 'pansou'" @changed="reload" />
      <TgSource v-else-if="c.key === 'tg'" @changed="reload" />
      <MukakuSource v-else-if="c.key === 'mukaku'" @changed="reload" />
      <Re0Source v-else-if="c.key === 're0'" @changed="reload" />
    </SectionCard>
  </div>
</template>

<style scoped>
.stack {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.src-extra {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
}
.src-switch {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 12.5px;
  color: var(--muted);
  cursor: pointer;
}
</style>
