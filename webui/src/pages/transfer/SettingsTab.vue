<script setup lang="ts">
import { computed, onMounted, ref, type Component } from 'vue'
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
import DianSource from '@/components/transfer/sources/DianSource.vue'
import FolderSettings from './FolderSettings.vue'
import SubSettings, { type SubSettingsSec as SubSec } from '@/pages/subscribe/SubSettings.vue'
import { transferApi } from '@/api'
import type { SourceKey, XferTargets } from '@/api/transfer'
import { useTransferSources } from '@/composables/transferSources'

/**
 * 发现与订阅的设置，分两个页签共用这一个布局（part）：「资源来源」= 默认转存库 + 各资源来源，
 * 「订阅设置」= 订阅的几个小节。原来两组合在一个「设置」页签里；来源设置是五张卡片竖着排、
 * 订阅设置又是五张，要找一项得滚很久，现在左边列小节、右边一次只显示一张（?sec=）。
 * 手机上小节列表变成顶上一排横滑。订阅的自动解锁积分在 RE0 / 癫影的来源设置里（AutoUnlockRows）。
 */
const props = defineProps<{ part: 'sources' | 'subs' }>()
const route = useRoute()
const router = useRouter()
const { state, reload, setEnabled } = useTransferSources()

// 「默认转存库」只在有两个以上能转存的库时才有得选；一个库或还没有库时整个小节不显示，
// 转存文件夹本身在「账号与媒体库 → 媒体库」里设
const targets = ref<XferTargets | null>(null)
onMounted(async () => {
  try {
    targets.value = await transferApi.xferTargets()
  } catch {
    targets.value = null
  }
})
const showFolder = computed(() => (targets.value?.libs.length ?? 0) > 1)


const SOURCES: { key: SourceKey; title: string; hint: string }[] = [
  { key: 'gy', title: '观影', hint: '按影片读站内磁力与网盘资源' },
  { key: 'pansou', title: '盘搜 PanSou', hint: '聚合全网网盘分享，免登录' },
  { key: 'tg', title: 'TG 频道', hint: '搜 Telegram 公开频道的网页版，不用登录' },
  { key: 'mukaku', title: '不太灵影视', hint: '站内资源需 VIP Token' },
  { key: 're0', title: 'RE0', hint: '账号密码网页登录，解锁花站内积分' },
  { key: 'dian', title: '癫影', hint: 'm.dian115.com，账号密码网页登录，解锁花站内积分' },
]
const SUB_SECS: { key: SubSec; title: string }[] = [
  { key: 'check', title: '检查计划' },
  { key: 'cond', title: '资源筛选' },
  { key: 'wash', title: '洗版' },
  { key: 'offline', title: '转存与离线' },
  { key: 'notify', title: '通知' },
  { key: 'limits', title: '节流与限额' },
]

const byKey = computed(() => Object.fromEntries((state.value?.sources ?? []).map((s) => [s.key, s])))

type Tone = 'ok' | 'warn' | 'off'
function srcTone(key: SourceKey): Tone | undefined {
  const s = byKey.value[key]
  if (!s) return undefined
  if (s.enabled === false) return 'off'
  return s.reason ? 'warn' : 'ok'
}

type Group = { label: string; icon: Component; items: { key: string; title: string; tone?: Tone }[] }
const GROUPS = computed<Group[]>(() =>
  props.part === 'subs'
    ? [{ label: '订阅', icon: BellRing, items: SUB_SECS.map((s) => ({ key: s.key as string, title: s.title })) }]
    : [
        ...(showFolder.value
          ? [{ label: '转存', icon: FolderInput, items: [{ key: 'folder', title: '默认转存库', tone: (targets.value?.default ? 'ok' : 'warn') as Tone }] }]
          : []),
        { label: '资源来源', icon: Globe, items: SOURCES.map((s) => ({ key: s.key as string, title: s.title, tone: srcTone(s.key) })) },
      ],
)
const allKeys = computed(() => GROUPS.value.flatMap((g) => g.items.map((it) => it.key)))

// 没写 ?sec= 时打开第一个小节；收藏的 ?sec=folder 在没有默认转存库可选时落到第一个来源，
// 不属于本页签的小节（别的页签留下的 ?sec=）同样落到第一个
const firstSec = computed(() => (props.part === 'subs' ? SUB_SECS[0].key : showFolder.value ? 'folder' : SOURCES[0].key))
const sec = computed({
  get: () => {
    const q = route.query.sec as string | undefined
    if (!q || !allKeys.value.includes(q)) return firstSec.value
    return q
  },
  set: (v: string) => void router.replace({ query: { ...route.query, sec: v === firstSec.value ? undefined : v } }),
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
      <FolderSettings v-if="sec === 'folder' && targets" :targets="targets" @saved="(t) => (targets = t)" />

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
        <DianSource v-else-if="source.key === 'dian'" @changed="reload" />
      </SectionCard>

      <!-- 订阅设置整存整取：保持挂载，小节之间切换不丢没保存的改动 -->
      <SubSettings v-if="part === 'subs'" :section="subSec ?? 'check'" @go="(k) => (sec = k)" />
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
