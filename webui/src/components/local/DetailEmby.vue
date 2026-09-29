<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { AudioLines, Captions, ChevronRight, CircleHelp, Film, Radar, RefreshCw } from '@lucide/vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HSkeleton from '@/components/hero/HSkeleton.vue'
import { localApi } from '@/api'
import type { EmbyDetailItem, ProbeState, TitleEmby } from '@/api/local'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { bytes } from '@/utils/format'
import {
  PROBE_TEXT,
  PROBE_TONE,
  audioParts,
  bitrateLabel,
  durationLabel,
  langBrief,
  probeHint,
  subtitleParts,
  videoBrief,
  videoParts,
} from '@/utils/mediaInfo'

/**
 * 片目详情 ·「媒体信息」：Emby 手里每个视频的音视频 / 字幕轨道，以及提前探测的状态。
 *
 * 提前探测的记账是入库后自动探测、手动刮削「轨道探测」、这里的按钮三条入口共用的（防重复取 115 直链）。
 * 所以用户在刮削里勾了探测、有的集却没探，原因都在这一页逐条写明，并把规则摆出来。
 */
const props = defineProps<{ data: TitleEmby | null; loading: boolean; titleKey: string; mediaType: 'movie' | 'tv' }>()
const emit = defineEmits<{ reload: [] }>()

const router = useRouter()
const { message, dialog } = useFeedback()

const items = computed(() => props.data?.items ?? [])
const counts = computed(() => props.data?.counts ?? {})
const n = (s: ProbeState) => counts.value[s] ?? 0
const doneCount = computed(() => n('done'))
/** 点「提前探测」会真正发请求的条目 */
const todo = computed(() => n('none') + n('retry'))
const held = computed(() => n('wait') + n('exhausted'))
const busy = computed(() => n('queued') + n('running'))
const limits = computed(() => props.data?.limits)

const STATE_ORDER: ProbeState[] = ['done', 'running', 'queued', 'none', 'retry', 'wait', 'exhausted', 'disc']
const stateChips = computed(() => STATE_ORDER.filter((s) => n(s) > 0).map((s) => ({ s, count: n(s) })))

const showRules = ref(false)
const probing = ref(false)

async function probe() {
  const count = todo.value
  const ok = await dialog.confirm({
    title: '提前探测',
    content: `让 Emby 探测这 ${count} 个还没有媒体信息的视频。每个都会经本站取一次 115 直链，后台一次一个、间隔 ${limits.value?.gap_seconds ?? 3} 秒${count > 20 ? `，大约要 ${Math.ceil((count * (limits.value?.gap_seconds ?? 3)) / 60)} 分钟以上` : ''}。${held.value ? `另有 ${held.value} 个近期已请求过，这次会跳过。` : ''}`,
    actions: [
      { label: '取消', value: false, variant: 'tertiary' },
      { label: '开始探测', value: true, variant: 'primary' },
    ],
  })
  if (!ok) return
  probing.value = true
  try {
    let d
    try {
      d = await localApi.probeTitle(props.titleKey)
    } catch (e) {
      // 超过 100 个要再确认一次（与手动刮削同一道门槛）
      if ((e as { status?: number }).status !== 409) throw e
      const again = await dialog.confirm({
        title: '再次确认',
        content: `要探测的视频超过 100 个（${count} 个），请求过多有触发 115 风控的风险。确定继续吗？`,
        actions: [
          { label: '取消', value: false, variant: 'tertiary' },
          { label: '确定探测', value: true, variant: 'danger' },
        ],
      })
      if (!again) return
      d = await localApi.probeTitle(props.titleKey, true)
    }
    if (d.queued) message.success(d.message)
    else message.info(d.message)
    emit('reload')
  } catch (e) {
    toastError(e, '提交探测失败')
  } finally {
    probing.value = false
  }
}

// 排队 / 探测中：每 10 秒自己刷一次，直到都探完
let timer: ReturnType<typeof setTimeout> | undefined
watch(
  () => [busy.value, props.loading] as const,
  ([b, l]) => {
    clearTimeout(timer)
    if (b > 0 && !l) timer = setTimeout(() => emit('reload'), 10_000)
  },
)

// ---- 分组与展开 ----

