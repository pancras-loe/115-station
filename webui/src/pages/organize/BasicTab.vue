<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NAlert, NButton, NPopconfirm, NRadioButton, NRadioGroup } from 'naive-ui'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import TaskStatusBar from '@/components/TaskStatusBar.vue'
import Cid115Input from '@/components/Cid115Input.vue'
import { organizeApi } from '@/api'
import { useSetting } from '@/composables/useSetting'
import { useTaskStore } from '@/stores/task'
import { toastError, useFeedback } from '@/composables/useFeedback'

const { message } = useFeedback()
const task = useTaskStore()

/**
 * org-basic 同时装着三个目录和「媒体补全」策略——后端就是一个 key，
 * 所以补全页签也保存这份 model（两边都调 save 是有意的，不是重复）。
 */
const { model, saving, save } = useSetting('org-basic', {
  pending: '',
  pending_path: '',
  existing: '',
  existing_path: '',
  redundant: '',
  redundant_path: '',
  enrich: {
    enabled: false,
    mode: 'standard',
    missing: 'rename',
    conflict_low: 'rename',
    conflict_high: 'rename',
    full_named: 'keep',
  },
})

type DirKey = 'pending' | 'existing' | 'redundant'
const DIRS: { key: DirKey; label: string; tip: string }[] = [
  {
    key: 'pending',
    label: '待整理文件夹',
    tip: '存放刚转存或下载的原始资源，整理引擎从这里扫描。建议用非媒体库内的文件夹，如 /影视库/等待整理。',
  },
  {
    key: 'existing',
    label: '已存在文件夹',
    tip: '影视库中已存在相同影视时的重复版本存放目录（洗版用）。如 /影视库/已经存在。',
  },
  {
    key: 'redundant',
    label: '冗余文件夹',
    tip: '存放识别失败的文件、广告图片等无用文件。如 /影视库/冗余文件。',
  },
]

const cids = ref<Record<DirKey, { cid: string; path: string }>>({
  pending: { cid: '', path: '' },
  existing: { cid: '', path: '' },
  redundant: { cid: '', path: '' },
})
const inputs = ref<Record<string, InstanceType<typeof Cid115Input> | null>>({})

watch(
  () => model.value,
  (m) => {
    for (const { key } of DIRS) {
      const cid = m[key]
      const path = m[`${key}_path` as const]
      if (cid && cid !== cids.value[key].cid) cids.value[key] = { cid, path: path || cid }
    }
  },
  { deep: true, immediate: true },
)

const running = ref(false)
const busy = computed(() => running.value || task.status.running)

/** 三个目录都要在保存前确认 cid 可信；任一失配就整体拦下 */
async function resolveAll(): Promise<boolean> {
  for (const { key, label } of DIRS) {
    const raw = cids.value[key].path.trim()
    if (!raw) continue // 允许留空
    const cid = (await inputs.value[key]?.ensureCid()) ?? ''
    if (!cid) {
      message.error(`${label}路径无法识别：请点「选择目录」重新选择，或输入纯数字 cid`)
      return false
    }
    model.value[key] = cid
    model.value[`${key}_path` as const] = cids.value[key].path
  }
  return true
}

async function saveAll() {
  if (!(await resolveAll())) return
  await save()
  warnOverlap()
}

/** 待整理目录可以在媒体库内部（常见布局），只警告「覆盖整个库」这种危险方向 */
function warnOverlap() {
  const norm = (p: string) => p.trim().replace(/\\/g, '/').replace(/\/+$/, '').toLowerCase()
  const p = norm(cids.value.pending.path)
  if (p && p !== '/' && p.length <= 1) {
    message.warning('待整理目录看起来覆盖到整个媒体库，库内条目会被跳过，建议改为库内子目录或与库平级')
  }
}

async function runOrganize() {
  running.value = true
  try {
    if (!(await resolveAll())) return
    message.info('整理任务执行中…')
    task.poll()
    const d = await organizeApi.runPipeline()
    const failed = (d.details ?? []).filter((x) => x.status !== 'success' && x.status !== 'exists').length
    const ok = (d.details ?? []).filter((x) => x.status === 'success').length
    message.success(
      d.message || `整理完成：成功 ${ok}${failed ? `，失败 ${failed}` : ''}，详情见实时日志`,
    )
  } catch (e) {
    toastError(e, '整理失败')
  } finally {
    running.value = false
    task.poll()
  }
}

