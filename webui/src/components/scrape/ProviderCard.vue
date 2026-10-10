<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { CircleCheck, Clapperboard, RefreshCw, Server, TriangleAlert } from '@lucide/vue'
import HButton from '@/components/hero/HButton.vue'
import HSpinner from '@/components/hero/HSpinner.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import { organizeApi } from '@/api'
import type { EmbyCheck, ScrapeProvider } from '@/api/organize'
import { useScrapeProvider } from '@/composables/scrapeProvider'
import { toastError, useFeedback } from '@/composables/useFeedback'

/**
 * 刮削方式总开关（影视刮削页顶部）：本站刮削，或交给 Emby 自己刮。
 * 选 Emby 时本站一样元数据都不写（后端每个入口都挡，见 scrapeprovider.go），
 * 「刮削」页签整页不生效；轨道探测与演职人员补全走的是 Emby，照常可用。
 * 下面顺带检查 Emby 媒体库的元数据设置合不合当前方式 —— 切到 Emby 却没开下载器，就没人刮了。
 */
const { provider, embyScrapes, load, set } = useScrapeProvider()
const { dialog, message } = useFeedback()

const OPTIONS: { value: ScrapeProvider; title: string; desc: string; icon: typeof Server }[] = [
  {
    value: 'station',
    title: '本站刮削',
    desc: '本站按 TMDB 写 NFO 与海报到 STRM 目录，Emby 只读本地元数据',
    icon: Clapperboard,
  },
  {
    value: 'emby',
    title: 'Emby 刮削',
    desc: 'Emby 自己从 TMDB 拉元数据，本站不写任何 NFO 与图片',
    icon: Server,
  },
]

const switching = ref(false)
async function choose(p: ScrapeProvider) {
  if (p === provider.value || switching.value) return
  if (p === 'emby') {
    const ok = await dialog.confirm({
      title: '改由 Emby 刮削？',
      content:
        '切换后本站不再写 NFO 与图片：整理后、同步后的自动刮削，媒体信息补全里的补刮，海报墙的手动刮削都会停用；轨道探测与演职人员补全照常。\n\n' +
        '本站以前写下的 NFO 与海报不会删除，Emby 读本地 NFO 的优先级比联网刮削高，那些片目仍显示原来的数据。想让 Emby 按自己的重刮，到「海报墙」对它们点「刷新元数据」并选「替换全部」。\n\n' +
        '请确认 Emby 媒体库已勾选元数据下载器与图像获取器（下方有检查）。',
      actions: [
        { label: '取消', value: false, variant: 'tertiary' },
        { label: '改由 Emby 刮削', value: true, variant: 'primary' },
      ],
    })
    if (!ok) return
  }
  switching.value = true
  try {
    const r = await set(p)
    message.success(r.message || '已切换')
  } catch (e) {
    toastError(e, '切换失败')
  } finally {
    switching.value = false
  }
}

// ---- Emby 媒体库设置检查 ----

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

/** 当前方式下每个库的问题；空 = 没问题 */
const libs = computed(() =>
  (check.value?.libraries ?? []).map((l) => ({
    ...l,
    issues: (embyScrapes.value ? l.emby_issues : l.station_issues) ?? [],
  })),
)
const problemCount = computed(() => libs.value.filter((l) => l.issues.length).length)

onMounted(() => {
  void load(true)
  void runCheck()
})
</script>

