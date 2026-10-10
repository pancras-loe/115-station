<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import HButton from '@/components/hero/HButton.vue'
import HCheckbox from '@/components/hero/HCheckbox.vue'
import HNumberInput from '@/components/hero/HNumberInput.vue'
import HPopconfirm from '@/components/hero/HPopconfirm.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import TestBanner, { type BannerState } from '@/components/ui/TestBanner.vue'
import SchedulePicker from '@/components/ui/SchedulePicker.vue'
import { pluginsApi } from '@/api'
import type { PersonFillConfig, PersonFillInfo, PersonType } from '@/api/plugins'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { JOB_STATUS } from '@/utils/jobStatus'
import { useQueueStore } from '@/stores/queue'

const { message } = useFeedback()
const router = useRouter()

const DEFAULTS: PersonFillConfig = {
  after_scrape: false,
  enabled: false,
  cron: '30 3 * * *',
  types: ['actor', 'director', 'writer'],
  image: true,
  zh_name: true,
  zh_bio: true,
  max_cast: 20,
  max_per_run: 300,
}

const TYPES: { v: PersonType; label: string }[] = [
  { v: 'actor', label: '演员' },
  { v: 'director', label: '导演' },
  { v: 'writer', label: '编剧' },
]

const form = ref<PersonFillConfig>({ ...DEFAULTS })
const info = ref<PersonFillInfo | null>(null)
const saving = ref(false)
const resetting = ref(false)

async function load() {
  try {
    const d = await pluginsApi.personFillConfig()
    info.value = d.data ?? null
    form.value = { ...DEFAULTS, ...(d.data?.config ?? {}) }
  } catch (e) {
    toastError(e, '读取配置失败')
  }
}

onMounted(load)

function toggleType(t: PersonType, on: boolean) {
  const s = new Set(form.value.types)
  if (on) s.add(t)
  else s.delete(t)
  form.value.types = TYPES.map((x) => x.v).filter((v) => s.has(v))
}

const invalid = computed(() => {
  if (!form.value.types.length) return '至少勾选一类人物'
  if (!form.value.image && !form.value.zh_name && !form.value.zh_bio) return '头像、中文名、中文简介至少开一项'
  return ''
})

/** 记账统计：这些人物查过了但没补全，到期前任务不再碰它们 */
const markRows = computed(() => {
  const m = info.value?.marks ?? {}
  const t = info.value?.state_text ?? {}
  return Object.entries(m)
    .filter(([, n]) => n > 0)
    .map(([k, n]) => ({ k, text: t[k] ?? k, n }))
})
const markTotal = computed(() => markRows.value.reduce((s, r) => s + r.n, 0))

