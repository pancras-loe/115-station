<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import HButton from '@/components/hero/HButton.vue'
import HNumberInput from '@/components/hero/HNumberInput.vue'
import HPopconfirm from '@/components/hero/HPopconfirm.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import TestBanner, { type BannerState } from '@/components/ui/TestBanner.vue'
import SchedulePicker from '@/components/ui/SchedulePicker.vue'
import SaveBar from '@/pages/scrape/SaveBar.vue'
import { pluginsApi } from '@/api'
import type { MetaFillConfig, MetaFillInfo } from '@/api/plugins'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { JOB_STATUS } from '@/utils/jobStatus'
import { useQueueStore } from '@/stores/queue'
import { useScrapeProvider } from '@/composables/scrapeProvider'

const { message } = useFeedback()
const router = useRouter()

const DEFAULTS: MetaFillConfig = {
  enabled: false,
  cron: '0 4 * * *',
  scrape: true,
  probe: true,
  max_titles: 50,
  max_probe: 100,
}

const form = ref<MetaFillConfig>({ ...DEFAULTS })
const info = ref<MetaFillInfo | null>(null)
const saving = ref(false)
const resetting = ref(false)

// 读回来时的快照，保存条据此提示有没有没保存的改动
const saved = ref('')
const dirty = computed(() => !!saved.value && JSON.stringify(form.value) !== saved.value)

/** onlyInfo：立即运行、清空记账之后只刷新统计，不拿服务器上的配置盖掉没保存的改动 */
async function load(onlyInfo = false) {
  try {
    const d = await pluginsApi.metaFillConfig()
    info.value = d.data ?? null
    if (onlyInfo) return
    form.value = { ...DEFAULTS, ...(d.data?.config ?? {}) }
    saved.value = JSON.stringify(form.value)
  } catch (e) {
    toastError(e, '读取配置失败')
  }
}

onMounted(() => load())

// 补刮只补「自动刮新片」的库（后端 execMetaFillJob 同口径）；一个这样的库都没有时开关置灰但保留原值
const { anyAuto, loaded: libsLoaded } = useScrapeProvider()
const noAuto = computed(() => libsLoaded.value && !anyAuto.value)
const scrapeOn = computed(() => form.value.scrape && !noAuto.value)

const invalid = computed(() => {
  if (scrapeOn.value || form.value.probe) return ''
  return noAuto.value ? '没有设成「自动刮新片」的库，补刮不做，请打开探测' : '补刮与探测至少开一项'
})

/** 自动探测规则的说明：和入库后自动探测同一套记账 */
const probeRule = computed(() => {
  const l = info.value?.limits
  return l ? `同一视频最多请求 ${l.max_attempts} 次、两次至少隔 ${l.retry_hours} 小时` : '同一视频最多请求 2 次、两次至少隔 24 小时'
})