interface Group {
  key: string
  title: string
  items: EmbyDetailItem[]
  done: number
}
const groups = computed<Group[]>(() => {
  if (props.mediaType !== 'tv') return [{ key: 'all', title: '', items: items.value, done: 0 }]
  const m = new Map<number, EmbyDetailItem[]>()
  for (const it of items.value) m.set(it.season ?? 0, [...(m.get(it.season ?? 0) ?? []), it])
  return [...m.entries()]
    .sort((a, b) => a[0] - b[0])
    .map(([s, list]) => ({
      key: `s${s}`,
      title: s === 0 ? '特别篇' : `第 ${s} 季`,
      items: list,
      done: list.filter((i) => i.has_info).length,
    }))
})

/** 单部电影（或只有一个视频）直接展开成大卡片 */
const single = computed(() => props.mediaType !== 'tv' && items.value.length <= 2)

const expanded = ref(new Set<string>())
function toggle(id: string) {
  const s = new Set(expanded.value)
  if (s.has(id)) s.delete(id)
  else s.add(id)
  expanded.value = s
}
const openGroups = ref(new Set<string>())
watch(
  groups,
  (gs) => {
    if (openGroups.value.size) return
    const first = gs.length === 1 ? gs[0] : gs.find((g) => g.done < g.items.length) ?? gs[0]
    if (first) openGroups.value = new Set([first.key])
  },
  { immediate: true },
)
function toggleGroup(k: string) {
  const s = new Set(openGroups.value)
  if (s.has(k)) s.delete(k)
  else s.add(k)
  openGroups.value = s
}

function itemLabel(it: EmbyDetailItem) {
  if (it.episode) return `E${String(it.episode).padStart(2, '0')}`
  return it.type === 'Movie' ? '影片' : '视频'
}
</script>

