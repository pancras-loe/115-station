<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { CircleCheck, RefreshCw, TriangleAlert } from '@lucide/vue'
import HButton from '@/components/hero/HButton.vue'
import HSpinner from '@/components/hero/HSpinner.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import { organizeApi } from '@/api'
import type { EmbyCheck } from '@/api/organize'
import { useScrapeProvider } from '@/composables/scrapeProvider'

/**
 * 影视刮削页顶部：Emby 媒体库设置检查（只读，不替用户改 Emby）。
 * 2026-10-15 起刮不刮按库设、改在「账号与媒体库 → 媒体库」的库卡片上（LIBRARY-ROLES-PLAN.md 阶段 2），
 * 这一页只管「怎么刮」。2026-10-24 缩成一行：全都合适时只报个数，有问题才列出有问题的库——
 * 设置对的时候整张卡片只是占地方，但 Emby 悄悄盖掉本站 NFO 这类问题只有这里说得出来，所以没删。
 * **别在这里放能改的开关**：两处各存一份会互相覆盖（incr.cron 那个坑）。
 */
const { embyScrapes, load } = useScrapeProvider()
const LIB_LINK = { name: 'accounts', query: { tab: 'library' } }

const check = ref<EmbyCheck | null>(null)
const checking = ref(false)
async function runCheck() {
  checking.value = true
  try {
    check.value = await organizeApi.embyCheck()
  } catch (e) {
    check.value = { configured: true, error: e instanceof Error ? e.message : String(e) }
  } finally {
    checking.value = false
  }
}

/** 每个 Emby 媒体库按它覆盖到的本站库的刮削方式报问题：交给 Emby 的看下载器，本站刮的看会不会再刮一遍 */
const embyLibs = computed(() =>
  (check.value?.libraries ?? []).map((l) => {
    const modes = l.modes ?? []
    const emby = modes.includes('off') || (!modes.length && embyScrapes.value)
    const station = modes.some((m) => m !== 'off') || (!modes.length && !embyScrapes.value)
    return {
      ...l,
      emby,
      issues: [...(emby ? (l.emby_issues ?? []) : []), ...(station ? (l.station_issues ?? []) : [])],
    }
  }),
)
const badLibs = computed(() => embyLibs.value.filter((l) => l.issues.length))
const embyProblem = computed(() => badLibs.value.some((l) => l.emby && (l.emby_issues ?? []).length))
const stationProblem = computed(() => badLibs.value.some((l) => !l.emby))

/** 一行里写什么：tone 决定图标与颜色 */
const summary = computed<{ tone: 'ok' | 'warn' | 'muted'; text: string }>(() => {
  const c = check.value
  if (!c) return { tone: 'muted', text: '检查中…' }
  if (!c.configured) return { tone: 'muted', text: '未配置 Emby，没法检查' }
  if (c.error) return { tone: 'warn', text: c.error }
  if (c.unmatched) return { tone: 'warn', text: '没有一个 Emby 媒体库的路径对得上本地媒体库，检查 Emby 设置里的路径映射' }
  const n = embyLibs.value.length
  if (!n) return { tone: 'muted', text: 'Emby 里还没有媒体库' }
  if (badLibs.value.length) return { tone: 'warn', text: `${badLibs.value.length} 个库要改（共 ${n} 个）` }
  return { tone: 'ok', text: `${n} 个库都合适` }
})

onMounted(() => {
  void load(true)
  void runCheck()
})
</script>

<template>
  <SectionCard class="emby-strip">
    <div class="row">
      <span class="icon" :class="summary.tone">
        <TriangleAlert v-if="summary.tone === 'warn'" :size="14" />
        <CircleCheck v-else-if="summary.tone === 'ok'" :size="14" />
      </span>
      <span class="text">
        <span class="label">Emby 媒体库设置：</span>
        <span :class="summary.tone">{{ summary.text }}</span>
        <RouterLink v-if="check && !check.configured" :to="{ name: 'settings', query: { tab: 'emby' } }" class="link">
          去配置
        </RouterLink>
      </span>
      <RouterLink :to="LIB_LINK" class="link side" title="每个库刮不刮在库卡片上选，这一页只管怎么刮">
        各库刮不刮
      </RouterLink>
      <HSpinner v-if="checking" size="sm" />
      <HButton v-else variant="ghost" size="sm" icon-only aria-label="重新检查" title="重新检查" @click="runCheck">
        <RefreshCw :size="14" />
      </HButton>
    </div>

    <div v-if="badLibs.length" class="detail">
      <div v-for="l in badLibs" :key="l.name" class="lib">
        <div class="lib-name">{{ l.name }}</div>
        <div class="lib-issues">
          <div v-for="i in l.issues" :key="i">{{ i }}</div>
        </div>
      </div>
      <p v-if="embyProblem" class="foot">
        交给 Emby 刮的库：在 Emby「媒体库 → 编辑媒体库」里勾选元数据下载器与图像获取器（TheMovieDb 等），否则没人刮。
      </p>
      <p v-if="stationProblem" class="foot">
        本站刮的库：到「刮削」页签右侧展开「用本站刮削时，Emby 媒体库要这样设」照着改；不改的话 Emby 会自己再刮一遍，结果以 Emby 的为准。
      </p>
    </div>
  </SectionCard>
</template>

<style scoped>
/* 只有一行，SectionCard 默认的 20px 内边距太空 */
.emby-strip {
  padding: 8px 12px 8px 16px;
  gap: 0;
}
.row {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 32px;
}
.icon {
  display: inline-flex;
  flex-shrink: 0;
}
.icon:empty {
  display: none;
}
.text {
  flex: 1;
  min-width: 0;
  font-size: 13px;
  line-height: 1.6;
}
.label {
  color: var(--muted);
}
.ok {
  color: var(--success);
}
.icon.ok + .text .ok {
  color: var(--foreground);
}
.warn {
  color: var(--warning);
}
.muted {
  color: var(--muted);
}
.link {
  margin-left: 6px;
  font-size: 12.5px;
  color: var(--accent);
  text-decoration: none;
  white-space: nowrap;
}
.link.side {
  margin-left: 0;
}

.detail {
  margin: 6px 0 4px 22px;
  padding-top: 6px;
  border-top: 1px solid var(--separator);
  font-size: 12.5px;
  line-height: 1.6;
}
.lib {
  padding: 4px 0;
}
.lib-name {
  font-weight: 500;
  color: var(--foreground);
}
.lib-issues {
  color: var(--warning);
}
.foot {
  margin: 6px 0 0;
  font-size: 12px;
  color: var(--muted);
}

@media (max-width: 720px) {
  .link.side {
    display: none;
  }
}
</style>
