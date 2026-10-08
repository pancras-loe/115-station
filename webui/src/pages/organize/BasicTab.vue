<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import { ChevronRight, Info, Play } from '@lucide/vue'
import HPopconfirm from '@/components/hero/HPopconfirm.vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import HNumberInput from '@/components/hero/HNumberInput.vue'
import HSelect from '@/components/hero/HSelect.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import CronField from '@/components/ui/CronField.vue'
import Cid115Input from '@/components/Cid115Input.vue'
import { configApi, organizeApi } from '@/api'
import { useSetting } from '@/composables/useSetting'
import { INCR_DEFAULTS, loadIncrCfg, patchIncrCfg } from '@/composables/incrSetting'
import { useQueueStore } from '@/stores/queue'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { confirmUnsaved } from '@/composables/confirmUnsaved'
import { ORG_BASIC_DEFAULTS } from './orgBasic'

const { message } = useFeedback()
const queue = useQueueStore()
const router = useRouter()
const tmdbReady = ref<boolean | null>(null)
const tmdbError = ref('')
const checkingTmdb = ref(false)
function configureTmdb() {
  void router.push({ path: '/settings', query: { tab: 'tmdb', from: 'organize' } })
}
async function checkTmdb(): Promise<boolean> {
  checkingTmdb.value = true
  tmdbError.value = ''
  try {
    const res = await configApi.getTmdb()
    tmdbReady.value = Boolean((res.data ?? res).api_key?.trim())
    return tmdbReady.value
  } catch {
    tmdbReady.value = null
    tmdbError.value = '无法读取 TMDB 配置，请重试后再开始整理。'
    return false
  } finally {
    checkingTmdb.value = false
  }
}
onMounted(() => { void checkTmdb() })

/** 本页只用得上三个目录，但 org-basic 是整对象存的，默认值得带全——见 orgBasic.ts */
const { model, saving, save, load, dirty: settingDirty } = useSetting('org-basic', ORG_BASIC_DEFAULTS)

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
/** 已有整理在跑、或手动整理已在排队时按钮置灰（排着的定时整理不算：再点会把它提到手动优先级） */
const queuedOrganize = computed(() => queue.activeManualOf('organize'))
const busy = computed(() => running.value || !!queuedOrganize.value)

// 本页提交的整理跑完：有待确认的条目就带用户去确认（此前是同步等待整理结果再跳）
let myJob = 0
const offFinished = queue.onFinished((j) => {
  if (j.id !== myJob || j.status !== 'success') return
  const awaiting = Number(j.result?.awaiting ?? 0)
  if (awaiting > 0) {
    message.info(`识别完成，${awaiting} 项等待人工确认`, { duration: 6000 })
    void router.push({ name: 'tasks', query: { tab: 'records', status: 'awaiting' } })
  }
})
onUnmounted(offFinished)

/**
 * 自动整理的定时开关。存在 setting `incr` 里（历史上整理与增量共用一条 cron），
 * 所以不跟 org-basic 一起存取，只 patch 自己这个字段——见 incrSetting.ts
 */
const cron = ref(INCR_DEFAULTS.cron)
/** 库里那份 cron，用来判断输入框改过没有——incr 不走 useSetting，dirty 得自己记 */
const cronSaved = ref(INCR_DEFAULTS.cron)
onMounted(async () => {
  try {
    cron.value = (await loadIncrCfg()).cron
    cronSaved.value = cron.value
  } catch (e) {
    toastError(e, '定时配置读取失败')
  }
})

async function saveCron(): Promise<boolean> {
  try {
    const v = cron.value.trim()
    await patchIncrCfg({ cron: v })
    cronSaved.value = v
    return true
  } catch (e) {
    toastError(e, '定时配置保存失败')
    return false
  }
}

/**
 * 三个目录输入框只写在 cids 上，要 saveAll 时才经 resolveAll 回填 model，
 * 所以 useSetting 的 dirty 看不见它们，得单独比一次。
 *
 * 比 cid 不比 path：Cid115Input 在路径一改就把 cid 作废（解析成功才填回），
 * 所以改动能立刻看出来；反过来重新选中同一个目录时 cid 不变，也不会误报。
 */
