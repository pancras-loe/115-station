<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { TriangleAlert } from '@lucide/vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HCheckbox from '@/components/hero/HCheckbox.vue'
import HModal from '@/components/hero/HModal.vue'
import type { FullRunPlan } from '@/pages/strm/useStrmRuns'

/**
 * 「开始全量同步」的确认框（2026-10-23）：把这次扫什么写全再让用户点头。
 * 扫描范围的设置在页签里排得靠后，新用户可能没看到就点了开始 —— 首次全量会按这些后缀把网盘上的文件
 * 全部生成 STRM / 下载到本地；开着失效检测时，后缀比上一次少了，原先同步过的那些文件会被判为失效 STRM
 */
const props = defineProps<{ plan: FullRunPlan | null }>()

const show = computed({
  get: () => !!props.plan,
  set: (v) => {
    if (!v) props.plan?.resolve(null)
  },
})

const selectedIds = ref<number[]>([])
const libs = computed(() => props.plan?.libs ?? [])
const selectedLibs = computed(() => libs.value.filter((lib) => selectedIds.value.includes(lib.id)))
watch(
  () => props.plan,
  () => { selectedIds.value = libs.value.map((lib) => lib.id) },
)

function toggleLib(id: number, checked: boolean) {
  if (checked) selectedIds.value = [...selectedIds.value, id]
  else selectedIds.value = selectedIds.value.filter((selected) => selected !== id)
}

/** 后缀统一成不带点、小写再比，配置里两种写法都有 */
const norm = (e: string) => e.trim().replace(/^\./, '').toLowerCase()
function dropped(now: string[], before?: string[]) {
  if (!before?.length) return []
  const cur = new Set(now.map(norm))
  return before.map(norm).filter((e) => e && !cur.has(e))
}

const groups = computed(() => {
  const p = props.plan
  if (!p) return []
  const fields = [
    { key: 'video_ext', label: '视频（生成 STRM）' },
    { key: 'image_ext', label: '图片（下载到本地）' },
    { key: 'data_ext', label: '数据（下载到本地，.nfo 始终包含）' },
  ] as const
  return fields.map(({ key, label }) => {
    const exts = p.cfg[key]
    const gone = new Map<string, string[]>()
    for (const lib of selectedLibs.value) {
      const previous = lib.last_sync?.last_scan ?? (lib.last_sync ? p.legacyScan : null)
      const missing = dropped(exts, previous?.[key]).sort().join(' ')
      if (!missing) continue
      const names = gone.get(missing) ?? []
      names.push(lib.name)
      gone.set(missing, names)
    }
    return { key, label, exts, gone: [...gone] }
  })
})
const anyGone = computed(() => groups.value.some((g) => g.gone.length))
const firstRun = computed(() => selectedLibs.value.some((lib) => !lib.last_sync))
</script>