<template>
  <SectionCard title="刮削方式" hint="谁来给媒体库刮削元数据：二选一">
    <div class="opts" role="radiogroup" aria-label="刮削方式">
      <button
        v-for="o in OPTIONS"
        :key="o.value"
        type="button"
        role="radio"
        class="opt"
        :class="{ on: provider === o.value }"
        :aria-checked="provider === o.value"
        :disabled="switching"
        @click="choose(o.value)"
      >
        <span class="opt-icon"><component :is="o.icon" :size="18" /></span>
        <span class="opt-body">
          <span class="opt-title">{{ o.title }}</span>
          <span class="opt-desc">{{ o.desc }}</span>
        </span>
        <span class="opt-dot" aria-hidden="true" />
      </button>
    </div>

    <p v-if="embyScrapes" class="effect">
      「刮削」页签的设置不生效，媒体信息补全只做探测、不补刮，演职人员的「刮削后补全」停用。
      「<RouterLink :to="{ name: 'local' }">海报墙</RouterLink>」改看 Emby 的海报与识别结果，可以让 Emby 重新刷新元数据。
    </p>

    <!-- Emby 媒体库设置检查：只读，不替用户改 Emby -->
    <div class="check">
      <div class="check-head">
        <span class="check-title">Emby 媒体库设置</span>
        <HSpinner v-if="checking" size="sm" />
        <HButton v-else variant="ghost" size="sm" icon-only aria-label="重新检查" title="重新检查" @click="runCheck">
          <RefreshCw :size="14" />
        </HButton>
      </div>
      <template v-if="check">
        <p v-if="!check.configured" class="check-line muted">
          未配置 Emby，没法检查（<RouterLink :to="{ name: 'settings', query: { tab: 'emby' } }">系统配置 → Emby</RouterLink>）。
        </p>
        <p v-else-if="check.error" class="check-line warn"><TriangleAlert :size="14" />{{ check.error }}</p>
        <p v-else-if="check.unmatched" class="check-line warn">
          <TriangleAlert :size="14" />Emby 里有媒体库，但没有一个的路径对得上本地媒体库：检查 Emby 设置里的路径映射。
        </p>
        <template v-else>
          <div v-for="l in libs" :key="l.name" class="lib">
            <span class="lib-icon" :class="l.issues.length ? 'warn' : 'ok'">
              <TriangleAlert v-if="l.issues.length" :size="14" />
              <CircleCheck v-else :size="14" />
            </span>
            <div class="lib-body">
              <div class="lib-name">{{ l.name }}</div>
              <div v-if="l.issues.length" class="lib-issues">
                <div v-for="i in l.issues" :key="i">{{ i }}</div>
              </div>
              <div v-else class="lib-ok">
                {{ embyScrapes ? `元数据：${l.meta_fetchers.join('、') || '—'}；图像：${l.image_fetchers.join('、') || '—'}` : '设置合适' }}
              </div>
            </div>
          </div>
          <p v-if="problemCount" class="check-foot">
            <template v-if="embyScrapes">
              在 Emby「媒体库 → 编辑媒体库」里勾选元数据下载器与图像获取器（TheMovieDb 等），否则 Emby 不会刮削。
            </template>
            <template v-else>
              到「刮削」页签右侧展开「用本站刮削时，Emby 媒体库要这样设」照着改；不改的话 Emby 会自己再刮一遍，结果以 Emby 的为准。
            </template>
          </p>
        </template>
      </template>
    </div>
  </SectionCard>
</template>

<style scoped>
.opts {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}
.opt {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 14px;
  border: 0;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  box-shadow: inset 0 0 0 1px var(--border);
  color: var(--foreground);
  text-align: left;
  cursor: pointer;
  transition:
    box-shadow 150ms ease,
    background-color 150ms ease;
}
.opt:hover:not(:disabled) {
  box-shadow: inset 0 0 0 1px color-mix(in oklab, var(--accent) 50%, transparent);
}
.opt:disabled {
  cursor: progress;
}
.opt:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
}
.opt.on {
  background: color-mix(in oklab, var(--accent) 8%, var(--surface-secondary));
  box-shadow: inset 0 0 0 2px var(--accent);
}
.opt-icon {
  width: 34px;
  height: 34px;
  flex-shrink: 0;
  border-radius: 10px;
  display: grid;
  place-items: center;
  background: var(--surface);
  color: var(--muted);
}
.opt.on .opt-icon {
  background: var(--accent-soft);
  color: var(--accent);
}
.opt-body {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.opt-title {
  font-size: 14px;
  font-weight: 600;
}
.opt-desc {
  font-size: 12.5px;
  line-height: 1.5;
  color: var(--muted);
}
.opt-dot {
  width: 16px;
  height: 16px;
  flex-shrink: 0;
  border-radius: 50%;
  box-shadow: inset 0 0 0 2px var(--border);
}
.opt.on .opt-dot {
  box-shadow: inset 0 0 0 5px var(--accent);
}

.effect {
  margin: 12px 0 0;
  font-size: 12.5px;
  line-height: 1.7;
  color: var(--muted);
}
.effect a,
.check a {
  color: var(--accent);
  text-decoration: none;
}

.check {
  margin-top: 14px;
  padding: 10px 12px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.check-head {
  display: flex;
  align-items: center;
  gap: 6px;
  min-height: 28px;
}
.check-title {
  flex: 1;
  font-size: 12px;
  font-weight: 500;
  color: var(--muted);
}
.check-line {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: 4px 0;
  font-size: 12.5px;
  line-height: 1.6;
}
.check-line :deep(svg) {
  flex-shrink: 0;
  margin-top: 3px;
}
.muted {
  color: var(--muted);
}
.warn {
  color: var(--warning);
}
.lib {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 6px 0;
}
.lib + .lib {
  border-top: 1px solid var(--separator);
}
.lib-icon {
  flex-shrink: 0;
  margin-top: 2px;
}
.lib-icon.ok {
  color: var(--success);
}
.lib-body {
  flex: 1;
  min-width: 0;
  font-size: 12.5px;
  line-height: 1.6;
}
.lib-name {
  font-weight: 500;
  color: var(--foreground);
}
.lib-issues {
  color: var(--warning);
}
.lib-ok {
  color: var(--muted);
  overflow-wrap: anywhere;
}
.check-foot {
  margin: 6px 0 2px;
  font-size: 12px;
  line-height: 1.6;
  color: var(--muted);
}

@media (max-width: 720px) {
  .opts {
    grid-template-columns: 1fr;
  }
}
</style>