const dirsDirty = computed(() =>
  DIRS.some(({ key }) => cids.value[key].cid.trim() !== (model.value[key] || '').trim()),
)

/** 整理跑的配置全部从库里读，所以本页任何一处没保存都要拦 */
const dirty = computed(
  () => settingDirty.value || dirsDirty.value || cron.value.trim() !== cronSaved.value,
)

/** 三个目录都要在保存前确认 cid 可信；任一失配就整体拦下 */
async function resolveAll(): Promise<boolean> {
  for (const { key, label } of DIRS) {
    const raw = cids.value[key].path.trim()
    // 允许留空。清空也要写回去：以前这里直接跳过，页面显示「有改动」、保存成功，
    // 落库的却还是旧目录，清不掉
    if (!raw) {
      model.value[key] = ''
      model.value[`${key}_path` as const] = ''
      continue
    }
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

async function saveAll(): Promise<boolean> {
  if (!(await resolveAll())) return false
  if (!(await saveCron())) return false
  // org-basic 是整对象覆盖存：先把库里最新的拉回来，再盖上本页的字段，
  // 库里有而本页不管的字段（后端另写的）就不会被打开本页时的旧值还原
  const mine = pickMine()
  await load()
  Object.assign(model.value, mine)
  const ok = await save()
  warnOverlap()
  return ok
}

/** 本页管的字段：resolveAll 回填的六个目录 + 人工确认开关 + 同集多份超时 */
function pickMine() {
  const d: Record<string, string | boolean | number> = {
    manual_confirm: model.value.manual_confirm,
    no_twin_hold: model.value.no_twin_hold,
    dup_auto_hours: model.value.dup_auto_hours,
    dup_auto_action: model.value.dup_auto_action,
  }
  for (const { key } of DIRS) {
    d[key] = model.value[key]
    d[`${key}_path`] = model.value[`${key}_path` as const]
  }
  return d
}

/** 待整理目录可以在媒体库内部（常见布局），只警告「覆盖整个库」这种危险方向 */
function warnOverlap() {
  const norm = (p: string) => p.trim().replace(/\\/g, '/').replace(/\/+$/, '').toLowerCase()
  const p = norm(cids.value.pending.path)
  if (p && p !== '/' && p.length <= 1) {
    message.warning('待整理目录看起来覆盖到整个媒体库，库内条目会被跳过，建议改为库内子目录或与库平级')
  }
}

/** 按钮上的 popconfirm 文案；配置改过时这个气泡不弹，由未保存确认框接管 */
/** 同集多份超时：老配置没有这个字段，HNumberInput 清空时给 null，都按 0（一直等）存 */
/** 「同名待确认」开关：存的是反着的 no_twin_hold（老配置没有这个键 = 开着） */
const twinHold = computed({
  get: () => !model.value.no_twin_hold,
  set: (v: boolean) => {
    model.value.no_twin_hold = !v
  },
})

const dupHours = computed({
  get: () => model.value.dup_auto_hours ?? 0,
  set: (v: number | null) => {
    model.value.dup_auto_hours = Math.max(0, Math.round(v ?? 0))
  },
})
const DUP_ACTIONS = [
  { label: '超时后都保留（#A #B）', value: 'keep_all' as const },
  { label: '超时后留推荐的，没有推荐就都保留', value: 'recommend' as const },
]

const runHint = '确定开始整理？会扫描待整理目录并搬移文件。'

async function runOrganize() {
  if (!(await checkTmdb())) return
  // 整理接口不带参数，三个目录全从库里读——改了没保存就是按旧配置搬文件
  let localFirst = true
  if (dirty.value) {
    if (!(await confirmUnsaved('直接开始会按上次保存的配置搬文件。', saveAll))) {
      return
    }
    // 保存成功后 dirty 归零；选了「按已保存配置开始」就别再拿界面值去校验/回填 model
    localFirst = !dirty.value
  }
  running.value = true
  try {
    if (localFirst && !(await resolveAll())) return
    const d = await organizeApi.runPipeline()
    message.success(d.message)
    myJob = d.job_id
    await queue.submitted(d.job_id)
  } catch (e) {
    toastError(e, '提交整理失败')
  } finally {
    running.value = false
  }
}

/** 流水线各步：一行画出来，比原来一段话描述「识别 → 二级分类 → …」好扫 */
const PIPELINE = ['识别', '二级分类', '洗版', '重命名', '搬入媒体库', '写 STRM', '刮削', '刷新 Emby']

/**
 * 什么时候会整理。原来是一整块常驻的提示框（六条 + 两段话），把配置项挤到折叠线以下；
 * 收进默认折起的 details。内容按 2026-09 入队改造后的真实行为写（AGENTS.md §6.12）：
 * 所有触发都只入任务队列，排着就不会丢 —— 原文里「每分钟重试补上」「5 分钟冷却」已随改造删除
 */
const TRIGGERS: { name: string; text: string }[] = [
  { name: '定时', text: '上面的 cron 到点入队，扫待整理目录（顺带扫一次转存目录）；留空只是不定时跑，其余触发照常' },
  { name: '手动', text: '下面的「开始整理」，扫的目录同上；排队时优先于后台任务' },
  { name: '机器人', text: '给企微 / TG 机器人发「整理」，和手动一样' },
  { name: '转存完成', text: '影视转存（含机器人找资源）的分享转存成功后入队' },
  { name: '离线下载', text: '离线任务下载完成后入队（同时发完成通知）' },
  { name: '守望者', text: '每分钟看一眼转存目录，有内容就接管，兜住上面两种扑空的情况' },
]
</script>

<template>
  <div class="stack">
    <HAlert status="warning" v-if="tmdbReady === false" title="整理前需要配置 TMDB">
      尚未填写 TMDB API 密钥，手动及自动触发的整理均无法识别影视。填写并测试连接后再开始整理。
      <div class="alert-action"><HButton variant="tertiary" size="sm" @click="configureTmdb">去配置</HButton></div>
    </HAlert>
    <HAlert status="danger" v-else-if="tmdbError">
      {{ tmdbError }}
      <div class="alert-action"><HButton variant="tertiary" size="sm" :loading="checkingTmdb" @click="checkTmdb">重新检查</HButton></div>
    </HAlert>

    <div class="grid">
      <!-- ==== 左：怎么跑 ==== -->
      <SectionCard title="运行方式" hint="人工确认与定时；每次整理都走同一条流水线">
        <ol class="pipe" aria-label="整理流水线">
          <li v-for="(s, i) in PIPELINE" :key="s" :class="{ 'is-stop': model.manual_confirm && i === 0 }">
            {{ s }}<ChevronRight v-if="i < PIPELINE.length - 1" :size="12" class="pipe-arrow" />
          </li>
        </ol>
        <p v-if="model.manual_confirm" class="pipe-note">人工确认已开：识别之后停下，等你在整理记录里确认再往下走</p>

        <FieldRow
          label="人工确认"
          tip="打开后整理识别完就停下：条目原地留在待整理目录，显示在「整理记录 → 待确认」里。确认识别结果，或用 TMDB ID / 片名重新指定后，才继续洗版、重命名、搬移入库、写 STRM 和刮削。没识别出来的也会停在那里等你指定，不再直接移进冗余。"
        >
          <div class="switch-row">
            <HSwitch v-model="model.manual_confirm" />
            <span class="switch-hint">
              {{ model.manual_confirm ? '识别完先停在「待确认」，确认后才入库' : '识别完直接入库（全自动）' }}
            </span>
          </div>
        </FieldRow>

        <FieldRow
          label="同名待确认"
          tip="TMDB 上片名完全相同的不止一部（比如《凡人修仙传》2020 年的动画和 2025 年的真人剧），名字里又没有能区分的年份时，整理不再按 TMDB 排第一的去猜，停在「整理记录 → 待确认」等你确认是哪一部。和「人工确认」开关无关，关着人工确认也会停。在这种记录上改指定不会写进识别记忆：名字里没有年份，记下来另一部的内容就会被永远认错。"
        >
          <div class="switch-row">
            <HSwitch v-model="twinHold" />
            <span class="switch-hint">
              {{ twinHold ? '同名分不出是哪一部时停下等你确认' : '同名时取 TMDB 排第一的（可能认错）' }}
            </span>
          </div>
        </FieldRow>

        <FieldRow
          label="同集多份"
          tip="同一次整理里几份不同的文件（比如同一集的粤语、英语两份）按命名规则会改成同一个名字时，不替你挑，停在「整理记录 → 待确认」等你选：留哪一份、都留（名字后加 #A #B）、都不要（移冗余）。通知里也能选：TG 点按钮，企业微信回复「多份 编号 选择」。这里设等多久没人选就自动处理，0 是一直等。"
        >
          <div class="dup-row">
            <HNumberInput v-model="dupHours" :min="0" :max="720" class="dup-hours">
              <template #suffix>小时</template>
            </HNumberInput>
            <HSelect
              v-if="model.dup_auto_hours > 0"
              v-model="model.dup_auto_action"
              :options="DUP_ACTIONS"
              class="dup-action"
              aria-label="超时后怎么处理"
            />
            <span v-else class="switch-hint">一直等你选</span>
          </div>
        </FieldRow>

        <FieldRow
          label="自动整理 Cron"
          tip="标准 5 字段 cron（分 时 日 月 周）。它只负责「到点跑一遍」，留空不影响转存完成、离线下载、守望者这些即时触发。"
        >
          <CronField v-model="cron" placeholder="*/10 8-23 * * *" />
        </FieldRow>

        <!-- 原生 details：展开收起不需要脚本，键盘和读屏也天然可用 -->
        <details class="more">
          <summary><ChevronRight :size="14" class="more-chev" />什么时候会整理（6 种触发）</summary>
          <ul class="triggers">
            <li v-for="t in TRIGGERS" :key="t.name"><b>{{ t.name }}</b><span>{{ t.text }}</span></li>
          </ul>
          <p class="more-foot">
            后三种走同一个「转存触发」任务：扫转存目录（没配才退回待整理目录），整理完再跑一轮增量同步，
            几路同时触发会合并成一个。所有触发都只进任务队列，和同步、深删排同一条队，同一时刻只跑一个，排着不会丢。
          </p>
        </details>
      </SectionCard>

      <!-- ==== 右：在哪些目录之间搬 ==== -->
      <SectionCard title="工作目录" hint="整理从待整理目录取素材，按结果搬到媒体库 / 已存在 / 冗余">
        <FieldRow v-for="d in DIRS" :key="d.key" :label="d.label" :tip="d.tip">
          <Cid115Input
            :ref="(el) => (inputs[d.key] = el as never)"
            v-model="cids[d.key]"
            placeholder="115 目录 cid"
          />
        </FieldRow>
        <p class="dir-note">
          <Info :size="14" />
          <span>
            媒体库目录在「<RouterLink :to="{ name: 'accounts' }">账号与媒体库</RouterLink>」，转存目录在「<RouterLink :to="{ name: 'media-transfer', query: { tab: 'link' } }">影视转存 → 链接转存</RouterLink>」。
            第一次用之前，先配好二级分类策略并跑一次全量同步。
          </span>
        </p>
      </SectionCard>
    </div>

    <!-- 两张卡共用一次保存；「开始整理」挨着保存放，改过没保存时整条高亮 -->
    <div class="save-bar" :class="{ 'is-dirty': dirty }">
      <span class="save-note">{{ dirty ? '有未保存的改动' : '整理会扫描待整理目录并搬移网盘文件' }}</span>
      <HButton variant="primary" v-if="tmdbReady === false" size="sm" @click="configureTmdb">配置 TMDB 后开始整理</HButton>
      <HButton variant="tertiary" v-else-if="tmdbReady === null || checkingTmdb" size="sm" :loading="checkingTmdb" @click="checkTmdb">检查 TMDB 配置</HButton>
      <!-- 改过没保存时走 runOrganize 里的确认框，那里已经问过一次，别再叠一层 popconfirm -->
      <HPopconfirm v-else-if="!dirty" @confirm="void runOrganize()" danger :disabled="busy">
        <HButton variant="danger-soft" size="sm" :disabled="busy" :loading="running">
          <template #icon><Play :size="14" /></template>
          开始整理
        </HButton>
        <template #content>{{ runHint }}</template>
      </HPopconfirm>
      <HButton variant="danger-soft" v-else size="sm" :disabled="busy" :loading="running" @click="void runOrganize()">
        <template #icon><Play :size="14" /></template>
        开始整理
      </HButton>
      <HButton variant="primary" size="sm" :loading="saving" @click="saveAll">保存配置</HButton>
    </div>
  </div>
</template>

<style scoped>
.stack {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.alert-action {
  margin-top: 12px;
}
.grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
  align-items: start;
}

/* ---- 流水线 ---- */
.pipe {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 4px;
  margin: 0 0 14px;
  padding: 10px 12px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  list-style: none;
}
.pipe li {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 12.5px;
  color: var(--foreground);
  white-space: nowrap;
}
.pipe li.is-stop {
  padding: 1px 8px;
  border-radius: 999px;
  background: var(--warning-soft);
  color: var(--warning-soft-foreground);
  font-weight: 500;
}
.pipe-arrow {
  color: var(--muted);
}
.pipe-note {
  margin: -8px 0 12px;
  font-size: 12px;
  color: var(--warning-soft-foreground);
}

.switch-row {
  display: flex;
  align-items: center;
  gap: 10px;
  min-height: 34px;
}
.dup-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}
.dup-hours {
  width: 150px;
}
.dup-action {
  min-width: 0;
  flex: 1 1 220px;
}
.switch-hint {
  font-size: 12.5px;
  color: var(--muted);
}

/* ---- 触发方式（默认折起） ---- */
.more {
  margin-top: 6px;
  border-top: 1px solid var(--separator);
  padding-top: 10px;
}
.more summary {
  display: flex;
  align-items: center;
  gap: 4px;
  list-style: none;
  cursor: pointer;
  font-size: 13px;
  color: var(--muted);
}
.more summary::-webkit-details-marker {
  display: none;
}
.more summary:hover {
  color: var(--foreground);
}
.more-chev {
  transition: transform 150ms ease;
}
.more[open] .more-chev {
  transform: rotate(90deg);
}
.triggers {
  margin: 10px 0 0;
  padding: 0;
  list-style: none;
  font-size: 12.5px;
  line-height: 1.6;
}
.triggers li {
  display: grid;
  grid-template-columns: 64px minmax(0, 1fr);
  gap: 8px;
  padding: 4px 0;
}
.triggers b {
  font-weight: 600;
  color: var(--foreground);
}
.triggers span {
  color: var(--muted);
}
.more-foot {
  margin: 8px 0 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--muted);
}

/* ---- 目录说明 ---- */
.dir-note {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: 4px 0 0;
  font-size: 12.5px;
  line-height: 1.6;
  color: var(--muted);
}
.dir-note :deep(svg) {
  flex-shrink: 0;
  margin-top: 3px;
}
.dir-note a {
  color: var(--accent);
  text-decoration: none;
}
.dir-note a:hover {
  text-decoration: underline;
}

/* ---- 保存条 ---- */
.save-bar {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  flex-wrap: wrap;
  gap: 8px;
  padding: 8px 8px 8px 16px;
  border-radius: var(--r-lg);
  box-shadow: inset 0 0 0 1px var(--border);
  transition:
    background-color 150ms ease,
    box-shadow 150ms ease;
}
.save-bar.is-dirty {
  background: color-mix(in oklab, var(--accent) 8%, transparent);
  box-shadow: inset 0 0 0 1px color-mix(in oklab, var(--accent) 40%, transparent);
}
.save-note {
  margin-right: auto;
  font-size: 12.5px;
  color: var(--muted);
}
.is-dirty .save-note {
  color: var(--accent);
  font-weight: 500;
}

@media (max-width: 1280px) {
  .grid {
    grid-template-columns: 1fr;
  }
}
</style>