<template>
  <div class="emby">
    <div v-if="loading && !data" class="sk">
      <HSkeleton width="100%" height="96px" radius="14px" />
      <HSkeleton width="100%" height="44px" radius="12px" />
      <HSkeleton width="100%" height="44px" radius="12px" />
    </div>

    <HAlert v-else-if="data && !data.configured" status="default" title="还没有配置 Emby">
      媒体信息（分辨率、音轨、字幕）来自 Emby。配置后这里会显示每个视频的轨道，以及能否提前探测。
      <template #actions>
        <HButton variant="tertiary" size="sm" @click="router.push({ name: 'settings' })">去系统配置</HButton>
      </template>
    </HAlert>

    <HAlert v-else-if="data?.error" status="danger" title="读取 Emby 失败">
      {{ data.error }}
      <template #actions>
        <HButton variant="tertiary" size="sm" :loading="loading" @click="emit('reload')">重试</HButton>
      </template>
    </HAlert>

    <HAlert v-else-if="data && !data.found" status="warning" title="Emby 里还没有这个片目">
      可能是刚入库、Emby 还没扫描到它；也可能是「系统配置 → Emby」里的路径映射对不上。等 Emby 扫描完成后再来看。
      <template #actions>
        <HButton variant="tertiary" size="sm" :loading="loading" @click="emit('reload')">重新查询</HButton>
      </template>
    </HAlert>

    <template v-else-if="data">
      <!-- 探测概况 -->
      <section class="panel">
        <div class="panel-top">
          <div class="panel-main">
            <div class="panel-title">
              <Radar :size="16" />
              媒体信息 <b>{{ doneCount }}</b> / {{ items.length }}
              <span class="panel-sub">个视频已由 Emby 探测</span>
            </div>
            <!-- 进度而不是负载：MeterBar 是按「越高越危险」配色的，这里越高越好 -->
            <div class="progress" role="progressbar" :aria-valuenow="doneCount" aria-valuemin="0" :aria-valuemax="items.length">
              <div class="progress-fill" :style="{ width: `${items.length ? Math.max(2, (doneCount / items.length) * 100) : 0}%` }" />
            </div>
          </div>
          <HButton variant="ghost" size="sm" icon-only aria-label="刷新" :loading="loading" @click="emit('reload')">
            <RefreshCw :size="15" />
          </HButton>
        </div>
        <div class="chips">
          <HChip v-for="c in stateChips" :key="c.s" :color="PROBE_TONE[c.s]" size="sm">{{ PROBE_TEXT[c.s] }} {{ c.count }}</HChip>
        </div>
        <div class="panel-actions">
          <HButton variant="primary" size="sm" :disabled="!todo" :loading="probing" @click="probe">
            <Radar :size="14" />{{ todo ? `提前探测 ${todo} 个视频` : '提前探测' }}
          </HButton>
          <span class="panel-note">
            <template v-if="todo">第一次播放不用再等 Emby 现场探测。每个视频一次 115 直链请求。</template>
            <template v-else-if="busy">正在后台探测，这里每 10 秒自动刷新。</template>
            <template v-else-if="held">还缺媒体信息的 {{ held }} 个视频近期已请求过，暂时不会再探测（见下方原因）。</template>
            <template v-else-if="doneCount === items.length && items.length">全部视频都已有媒体信息。</template>
            <template v-else>没有可以探测的视频。</template>
          </span>
        </div>
        <p class="auto-line">
          入库后自动探测：<b :class="data.auto_probe ? 'on' : 'off'">{{ data.auto_probe ? '已开启' : '未开启' }}</b>
          <span class="dim">（自动整理 → 影视刮削 →「轨道探测」）</span>
        </p>
        <button type="button" class="rules-toggle" :aria-expanded="showRules" @click="showRules = !showRules">
          <CircleHelp :size="14" />为什么有的视频没被探测？
          <ChevronRight :size="14" class="chev" :class="{ open: showRules }" />
        </button>
        <div v-if="showRules && limits" class="rules">
          <p>
            <b>入库后的自动探测、手动刮削里的「轨道探测」、这里的「提前探测」</b>共用同一份探测记录，
            因为每次探测都要经本站取一次 115 直链，重复探测会平白增加风控风险：
          </p>
          <ul>
            <li>已有媒体信息的视频永远不会再探测；</li>
            <li>同一个视频最多请求 {{ limits.max_attempts }} 次，两次之间至少隔 {{ limits.retry_hours }} 小时（超时也算一次）；</li>
            <li>连续 {{ limits.break_after }} 个视频探测失败，整个队列暂停 {{ limits.break_minutes }} 分钟；</li>
            <li>用完次数的视频不再自动探测，{{ limits.prune_days }} 天后记录过期，才会再给一次机会。</li>
          </ul>
          <p>
            所以刚入库时自动探测过（哪怕失败了）的视频，马上手动刮削勾「轨道探测」也会被跳过 —— 这是预期行为，不是漏了。
            每个视频的具体原因见下方列表，把鼠标移到状态上可以看到。
          </p>
        </div>
      </section>

      <!-- 电影：大卡片 -->
      <template v-if="single">
        <article v-for="it in items" :key="it.id" class="big">
          <header class="big-head">
            <span class="big-name">{{ it.name }}</span>
            <HChip :color="PROBE_TONE[it.probe.state]" size="sm">{{ PROBE_TEXT[it.probe.state] }}</HChip>
          </header>
          <p class="big-meta">
            <span v-if="it.runtime">{{ durationLabel(it.runtime) }}</span>
            <span v-if="it.container">{{ it.container.toUpperCase() }}</span>
            <span v-if="it.size">{{ bytes(it.size) }}</span>
            <span v-if="it.bitrate">{{ bitrateLabel(it.bitrate) }}</span>
          </p>
          <p class="probe-hint" :class="`tone-${PROBE_TONE[it.probe.state]}`">{{ probeHint(it.probe, limits) }}</p>
          <template v-if="it.has_info">
            <div class="tracks">
              <div class="track-group">
                <div class="tg-title"><Film :size="14" />视频</div>
                <div v-for="(v, i) in it.video" :key="i" class="track">{{ videoParts(v).join(' · ') }}</div>
              </div>
              <div class="track-group">
                <div class="tg-title"><AudioLines :size="14" />音轨 {{ it.audio.length }}</div>
                <div v-for="(a, i) in it.audio" :key="i" class="track">
                  {{ audioParts(a).join(' · ') }}
                  <span v-if="a.default" class="pill">默认</span>
                </div>
              </div>
              <div class="track-group">
                <div class="tg-title"><Captions :size="14" />字幕 {{ it.subtitles.length }}</div>
                <div v-for="(s, i) in it.subtitles" :key="i" class="track">
                  {{ subtitleParts(s).join(' · ') }}
                  <span class="pill">{{ s.external ? '外挂' : '内嵌' }}</span>
                  <span v-if="s.default" class="pill">默认</span>
                  <span v-if="s.forced" class="pill">强制</span>
                </div>
                <div v-if="!it.subtitles.length" class="track dim">没有字幕</div>
              </div>
            </div>
          </template>
        </article>
      </template>

      <!-- 剧集 / 多个视频：按季折叠、逐集一行 -->
      <template v-else>
        <div v-for="g in groups" :key="g.key" class="group" :class="{ open: openGroups.has(g.key) || !g.title }">
          <button v-if="g.title" type="button" class="group-head" :aria-expanded="openGroups.has(g.key)" @click="toggleGroup(g.key)">
            <ChevronRight :size="16" class="chev" />
            <span class="group-title">{{ g.title }}</span>
            <span class="dim">{{ g.items.length }} 集</span>
            <span class="group-stat" :class="g.done === g.items.length ? 'ok' : ''">已探测 {{ g.done }}/{{ g.items.length }}</span>
          </button>
          <div v-if="openGroups.has(g.key) || !g.title" class="rows">
            <div v-for="it in g.items" :key="it.id" class="row" :class="{ expanded: expanded.has(it.id) }">
              <button type="button" class="row-main" :aria-expanded="expanded.has(it.id)" @click="toggle(it.id)">
                <span class="r-no">{{ itemLabel(it) }}</span>
                <span class="r-name">{{ it.name }}</span>
                <span class="r-info">
                  <template v-if="it.has_info">
                    <span class="r-video">{{ videoBrief(it) }}</span>
                    <span class="r-meta"><AudioLines :size="12" />{{ it.audio.length }}<span class="r-lang">{{ langBrief(it.audio) }}</span></span>
                    <span class="r-meta"><Captions :size="12" />{{ it.subtitles.length }}</span>
                  </template>
                </span>
                <HChip :color="PROBE_TONE[it.probe.state]" size="sm" class="r-state" :title="probeHint(it.probe, limits)">
                  {{ PROBE_TEXT[it.probe.state] }}
                </HChip>
              </button>
              <div v-if="expanded.has(it.id)" class="row-detail">
                <p class="probe-hint" :class="`tone-${PROBE_TONE[it.probe.state]}`">{{ probeHint(it.probe, limits) }}</p>
                <p v-if="it.runtime || it.size" class="big-meta">
                  <span v-if="it.runtime">{{ durationLabel(it.runtime) }}</span>
                  <span v-if="it.container">{{ it.container.toUpperCase() }}</span>
                  <span v-if="it.size">{{ bytes(it.size) }}</span>
                  <span v-if="it.bitrate">{{ bitrateLabel(it.bitrate) }}</span>
                </p>
                <div v-if="it.has_info" class="tracks compact">
                  <div class="track-group">
                    <div class="tg-title"><Film :size="13" />视频</div>
                    <div v-for="(v, i) in it.video" :key="i" class="track">{{ videoParts(v).join(' · ') }}</div>
                  </div>
                  <div class="track-group">
                    <div class="tg-title"><AudioLines :size="13" />音轨</div>
                    <div v-for="(a, i) in it.audio" :key="i" class="track">
                      {{ audioParts(a).join(' · ') }}<span v-if="a.default" class="pill">默认</span>
                    </div>
                  </div>
                  <div class="track-group">
                    <div class="tg-title"><Captions :size="13" />字幕</div>
                    <div v-for="(s, i) in it.subtitles" :key="i" class="track">
                      {{ subtitleParts(s).join(' · ') }}
                      <span class="pill">{{ s.external ? '外挂' : '内嵌' }}</span>
                      <span v-if="s.default" class="pill">默认</span>
                    </div>
                    <div v-if="!it.subtitles.length" class="track dim">没有字幕</div>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </template>
    </template>
  </div>
