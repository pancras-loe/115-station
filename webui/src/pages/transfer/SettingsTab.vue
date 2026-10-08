<script setup lang="ts">
import { computed, type Component } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { BellRing, FolderInput, Globe } from '@lucide/vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import HChip from '@/components/hero/HChip.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import GySource from '@/components/transfer/sources/GySource.vue'
import PansouSource from '@/components/transfer/sources/PansouSource.vue'
import TgSource from '@/components/transfer/sources/TgSource.vue'
import MukakuSource from '@/components/transfer/sources/MukakuSource.vue'
import Re0Source from '@/components/transfer/sources/Re0Source.vue'
import FolderSettings from './FolderSettings.vue'
import SubSettings from '@/pages/subscribe/SubSettings.vue'
import type { SourceKey } from '@/api/transfer'
import { useTransferSources } from '@/composables/transferSources'

/**
 * 影视转存的全部设置：转存目录、五个资源来源、订阅。原来来源设置是五张卡片竖着排、
 * 订阅设置又是五张，要找一项得滚很久；现在左边列小节、右边一次只显示一张（?sec=）。
 * 手机上小节列表变成顶上一排横滑。
 */
const route = useRoute()
const router = useRouter()
const { state, reload, setEnabled } = useTransferSources()

type SubSec = 'check' | 'cond' | 'offline' | 'unlock' | 'notify'

const SOURCES: { key: SourceKey; title: string; hint: string }[] = [
  { key: 'gy', title: '观影', hint: '站内种子 → 115 离线下载' },
  { key: 'pansou', title: '盘搜 PanSou', hint: '聚合全网网盘分享，免登录' },
  { key: 'tg', title: 'TG 频道', hint: '搜 Telegram 公开频道的网页版，不用登录' },
  { key: 'mukaku', title: '不太灵影视', hint: '站内资源需 VIP Token' },
  { key: 're0', title: 'RE0', hint: '官方 OpenAPI，解锁花站内积分' },
]
const SUB_SECS: { key: SubSec; title: string }[] = [
  { key: 'check', title: '检查' },
  { key: 'cond', title: '资源条件' },
  { key: 'offline', title: '离线下载' },
  { key: 'unlock', title: 'RE0 自动解锁' },
  { key: 'notify', title: '通知' },
]

const byKey = computed(() => Object.fromEntries((state.value?.sources ?? []).map((s) => [s.key, s])))

type Tone = 'ok' | 'warn' | 'off'
function srcTone(key: SourceKey): Tone | undefined {
  const s = byKey.value[key]
  if (!s) return undefined
  if (s.enabled === false) return 'off'
  return s.reason ? 'warn' : 'ok'
}

const GROUPS = computed<{ label: string; icon: Component; items: { key: string; title: string; tone?: Tone }[] }[]>(() => [
  { label: '转存', icon: FolderInput, items: [{ key: 'folder', title: '转存目录', tone: (state.value?.folder ? 'ok' : 'warn') as Tone }] },
  { label: '资源来源', icon: Globe, items: SOURCES.map((s) => ({ key: s.key as string, title: s.title, tone: srcTone(s.key) })) },
  { label: '订阅', icon: BellRing, items: SUB_SECS.map((s) => ({ key: s.key as string, title: s.title, tone: undefined as Tone | undefined })) },
])

const sec = computed({
  get: () => (route.query.sec as string) || 'folder',
  set: (v: string) => void router.replace({ query: { ...route.query, sec: v === 'folder' ? undefined : v } }),
})
const source = computed(() => SOURCES.find((s) => s.key === sec.value))
const subSec = computed(() => SUB_SECS.find((s) => s.key === sec.value)?.key)
</script>