// ---- 媒体补全 ----
const ENRICH_ROWS = [
  {
    key: 'mode' as const,
    label: '保守度',
    tip: '保守 = 只补缺失信息；标准 = 缺失补充 + 名实冲突按探测修改（完整命名不动）；激进 = 完整命名冲突也按探测修改。',
    options: [
      { v: 'conservative', l: '保守' },
      { v: 'standard', l: '标准' },
      { v: 'aggressive', l: '激进' },
    ],
  },
  {
    key: 'missing' as const,
    label: '缺信息时',
    tip: '如 蜘蛛侠.2016.mkv 探测出 1080p：补充 = 把画质写进文件名；保留 = 保持原名。',
    options: [
      { v: 'rename', l: '补充' },
      { v: 'keep', l: '保留' },
    ],
  },
  {
    key: 'conflict_low' as const,
    label: '探测高于命名',
    tip: '文件名标 1080p 但探测实际是 2160p（发布站低标）。以探测为准 = 改成 2160p。',
    options: [
      { v: 'rename', l: '以探测为准' },
      { v: 'keep', l: '保留命名' },
    ],
  },
  {
    key: 'conflict_high' as const,
    label: '探测低于命名',
    tip: '文件名标 2160p 但探测实际是 1080p（拿 1080p 冒充 4K）。以探测为准 = 改成 1080p。',
    options: [
      { v: 'rename', l: '以探测为准' },
      { v: 'keep', l: '保留命名' },
    ],
  },
  {
    key: 'full_named' as const,
    label: '完整命名冲突',
    tip: '文件名已含来源 + 发布组（如 BluRay-HDS）但与探测不符：专业组命名通常可信，默认保留。跨 3 档极端差异始终保留并记录日志。',
    options: [
      { v: 'keep', l: '保留' },
      { v: 'rename', l: '以探测为准' },
    ],
  },
]
</script>

<template>
  <div class="stack">
    <TaskStatusBar />

    <SectionCard title="基础配置" hint="整理引擎的三个工作目录">
      <NAlert class="note" type="warning" :bordered="false">
        自动整理前必须先创建好二级分类策略，并完成一次全量同步。
      </NAlert>

      <FieldRow v-for="d in DIRS" :key="d.key" :label="d.label" :tip="d.tip">
        <Cid115Input
          :ref="(el) => (inputs[d.key] = el as never)"
          v-model="cids[d.key]"
          placeholder="115 目录 cid"
        />
      </FieldRow>

      <FormActions>
        <NButton type="primary" :loading="saving" @click="saveAll">保存配置</NButton>
        <NPopconfirm @positive-click="void runOrganize()">
          <template #trigger>
            <NButton type="error" ghost :disabled="busy" :loading="running">开始整理</NButton>
          </template>
          确定开始整理？会扫描待整理目录并搬移文件。
        </NPopconfirm>
      </FormActions>
    </SectionCard>

    <SectionCard title="媒体补全" hint="ffprobe 探测真实画质">
      <NAlert class="note" type="info" :bordered="false">
        文件名缺分辨率 / 编码时（如 <code>蜘蛛侠.2016.mkv</code>），用 ffprobe 读取 115
        直链头部探测真实画质（只拉几 MB，不下载全文件），按下方策略规范命名。
        探测是「事实」、命名是「声明」：默认只在缺失或名实冲突时修改；片名 / 集数 / 年份 /
        来源 / 发布组等原有信息一律保留。
      </NAlert>

      <FieldRow label="媒体补全" tip="文件名缺分辨率 / 编码时自动探测规范命名。开启后新整理的文件生效。">
        <NRadioGroup v-model:value="model.enrich.enabled">
          <NRadioButton :value="true">开启</NRadioButton>
          <NRadioButton :value="false">关闭</NRadioButton>
        </NRadioGroup>
      </FieldRow>

      <FieldRow v-for="r in ENRICH_ROWS" :key="r.key" :label="r.label" :tip="r.tip">
        <NRadioGroup v-model:value="model.enrich[r.key]">
          <NRadioButton v-for="o in r.options" :key="o.v" :value="o.v">{{ o.l }}</NRadioButton>
        </NRadioGroup>
      </FieldRow>

      <FormActions>
        <NButton type="primary" :loading="saving" @click="save()">保存策略</NButton>
      </FormActions>
    </SectionCard>
  </div>
</template>

<style scoped>
.stack {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.note {
  margin-bottom: 12px;
}
</style>