</template>

<style scoped>
.emby {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.sk {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.dim {
  color: var(--muted);
}

.panel {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 14px 16px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.panel-top {
  display: flex;
  align-items: flex-start;
  gap: 8px;
}
.panel-main {
  display: flex;
  flex-direction: column;
  gap: 8px;
  flex: 1;
  min-width: 0;
}
.panel-title {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  font-size: 13.5px;
}
.panel-title b {
  font-size: 18px;
  font-variant-numeric: tabular-nums;
}
.panel-sub {
  font-size: 12.5px;
  color: var(--muted);
}
.progress {
  height: 6px;
  border-radius: 999px;
  background: var(--surface);
  overflow: hidden;
}
.progress-fill {
  height: 100%;
  border-radius: inherit;
  background: var(--success);
  transition: width 0.3s;
}
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.panel-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 12px;
}
.panel-note {
  flex: 1 1 220px;
  font-size: 12px;
  line-height: 1.6;
  color: var(--muted);
}
.auto-line {
  margin: 0;
  font-size: 12.5px;
}
.auto-line .on {
  color: var(--success);
}
.auto-line .off {
  color: var(--muted);
}
.rules-toggle {
  all: unset;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  width: fit-content;
  font-size: 12.5px;
  color: var(--accent);
  cursor: pointer;
}
.rules-toggle:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
  border-radius: 4px;
}
.chev {
  flex: none;
  transition: transform 0.15s;
}
.chev.open,
.group.open > .group-head .chev {
  transform: rotate(90deg);
}
.rules {
  padding: 10px 12px;
  border-radius: var(--r-sm);
  background: var(--surface);
  font-size: 12.5px;
  line-height: 1.7;
}
.rules p {
  margin: 0;
}
.rules ul {
  margin: 6px 0;
  padding-left: 20px;
}

.probe-hint {
  margin: 0;
  font-size: 12.5px;
  line-height: 1.6;
}
.tone-success {
  color: var(--success);
}
.tone-warning {
  color: var(--warning);
}
.tone-danger {
  color: var(--danger);
}
.tone-default,
.tone-accent {
  color: var(--muted);
}

.big {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 14px 16px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.big-head {
  display: flex;
  align-items: center;
  gap: 8px;
}
.big-name {
  flex: 1;
  min-width: 0;
  font-size: 14px;
  font-weight: 600;
}
.big-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
  margin: 0;
  font-size: 12px;
  color: var(--muted);
}
.tracks {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 12px;
}
.track-group {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}
.tg-title {
  display: flex;
  align-items: center;
  gap: 5px;
  font-size: 12px;
  font-weight: 600;
  color: var(--muted);
}
.track {
  font-size: 12.5px;
  line-height: 1.6;
}
.pill {
  margin-left: 6px;
  padding: 0 6px;
  border-radius: 999px;
  background: var(--surface);
  font-size: 11px;
  color: var(--muted);
}

.group {
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  overflow: hidden;
}
.group-head {
  all: unset;
  box-sizing: border-box;
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 10px 12px;
  font-size: 12.5px;
  cursor: pointer;
}
.group-head:focus-visible {
  box-shadow: inset 0 0 0 2px var(--focus);
}
.group-title {
  font-size: 13.5px;
  font-weight: 600;
}
.group-stat {
  margin-left: auto;
  font-size: 12px;
  color: var(--warning);
}
.group-stat.ok {
  color: var(--success);
}
.rows {
  display: flex;
  flex-direction: column;
  padding: 0 6px 6px;
}
.group:not(:has(.group-head)) .rows {
  padding-top: 6px;
}
.row {
  border-radius: var(--r-sm);
}
.row.expanded {
  background: var(--surface);
}
.row-main {
  all: unset;
  box-sizing: border-box;
  display: grid;
  grid-template-columns: 40px minmax(0, 1fr) auto auto;
  align-items: center;
  gap: 8px;
  width: 100%;
  min-height: 36px;
  padding: 4px 8px;
  border-radius: var(--r-sm);
  font-size: 12.5px;
  cursor: pointer;
}
.row-main:hover {
  background: var(--surface);
}
.row-main:focus-visible {
  box-shadow: inset 0 0 0 2px var(--focus);
}
.r-no {
  font-variant-numeric: tabular-nums;
  color: var(--muted);
}
.r-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.r-info {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 12px;
  color: var(--muted);
}
.r-video {
  color: var(--foreground);
}
.r-meta {
  display: inline-flex;
  align-items: center;
  gap: 3px;
}
.r-lang {
  margin-left: 3px;
  max-width: 120px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.row-detail {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 4px 12px 12px 56px;
}
.tracks.compact {
  gap: 10px;
}

@media (max-width: 720px) {
  .row-main {
    grid-template-columns: 34px minmax(0, 1fr) auto;
  }
  .r-info {
    grid-column: 2 / 4;
    grid-row: 2;
    flex-wrap: wrap;
    gap: 2px 10px;
  }
  .r-state {
    grid-column: 3;
    grid-row: 1;
  }
  .r-lang {
    display: none;
  }
  .row-detail {
    padding-left: 12px;
  }
}
</style>