async function save() {
  if (invalid.value) {
    message.warning(invalid.value)
    return
  }
  saving.value = true
  try {
    await pluginsApi.saveMetaFill({
      ...form.value,
      cron: form.value.cron.trim() || DEFAULTS.cron,
      max_titles: form.value.max_titles || DEFAULTS.max_titles,
      max_probe: form.value.max_probe || DEFAULTS.max_probe,
    })
    message.success('已保存')
    await load()
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}

async function reset() {
  resetting.value = true
  try {
    const d = await pluginsApi.resetMetaFill()
    message.success(d.message || '已清空')
    await load(true)
  } catch (e) {
    toastError(e, '清空失败')
  } finally {
    resetting.value = false
  }
}

// 跑一次可能几十分钟：入任务队列后立即返回，结果显示在卡片里
const queue = useQueueStore()
const running = ref(false)
const runResult = ref<BannerState | null>(null)

async function run() {
  running.value = true
  try {
    const d = await pluginsApi.runMetaFill()
    runResult.value = { status: 'ok', title: d.message || '已加入任务队列', detail: '扫描完会另建刮削 / 探测任务，进度见任务中心' }
    await queue.submitted(d.job_id)
    await load(true)
  } catch (e) {
    runResult.value = { status: 'err', title: '提交失败', detail: e instanceof Error ? e.message : '' }
  } finally {
    running.value = false
  }
}

function openLastJob() {
  const id = info.value?.last_job?.id
  if (!id) return
  void router.push({ name: 'tasks', query: { job: String(id) } })
}
</script>

<template>
  <SectionCard title="媒体信息补全" hint="定时找出海报墙里「未刮全」的片目、Emby 里缺媒体信息的视频，交给刮削与探测补上">
    <template #extra>
      <HButton variant="secondary" size="sm" :loading="running" @click="run">立即运行</HButton>
    </template>
    <p v-if="noAuto" class="off-note">
      补刮只补设成「自动刮新片」的库（在「账号与媒体库 → 媒体库」里选），现在一个都没有：只做探测。
    </p>

    <FieldRow label="定时运行" wide tip="开启后按计划自动排进任务队列。「立即运行」不受此开关影响。">
      <SchedulePicker v-model="form.cron" v-model:enabled="form.enabled" toggle placeholder="0 4 * * *" />
    </FieldRow>

    <FieldRow
      label="补刮 NFO / 图片"
      tip="写哪些产物沿用「刮削」页签的配置，但一律只补缺失（不覆盖已有文件）、只写本地（传不传网盘看库的「上传到 115」开关）。补刮过、缺的还是那几样的片目（TMDB 上没有、目录名认不出条目）一段时间内不再刮。"
    >
      <HSwitch v-model="form.scrape" :disabled="noAuto" aria-label="补刮 NFO / 图片" />
    </FieldRow>
    <FieldRow label="单次补刮" tip="一次最多交给刮削多少部片目，最近入库的优先，剩下的下次接着补。刮削只请求 TMDB，不发 115 请求。">
      <HNumberInput v-model="form.max_titles" :min="1" :max="1000" :step="10" :disabled="!scrapeOn" aria-label="单次补刮上限">
        <template #suffix>部</template>
      </HNumberInput>
    </FieldRow>

    <FieldRow
      label="探测媒体信息"
      :tip="`让 Emby 对还缺媒体信息的视频提前探测，第一次播放不用现场探。按自动规则：${probeRule}，用完不再自动探（与入库后自动探测共用记账，要再试请在片目详情或任务中心手动探测）。不受「轨道探测」全局开关影响。`"
    >
      <HSwitch v-model="form.probe" aria-label="探测媒体信息" />
    </FieldRow>
    <FieldRow label="单次探测" tip="一次最多请求探测多少个视频。每探测一个视频，Emby 就要经本站 302 取一次 115 直链，间隔 3 秒逐个进行，别设太大。">
      <HNumberInput v-model="form.max_probe" :min="1" :max="1000" :step="10" :disabled="!form.probe" aria-label="单次探测上限">
        <template #suffix>个</template>
      </HNumberInput>
    </FieldRow>

    <div class="stats">
      <div v-if="info && !info.local_root" class="warn">未配置本地媒体库根目录，任务无法运行。</div>
      <div v-if="info && form.probe && !info.emby" class="warn">未配置 Emby，探测这一步会跳过。</div>
      <div>
        <span v-if="info?.marks">
          {{ info.marks }} 部片目补刮过、缺的没变，{{ info.retry_days ?? 30 }} 天内不再刮
        </span>
        <span v-else>没有暂缓的片目</span>
      </div>
      <div v-if="info?.last_job" class="last">
        <span>上次：</span>
        <a class="link" @click="openLastJob">{{ JOB_STATUS[info.last_job.status]?.text ?? info.last_job.status }}</a>
        <span v-if="info.last_job.message" class="muted"> · {{ info.last_job.message }}</span>
      </div>
      <HPopconfirm v-if="info?.marks" side="top" @confirm="reset">
        <HButton class="reset" variant="tertiary" size="sm" :loading="resetting">清空记账</HButton>
        <template #content>清空后，下次把暂缓的片目重新补刮一遍（探测的记账不受影响）。</template>
      </HPopconfirm>
    </div>

    <TestBanner class="run-result" :state="runResult" />

    <SaveBar
      class="bar"
      :dirty="dirty"
      :saving="saving"
      note="扫描本身不发 115 请求；补刮进刮削队列、探测进探测队列，进度在任务中心"
      @save="save"
    />
  </SectionCard>
</template>

<style scoped>
.off-note {
  margin: 0 0 10px;
  font-size: 12.5px;
  line-height: 1.6;
  color: var(--accent);
}
.stats {
  margin-top: 14px;
  padding: 12px 14px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  font-size: 13px;
  line-height: 1.7;
  color: var(--foreground);
}
.warn {
  color: var(--warning);
}
.muted {
  color: var(--muted);
}
.link {
  color: var(--accent);
  cursor: pointer;
}
.reset {
  margin-top: 6px;
}
.run-result {
  margin-top: 12px;
}
.bar {
  margin-top: 14px;
}
</style>
