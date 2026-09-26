<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import HPopconfirm from '@/components/hero/HPopconfirm.vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
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
    void router.push({ query: { tab: 'records', status: 'awaiting' } })
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

async function saveAll(): Promise<boolean> {
  if (!(await resolveAll())) return false
  if (!(await saveCron())) return false
  // org-basic 是整对象覆盖存，而 enrich 那半边在「媒体补全」页签改：
  // 先把库里最新的拉回来，再盖上本页的字段，免得把对面刚存的策略还原成打开本页时的旧值
  const mine = pickMine()
  await load()
  Object.assign(model.value, mine)
  const ok = await save()
  warnOverlap()
  return ok
}

/** 本页管的字段：resolveAll 回填的六个目录 + 人工确认开关 */
function pickMine() {
  const d: Record<string, string | boolean> = { manual_confirm: model.value.manual_confirm }
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
const runHint = '确定开始整理？会扫描待整理目录并搬移文件。'

async function runOrganize() {
  if (!(await checkTmdb())) return
  // 整理接口不带参数，三个目录和补全策略全从库里读——改了没保存就是按旧配置搬文件
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

    <SectionCard title="基础配置" hint="整理引擎的工作目录与定时">
      <HAlert status="warning" class="note">
        自动整理前必须先创建好二级分类策略，并完成一次全量同步。
      </HAlert>

      <FieldRow v-for="d in DIRS" :key="d.key" :label="d.label" :tip="d.tip">
        <Cid115Input
          :ref="(el) => (inputs[d.key] = el as never)"
          v-model="cids[d.key]"
          placeholder="115 目录 cid"
        />
      </FieldRow>

      <FieldRow
        label="人工确认"
        tip="打开后整理识别完就停下：条目原地留在待整理目录，显示在「整理记录 → 待确认」里。确认识别结果，或用 TMDB ID / 片名重新指定后，才继续洗版、重命名、搬移入库、写 STRM 和刮削。没识别出来的也会停在那里等你指定，不再直接移进冗余。"
      >
        <div class="switch-row">
          <HSwitch v-model="model.manual_confirm" />
          <span class="switch-hint">
            {{
              model.manual_confirm
                ? '识别完先停在「整理记录 → 待确认」，确认后才入库'
                : '识别完直接入库（全自动）'
            }}
          </span>
        </div>
      </FieldRow>

      <FieldRow
        label="自动整理 Cron"
        tip="标准 5 字段 cron（分 时 日 月 周）。它只负责「到点跑一遍」，留空不影响下面列出的即时触发。"
      >
        <CronField v-model="cron" placeholder="*/10 8-23 * * *" />
      </FieldRow>

      <HAlert status="accent" class="note-top" title="自动整理的六种触发方式">
        <p class="al-p">
          不管哪种触发，跑的都是同一条流水线：识别 → 二级分类 → 洗版 → 重命名 → 搬入媒体库 →
          写 STRM / 下字幕封面 → 刮削 → 刷新 Emby。区别只在<strong>什么时候开始</strong>和<strong>扫哪个目录</strong>。
          打开「人工确认」后，流水线在识别之后停下，等你在整理记录里确认才接着走。
        </p>
        <ul class="al-ul">
          <li>
            <strong>定时</strong> —— 上面这条 cron，到点扫<strong>待整理目录</strong>（顺带扫一次转存目录）。
            留空只是不再定时跑，下面五种照常工作
          </li>
          <li><strong>手动</strong> —— 下面的「开始整理」按钮，扫的目录同上</li>
          <li>
            <strong>转存完成</strong> —— 影视转存 / 分享转存时勾了「自动整理」，转存成功 3 秒后立即开整
          </li>
          <li>
            <strong>离线下载</strong> —— 提交时勾了整理的话，提交后 10 秒先探一次
            （115 秒传命中说明文件已到位，当场整理），没命中 60 秒后再试；
            任务真正下载完成时，离线监视器会再触发一次（同时发完成通知）
          </li>
          <li>
            <strong>守望者兜底</strong> —— 每分钟看一眼转存目录，有内容且没有别的任务在跑就接管。
            下载完成时间不可控，上面那两次探测扑空时靠它接住，下载完成后约 1 分钟内必被处理（5 分钟冷却）
          </li>
          <li><strong>企微机器人</strong> —— 给机器人发「整理」，或点底部菜单的「自动整理」</li>
        </ul>
        <p class="al-p">
          后三种扫的是<strong>转存目录</strong>（没配转存目录才退回待整理目录），并且整理完会顺手跑一次
          增量同步收尾。整理、增量、全量共用一把任务锁，同一时刻只跑一个；cron 命中时撞上别的任务
          不会整轮丢掉，会在之后每分钟重试直到补上。
        </p>
      </HAlert>

      <FormActions>
        <HButton variant="primary" :loading="saving" @click="saveAll">保存配置</HButton>
        <!-- 改过没保存时走 runOrganize 里的确认框，那里已经问过一次，别再叠一层 popconfirm -->
        <HButton variant="primary" v-if="tmdbReady === false" @click="configureTmdb">配置 TMDB 后开始整理</HButton>
        <HButton variant="tertiary" v-else-if="tmdbReady === null || checkingTmdb" :loading="checkingTmdb" @click="checkTmdb">检查 TMDB 配置</HButton>
        <HPopconfirm v-else-if="!dirty" @confirm="void runOrganize()" danger :disabled="busy">
<HButton variant="danger-soft" :disabled="busy" :loading="running">开始整理</HButton>
<template #content>{{ runHint }}</template>
</HPopconfirm>
        <HButton variant="danger-soft" v-else :disabled="busy" :loading="running" @click="void runOrganize()">
          开始整理
        </HButton>
      </FormActions>
    </SectionCard>
  </div>
</template>

<style scoped>
.alert-action { margin-top: 12px; }
.switch-row {
  display: flex;
  align-items: center;
  gap: 10px;
  min-height: 34px;
}
.switch-hint {
  font-size: 12.5px;
  color: var(--c-text-3);
}
.note-top {
  margin: 4px 0 12px;
}
.al-p {
  margin: 0;
  line-height: 1.85;
}
.al-ul {
  margin: 8px 0;
  padding-left: 18px;
  line-height: 1.85;
}
.al-ul li + li {
  margin-top: 4px;
}
.al-p + .al-p {
  margin-top: 8px;
}
.stack {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.note {
  margin-bottom: 12px;
}
</style>