async function save() {
  if (invalid.value) {
    message.warning(invalid.value)
    return
  }
  saving.value = true
  try {
    await pluginsApi.savePersonFill({
      ...form.value,
      cron: form.value.cron.trim() || DEFAULTS.cron,
      max_cast: form.value.max_cast ?? 0,
      max_per_run: form.value.max_per_run || DEFAULTS.max_per_run,
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
    const d = await pluginsApi.resetPersonFill()
    message.success(d.message || '已清空')
    await load()
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
    const d = await pluginsApi.runPersonFill()
    runResult.value = { status: 'ok', title: d.message || '已加入任务队列', detail: '进度见顶栏任务面板或任务中心' }
    await queue.submitted(d.job_id)
    await load()
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
  <SectionCard title="演职人员补全">
    <template #extra>
      <HButton variant="secondary" size="sm" :loading="running" @click="run">立即运行</HButton>
    </template>
    <p class="lead">
      给 Emby 里缺头像、名字不是中文的演职人员补上 TMDB 的头像、中文名与中文简介。
      通过 Emby API 写入（人物头像存在 Emby 自己的元数据目录里，不在媒体文件夹旁边），
      头像经本站的 TMDB 代理下载，Emby 连不上 TMDB 也能补。不发任何 115 请求。
    </p>

    <FieldRow
      label="刮削后补全"
      tip="每次刮削（整理后、同步后、本地文件页手动刮削）结束时，把这次刮过的片目单独排一个补全任务。任务会先等 Emby 读完新的 NFO（最多十来分钟），Emby 里还查不到的留给定时任务。只看这几部，不影响定时任务的续扫位置。"
    >
      <HSwitch v-model="form.after_scrape" aria-label="刮削后补全" />
    </FieldRow>
    <FieldRow
      label="定时运行"
      wide
      tip="开启后按计划把全库扫一遍（单独一条人物队列，不挡整理与刮削），每次从上次停下的片目接着补。「立即运行」不受此开关影响。"
    >
      <SchedulePicker v-model="form.cron" v-model:enabled="form.enabled" toggle placeholder="30 3 * * *" />
    </FieldRow>
    <FieldRow label="人物类型" tip="演员包括剧集的客串。剧集的导演、编剧挂在每一集上，勾选后会额外读取各集的人员表。">
      <div class="checks">
        <HCheckbox
          v-for="t in TYPES"
          :key="t.v"
          :checked="form.types.includes(t.v)"
          @update:checked="(v) => toggleType(t.v, v)"
        >
          {{ t.label }}
        </HCheckbox>
      </div>
    </FieldRow>
    <FieldRow label="补头像" tip="只补 Emby 里还没有头像的人物，已有的不覆盖。">
      <HSwitch v-model="form.image" aria-label="补头像" />
    </FieldRow>
    <FieldRow
      label="中文名"
      tip="名字不含汉字的人物改成 TMDB 上的中文名（翻译或别名，繁体统一转简体），并锁定名字字段，免得 Emby 刷新时又改回去。若同名的人物已存在则不改名，只补头像。补过的中文名也会用于之后刮削写 NFO 的演员表。"
    >
      <HSwitch v-model="form.zh_name" aria-label="中文名" />
    </FieldRow>
    <FieldRow label="中文简介" tip="原简介为空或不是中文、而 TMDB 有中文简介时替换，同样锁定字段。已有头像和中文名、只缺中文简介的人物也会处理。">
      <HSwitch v-model="form.zh_bio" aria-label="中文简介" />
    </FieldRow>
    <FieldRow label="每部片演员" tip="每部片只看演员表前 N 位（导演、编剧不占名额）。龙套在 TMDB 上多半既无头像也无中文名。0 = 不限。">
      <HNumberInput v-model="form.max_cast" :min="0" :max="200" aria-label="每部片演员数">
        <template #suffix>位</template>
      </HNumberInput>
    </FieldRow>
    <FieldRow label="单次上限" tip="一次任务最多处理多少个人物，剩下的下次接着补。每个人物约需 1~3 次 TMDB 请求。">
      <HNumberInput v-model="form.max_per_run" :min="1" :max="5000" :step="50" aria-label="单次上限">
        <template #suffix>人</template>
      </HNumberInput>
    </FieldRow>

    <div class="stats">
      <div class="stats-head">
        <span>
          已缓存 TMDB 人物 {{ info?.cached ?? 0 }} 个（其中有中文名 {{ info?.cached_zh ?? 0 }} 个）
        </span>
      </div>
      <div>
        <span v-if="info?.cursor">上次没看完（单次上限 / 手动停止 / Emby 读取中断），下次从第 {{ info.cursor + 1 }} 部片目接着补，看到末尾再从头补到这里</span>
        <span v-else>下次从第一部片目开始（按加入时间排序）</span>
      </div>
      <div v-if="markTotal" class="marks">
        <span>暂缓 {{ markTotal }} 个人物：</span>
        <span v-for="r in markRows" :key="r.k" class="mark">{{ r.text }} {{ r.n }}</span>
        <span class="muted">（查无结果的一个月后再试，出错的次日再试）</span>
      </div>
      <div v-if="info?.last_job" class="last">
        <span>上次：</span>
        <a class="link" @click="openLastJob">{{ JOB_STATUS[info.last_job.status]?.text ?? info.last_job.status }}</a>
        <span v-if="info.last_job.message" class="muted"> · {{ info.last_job.message }}</span>
      </div>
    </div>

    <TestBanner :state="runResult" />

    <FormActions>
      <HButton variant="primary" :loading="saving" :disabled="!!invalid" @click="save">保存</HButton>
      <HPopconfirm v-if="markTotal || info?.cursor" side="top" @confirm="reset">
        <HButton variant="tertiary" :loading="resetting">清空记账</HButton>
        <template #content>清空后，下次任务从第一部片目开始，把暂缓的人物全部重新查一遍 TMDB。</template>
      </HPopconfirm>
    </FormActions>
  </SectionCard>
</template>

<style scoped>
.lead {
  margin: 0 0 14px;
  font-size: 13px;
  line-height: 1.6;
  color: var(--muted);
}
.checks {
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
}
.stats {
  margin: 14px 0;
  padding: 12px 14px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  font-size: 13px;
  line-height: 1.7;
  color: var(--foreground);
}
.marks .mark {
  margin-right: 10px;
}
.muted {
  color: var(--muted);
}
.link {
  color: var(--accent);
  cursor: pointer;
}
</style>
