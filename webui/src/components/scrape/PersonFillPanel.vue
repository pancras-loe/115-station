<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import HButton from '@/components/hero/HButton.vue'
import HCheckbox from '@/components/hero/HCheckbox.vue'
import HNumberInput from '@/components/hero/HNumberInput.vue'
import HPopconfirm from '@/components/hero/HPopconfirm.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import TestBanner, { type BannerState } from '@/components/ui/TestBanner.vue'
import SchedulePicker from '@/components/ui/SchedulePicker.vue'
import SaveBar from '@/pages/scrape/SaveBar.vue'
import { pluginsApi } from '@/api'
import type { PersonDupGroup, PersonFillConfig, PersonFillInfo, PersonType } from '@/api/plugins'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { JOB_STATUS } from '@/utils/jobStatus'
import { useQueueStore } from '@/stores/queue'
import { useScrapeProvider } from '@/composables/scrapeProvider'
import { useMediaServer } from '@/composables/mediaServer'

const { message } = useFeedback()
// 所有库都「不刮（交给 Emby）」：不会再有刮削任务，「刮削后补全」自然不生效，置灰说明
const { embyScrapes } = useScrapeProvider()
const router = useRouter()
const ms = useMediaServer()

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
  nfo_sync: true,
  merge_dups: false,
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
// 读回来时的快照：表单分在两张卡片里、底下一个保存条，靠它提示有没有没保存的改动
const saved = ref('')
const dirty = computed(() => !!saved.value && JSON.stringify(form.value) !== saved.value)

/** onlyInfo：立即运行、清空记账之后只刷新统计，不拿服务器上的配置盖掉没保存的改动 */
async function load(onlyInfo = false) {
  try {
    const d = await pluginsApi.personFillConfig()
    info.value = d.data ?? null
    if (onlyInfo) return
    form.value = { ...DEFAULTS, ...(d.data?.config ?? {}) }
    saved.value = JSON.stringify(form.value)
  } catch (e) {
    toastError(e, '读取配置失败')
  }
}

onMounted(() => load())

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
    const d = await pluginsApi.runPersonFill()
    runResult.value = { status: 'ok', title: d.message || '已加入任务队列', detail: '进度见顶栏任务面板或任务中心' }
    await queue.submitted(d.job_id)
    await load(true)
  } catch (e) {
    runResult.value = { status: 'err', title: '提交失败', detail: e instanceof Error ? e.message : '' }
  } finally {
    running.value = false
  }
}

// 重复人物预览：只读 Emby，看清会留哪个再打开开关
const dups = ref<{ total: number; persons: number; groups: PersonDupGroup[] } | null>(null)
const dupsLoading = ref(false)
async function loadDups() {
  dupsLoading.value = true
  try {
    const d = await pluginsApi.personFillDups()
    dups.value = d.data ?? { total: 0, persons: 0, groups: [] }
  } catch (e) {
    toastError(e, '读取重复人物失败')
  } finally {
    dupsLoading.value = false
  }
}

function openLastJob() {
  const id = info.value?.last_job?.id
  if (!id) return
  void router.push({ name: 'tasks', query: { job: String(id) } })
}
</script>