<template>
  <div class="settings">
    <nav class="snav" aria-label="设置小节">
      <div v-for="g in GROUPS" :key="g.label" class="snav-group">
        <p class="snav-label"><component :is="g.icon" :size="15" />{{ g.label }}</p>
        <button
          v-for="it in g.items"
          :key="it.key"
          type="button"
          class="snav-item"
          :class="{ on: sec === it.key }"
          :aria-current="sec === it.key ? 'page' : undefined"
          @click="sec = it.key"
        >
          {{ it.title }}
          <span v-if="it.tone" class="tone" :class="it.tone" />
        </button>
      </div>
    </nav>

    <div class="spane">
      <FolderSettings v-if="sec === 'folder'" />

      <SectionCard v-else-if="source" :key="source.key" :title="source.title" :hint="source.hint">
        <template #extra>
          <div class="src-extra">
            <HChip v-if="byKey[source.key]?.reason" color="warning">{{ byKey[source.key]?.reason }}</HChip>
            <HChip v-else-if="byKey[source.key]" color="success">可用</HChip>
            <label class="src-switch">
              <span>参与搜索</span>
              <HSwitch
                :model-value="byKey[source.key]?.enabled !== false"
                :aria-label="`${source.title} 参与搜索`"
                size="sm"
                @update:model-value="(v: boolean) => setEnabled(source!.key, v)"
              />
            </label>
          </div>
        </template>
        <GySource v-if="source.key === 'gy'" @changed="reload" />
        <PansouSource v-else-if="source.key === 'pansou'" @changed="reload" />
        <TgSource v-else-if="source.key === 'tg'" @changed="reload" />
        <MukakuSource v-else-if="source.key === 'mukaku'" @changed="reload" />
        <Re0Source v-else-if="source.key === 're0'" @changed="reload" />
      </SectionCard>

      <!-- 订阅设置整存整取：保持挂载，小节之间切换不丢没保存的改动 -->
      <SubSettings v-show="!!subSec" :section="subSec ?? 'check'" />
    </div>
  </div>
</template>

<style scoped>
.settings {
  display: grid;
  grid-template-columns: 168px minmax(0, 1fr);
  gap: 20px;
  align-items: start;
}
.snav {
  position: sticky;
  top: 12px;
  display: flex;
  flex-direction: column;
  gap: 18px;
}
.snav-group {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.snav-group + .snav-group {
  padding-top: 18px;
  border-top: 1px solid var(--separator);
}
/* 分组标题要一眼和下面的小节区分开：比小节字号大、加粗、带图标，不用灰色 */
.snav-label {
  display: flex;
  align-items: center;
  gap: 7px;
  margin: 0 0 6px;
  padding: 0 10px;
  font-size: 14.5px;
  font-weight: 700;
  color: var(--foreground);
}
.snav-label > svg {
  color: var(--accent);
}
.snav-item {
  all: unset;
  box-sizing: border-box;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  /* 与分组标题的文字对齐（10 + 图标 15 + 间距 7） */
  padding: 7px 10px 7px 32px;
  border-radius: var(--r-sm);
  font-size: 13.5px;
  color: color-mix(in oklab, var(--foreground) 82%, var(--muted));
  cursor: pointer;
  white-space: nowrap;
  transition: background-color 0.15s, color 0.15s;
}
.snav-item:hover {
  background: var(--default);
}
.snav-item:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.snav-item.on {
  background: color-mix(in oklab, var(--accent) 16%, transparent);
  color: var(--accent);
  font-weight: 600;
}
.tone {
  width: 7px;
  height: 7px;
  flex: none;
  border-radius: 50%;
}
.tone.ok {
  background: var(--success);
}
.tone.warn {
  background: var(--warning);
}
.tone.off {
  background: var(--muted);
  opacity: 0.6;
}
.spane {
  min-width: 0;
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

/* 手机：小节列表横排、可横滑，分组标题收起 */
@media (max-width: 720px) {
  .settings {
    grid-template-columns: minmax(0, 1fr);
    gap: 12px;
  }
  .snav {
    position: static;
    flex-direction: row;
    gap: 6px;
    overflow-x: auto;
    scrollbar-width: none;
    padding-bottom: 2px;
  }
  .snav::-webkit-scrollbar {
    display: none;
  }
  .snav-group {
    flex-direction: row;
    gap: 6px;
  }
  .snav-group + .snav-group {
    padding-top: 0;
    padding-left: 6px;
    border-top: 0;
    border-left: 1px solid var(--separator);
  }
  .snav-label {
    display: none;
  }
  .snav-item {
    padding: 5px 12px;
    color: var(--foreground);
    border-radius: 999px;
    background: var(--default);
    font-size: 13px;
  }
}
</style>