<template>
  <HModal v-model:show="show" title="开始全量同步？" width="600px">
    <div v-if="plan" class="body">
      <p class="lead">
        {{
          plan.mode === 'fast'
            ? '用快速模式扫描下面这些库并生成 STRM。快速模式请求少，接口不可用时会自动降级为标准模式。'
            : '用标准模式逐个目录扫描下面这些库并生成 STRM，库大时耗时较长、115 请求较多。'
        }}
      </p>

      <HAlert v-if="plan.libs === null" status="warning">媒体库列表加载失败，请关闭窗口后重试。</HAlert>
      <HAlert v-else-if="!libs.length" status="default">请先到「账号与媒体库 → 媒体库」添加媒体库。</HAlert>
      <section v-else class="block">
        <div class="lib-toolbar">
          <h4>选择媒体库（已选 {{ selectedLibs.length }} / {{ libs.length }}）</h4>
          <div class="lib-actions">
            <HButton variant="tertiary" size="sm" :disabled="selectedLibs.length === libs.length" @click="selectedIds = libs.map((l) => l.id)">全选</HButton>
            <HButton variant="tertiary" size="sm" :disabled="!selectedLibs.length" @click="selectedIds = []">清空</HButton>
          </div>
        </div>
        <ul class="libs">
          <li v-for="l in libs" :key="l.id">
            <HCheckbox :checked="selectedIds.includes(l.id)" @update:checked="toggleLib(l.id, $event)">
              <span class="lib-label">
                <b>{{ l.name }}</b>
                <span class="mono">{{ l.path || `cid ${l.cid}` }}</span>
                <span class="dim">{{ l.account_name }}</span>
              </span>
            </HCheckbox>
          </li>
        </ul>
      </section>

      <section class="block">
        <h4>扫描范围</h4>
        <dl class="exts">
          <template v-for="g in groups" :key="g.key">
            <dt>{{ g.label }}</dt>
            <dd>
              <span class="mono">{{ g.exts.length ? g.exts.join(' ') : '无' }}</span>
              <span v-for="[exts, names] in g.gone" :key="exts" class="gone">
                <TriangleAlert :size="13" />少了 {{ exts }}（{{ names.join('、') }}）
              </span>
            </dd>
          </template>
        </dl>
      </section>

      <section class="block">
        <h4>同步完之后</h4>
        <ul class="after">
          <li>
            失效 STRM 检测：<b>{{ plan.cfg.detect_orphans ? '开' : '关' }}</b>
            <span v-if="plan.cfg.detect_orphans" class="dim">（只检查所选库，清理需手动确认）</span>
          </li>
          <li>
            结束后刷新 Emby：<b>{{ plan.cfg.refresh_emby ? '开' : '关' }}</b>
            <span v-if="plan.cfg.refresh_emby" class="dim">（按有新文件的库通知，共用一个 Emby 库时可能重扫共享库）</span>
          </li>
        </ul>
      </section>

      <HAlert v-if="anyGone && plan.cfg.detect_orphans" status="warning" class="note">
        所选库中，按上面移除的后缀同步过的文件可能被标成失效 STRM。可以取消并到「扫描范围」恢复这些后缀。
      </HAlert>
      <HAlert v-else-if="anyGone" status="default" class="note">
        本次扫描排除所列后缀，已有本地文件会保留。
      </HAlert>
      <HAlert v-if="firstRun" status="default" class="note">
        首次同步的库会为符合后缀条件的视频生成 STRM，并下载符合条件的附属文件。
      </HAlert>
    </div>
    <template #footer>
      <HButton variant="tertiary" @click="plan?.resolve(null)">取消</HButton>
      <HButton variant="primary" :disabled="!selectedLibs.length" @click="plan?.resolve(selectedLibs.map((l) => l.id))">
        同步所选 {{ selectedLibs.length }} 个库
      </HButton>
    </template>
  </HModal>
</template>

<style scoped>
.body {
  display: flex;
  flex-direction: column;
  gap: 14px;
  font-size: 13px;
}
.lead {
  margin: 0;
  line-height: 1.6;
  color: var(--muted);
}
.block h4 {
  margin: 0 0 6px;
  font-size: 12.5px;
  font-weight: 600;
  color: var(--foreground);
}
.libs,
.after {
  list-style: none;
  margin: 0;
  padding: 8px 12px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  line-height: 1.9;
}
.lib-toolbar,
.lib-actions {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
}
.lib-toolbar h4 {
  margin-bottom: 0;
}
.libs {
  max-height: 240px;
  overflow-y: auto;
}
.lib-label {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 2px 10px;
  min-width: 0;
}
.mono {
  font-family: var(--font-mono);
  font-size: 12px;
  overflow-wrap: anywhere;
}
.dim {
  font-size: 12px;
  color: var(--muted);
}
.exts {
  display: grid;
  grid-template-columns: max-content minmax(0, 1fr);
  gap: 6px 12px;
  margin: 0;
  padding: 8px 12px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.exts dt {
  font-size: 12px;
  color: var(--muted);
}
.exts dd {
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.gone {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
  color: var(--warning-soft-foreground);
}
.gone svg {
  flex-shrink: 0;
}
.note {
  margin: 0;
}
@media (max-width: 720px) {
  .exts {
    grid-template-columns: 1fr;
  }
}
</style>