<template>
  <div class="stack">
    <div class="grid">
      <!-- ==== 左：补什么、写回哪 ==== -->
      <SectionCard title="补全内容" hint="给 Emby 里缺头像、名字不是中文的演职人员补上 TMDB 的头像、中文名与中文简介">
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
        <FieldRow label="每部片演员" tip="每部片只看演员表前 N 位（导演、编剧不占名额）。龙套在 TMDB 上多半既无头像也无中文名。0 = 不限。">
          <HNumberInput v-model="form.max_cast" :min="0" :max="200" aria-label="每部片演员数">
            <template #suffix>位</template>
          </HNumberInput>
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

        <div class="sub-title">写回与整理</div>
        <FieldRow
          label="同步到本地 NFO"
          :hint="embyScrapes ? '所有库都不刮（交给 Emby），本站不改 NFO，这一项不生效' : '设成「不刮（交给 Emby）」的库不改 NFO'"
          tip="Kodi、VidHub 等直接读媒体目录的播放器看的是 NFO，不是 Emby。开启后每补完一部，把它的 NFO 里的演员、导演、编剧名换成中文，演员头像存进片目录下的 .actors（Kodi 约定，优先用 Emby 里那张）。只改人名，NFO 其余内容不动；网盘同步下来的 NFO 不改。"
        >
          <HSwitch v-model="form.nfo_sync" :disabled="embyScrapes" aria-label="同步到本地 NFO" />
        </FieldRow>
        <FieldRow
          label="合并重复人物"
          tip="同一个 TMDB 人物在 Emby 里有好几个条目时（常见：老片挂着英文名的，新刮的片按中文名又建了一个），每次全库扫描开头把它们并成一个：留下中文名、有头像的那个，把挂着重复条目的片目改成挂留下的，确认没有片目再挂着重复条目后才删掉它。名字对不上这个 TMDB 人物的、重复条目同名的不合并。每次最多 50 组。刮削后补全不做这一步。"
        >
          <div class="dup-row">
            <HSwitch v-model="form.merge_dups" :disabled="!!ms.blocked('people_merge')" aria-label="合并重复人物" />
            <HButton
              variant="tertiary"
              size="sm"
              :disabled="!!ms.blocked('people_merge')"
              :loading="dupsLoading"
              @click="loadDups"
            >检查重复</HButton>
            <span v-if="ms.blocked('people_merge')" class="ms-block">{{ ms.name.value }} 下不可用：{{ ms.blocked('people_merge') }}</span>
          </div>
        </FieldRow>
        <div v-if="dups" class="dups">
          <div v-if="!dups.total">{{ dups.persons }} 个人物里没有重复的</div>
          <template v-else>
            <div>
              {{ dups.persons }} 个人物里，{{ dups.total }} 组同一个 TMDB 人物有多个条目<span
                v-if="dups.total > dups.groups.length"
                >（只列前 {{ dups.groups.length }} 组）</span
              >：
            </div>
            <ul>
              <li v-for="g in dups.groups" :key="g.tmdb_id">
                <span class="keep">{{ g.keep.name }}</span>
                <span class="muted"> ← {{ g.drop.map((d) => d.name).join('、') }}</span>
                <span v-if="g.same_name" class="warn">（同名，不合并）</span>
              </li>
            </ul>
          </template>
        </div>
      </SectionCard>

      <!-- ==== 右：什么时候跑、跑到哪了 ==== -->
      <SectionCard title="运行" hint="单独一条人物队列，不挡整理与刮削">
        <template #extra>
          <HButton variant="secondary" size="sm" :loading="running" @click="run">立即运行</HButton>
        </template>
        <FieldRow
          label="刮削后补全"
          :hint="embyScrapes ? '所有库都不刮（交给 Emby），本站不刮削，这一项不生效；用定时运行补全' : undefined"
          tip="每次刮削（整理后、同步后、海报墙手动刮削）结束时，把这次刮过的片目单独排一个补全任务。任务会先等 Emby 读完新的 NFO（最多十来分钟），Emby 里还查不到的留给定时任务。只看这几部，不影响定时任务的续扫位置。"
        >
          <HSwitch v-model="form.after_scrape" :disabled="embyScrapes" aria-label="刮削后补全" />
        </FieldRow>
        <FieldRow
          label="定时运行"
          wide
          tip="开启后按计划把全库扫一遍（单独一条人物队列，不挡整理与刮削），每次从上次停下的片目接着补。「立即运行」不受此开关影响。"
        >
          <SchedulePicker v-model="form.cron" v-model:enabled="form.enabled" toggle placeholder="30 3 * * *" />
        </FieldRow>
        <FieldRow label="单次上限" tip="一次任务最多处理多少个人物，剩下的下次接着补。每个人物约需 1~3 次 TMDB 请求。">
          <HNumberInput v-model="form.max_per_run" :min="1" :max="5000" :step="50" aria-label="单次上限">
            <template #suffix>人</template>
          </HNumberInput>
        </FieldRow>

        <div class="stats">
          <div>已缓存 TMDB 人物 {{ info?.cached ?? 0 }} 个（其中有中文名 {{ info?.cached_zh ?? 0 }} 个）</div>
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
          <HPopconfirm v-if="markTotal || info?.cursor" side="top" @confirm="reset">
            <HButton class="reset" variant="tertiary" size="sm" :loading="resetting">清空记账</HButton>
            <template #content>清空后，下次任务从第一部片目开始，把暂缓的人物全部重新查一遍 TMDB。</template>
          </HPopconfirm>
        </div>

        <TestBanner class="run-result" :state="runResult" />
      </SectionCard>
    </div>

    <SaveBar
      :dirty="dirty"
      :saving="saving"
      note="通过 Emby API 写入，头像经本站的 TMDB 代理下载；不发任何 115 请求"
      @save="save"
    />
  </div>
</template>

<style scoped>
.ms-block {
  font-size: 12.5px;
  color: var(--warning);
}
.stack {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
  align-items: start;
}
/* 卡片里的小节标题：上面一道分隔线，把「补什么」和「写回哪」分开 */
.sub-title {
  margin-top: 8px;
  padding: 14px 0 2px;
  border-top: 1px solid var(--separator);
  font-size: 12px;
  font-weight: 500;
  color: var(--muted);
}
.checks {
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
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
.reset {
  margin-top: 6px;
}
.run-result {
  margin-top: 12px;
}
.dup-row {
  display: flex;
  align-items: center;
  gap: 12px;
}
.dups {
  margin: 0 0 4px;
  padding: 10px 14px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  font-size: 13px;
  line-height: 1.7;
  max-height: 240px;
  overflow: auto;
}
.dups ul {
  margin: 4px 0 0;
  padding-left: 18px;
}
.keep {
  font-weight: 600;
}
.warn {
  color: var(--warning);
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

@media (max-width: 1280px) {
  .grid {
    grid-template-columns: 1fr;
  }
}
</style>
