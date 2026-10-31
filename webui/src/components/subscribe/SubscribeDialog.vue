<script setup lang="ts">
import MediaTypeChip from '@/components/MediaTypeChip.vue'
import { computed, ref, watch } from 'vue'
import { ChevronDown } from '@lucide/vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import HModal from '@/components/hero/HModal.vue'
import HMultiSelect from '@/components/hero/HMultiSelect.vue'
import HNumberInput from '@/components/hero/HNumberInput.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSelect from '@/components/hero/HSelect.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import PosterImage from '@/components/PosterImage.vue'
import SubCondFields from '@/components/subscribe/SubCondFields.vue'
import SubPriorityFields from '@/components/subscribe/SubPriorityFields.vue'
import { resourcesApi } from '@/api'
import * as subscribeApi from '@/api/subscribe'
import type { SubForm, Subscription, TmdbSeason } from '@/api/subscribe'
import { useTransferSources } from '@/composables/transferSources'
import { stagingSubChoices, useStagingSubs } from '@/composables/stagingSubs'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { useQueueStore } from '@/stores/queue'

/**
 * 订阅弹窗：新建（找资源页选定 TMDB 条目后）与修改（订阅页）共用。
 * 默认值按维护者定的：补缺集；剧集还在播时预选最新一季（全剧往往要补几百集，多半不是想要的）。
 */
export interface SubTarget {
  tmdb_id: number
  media_type: 'movie' | 'tv'
  title: string
  year?: string
  poster?: string
}

const props = defineProps<{ target?: SubTarget | null; edit?: Subscription | null }>()
const show = defineModel<boolean>('show', { required: true })
const emit = defineEmits<{ saved: [s: Subscription] }>()

const { message } = useFeedback()
const queue = useQueueStore()
const { state: srcState } = useTransferSources()
const { items: stagingItems, v2: libsV2 } = useStagingSubs()
/** 「转存到」：跟随设置 / 转存目录本身 / 各绑定子目录。只在有绑定时显示（按来源分库） */
const subdirOptions = computed(() => {
  // 媒体库表（V2）：选的是库（只能是主号的库，订阅的包装目录、等秒传还只走主号），跟随 = 订阅设置 / 默认转存库
  const out = libsV2.value
    ? [{ label: '跟随订阅设置（默认转存库）', value: '' }, ...stagingSubChoices({ mainOnly: true })]
    : [{ label: '跟随订阅设置', value: '' }, { label: '转存目录（按分类选库）', value: 'root' }, ...stagingSubChoices()]
  // 选过的子目录后来解绑了：仍显示出来，免得下拉是空的、看不出原来选了什么
  const cur = form.value.subdir
  if (cur && !out.some((o) => o.value === cur)) out.push({ label: '已解绑的子目录（会转存到转存目录）', value: cur })
  return out
})

const blank = (): SubForm => ({
  scope: 'all',
  season: 1,
  ep_start: 1,
  ep_end: 0,
  specials: false,
  follow: 'missing',
  sources: [],
  rank_limit: 0,
  include: '',
  exclude: '',
  offline_mode: '',
  delivery: '',
  cond: null,
  priority: null,
  wash: false,
  subdir: '',
})
const form = ref<SubForm>(blank())
const seasons = ref<TmdbSeason[]>([])
const ended = ref(false)
const loadingSeasons = ref(false)
const advanced = ref(false)
const submitting = ref(false)

const head = computed(() => {
  if (props.edit) {
    const e = props.edit
    return { tmdb_id: e.tmdb_id, media_type: e.media_type, title: e.title, year: e.year, poster: e.poster_path }
  }
  return props.target ?? null
})
const tv = computed(() => head.value?.media_type === 'tv')

watch(show, async (open) => {
  if (!open) return
  form.value = props.edit ? subscribeApi.toForm(props.edit) : blank()
  if (!props.edit) {
    // 新建：洗版默认值跟订阅设置
    subscribeApi
      .getConfig()
      .then((d) => {
        if (!props.edit && show.value) form.value.wash = !!d.data.wash_default
      })
      .catch(() => {})
  }
  advanced.value = !!props.edit && (form.value.sources.length > 0 || !!form.value.rank_limit || !!form.value.include || !!form.value.exclude || !!form.value.offline_mode || !!form.value.delivery || !!form.value.cond || !!form.value.priority)
  seasons.value = []
  ended.value = false
  if (!tv.value || !head.value) return
  loadingSeasons.value = true
  try {
    const d = await subscribeApi.seasons(head.value.tmdb_id)
    seasons.value = d.data ?? []
    ended.value = !!d.ended
    const latest = Math.max(0, ...seasons.value.map((s) => s.season))
    if (!props.edit && !ended.value && latest > 1) {
      form.value.scope = 'season'
      form.value.season = latest
    } else if (!props.edit && latest > 0) {
      form.value.season = latest
    }
  } catch (e) {
    toastError(e, '读取季列表失败')
  } finally {
    loadingSeasons.value = false
  }
})

const seasonOptions = computed(() =>
  seasons.value.map((s) => ({
    label: `${s.season === 0 ? '特别篇' : `第 ${s.season} 季`} · ${s.episodes} 集${s.air_date ? ` · ${s.air_date.slice(0, 4)}` : ''}`,
    value: s.season,
  })),
)
const sourceOptions = computed(() =>
  (srcState.value?.sources ?? []).map((s) => ({ label: s.enabled ? s.label : `${s.label}（已关闭）`, value: s.key })),
)
const epEnd = computed({
  get: () => (form.value.ep_end > 0 ? form.value.ep_end : null),
  set: (v) => (form.value.ep_end = v ?? 0),
})

// 资源优先级：空串 = 跟随订阅设置（priority 为 null）。改成自定义时从订阅设置的自定义规则抄一份起步，
// 订阅设置不是自定义就给两条常见的（4K、1080p）
const prioMode = computed({
  get: () => form.value.priority?.mode ?? '',
  set: (v: subscribeApi.PrioMode | '') => {
    if (!v) {
      form.value.priority = null
      return
    }
    form.value.priority = { mode: v, rules: form.value.priority?.rules ?? [] }
    if (v === 'custom' && !form.value.priority.rules?.length) void seedCustomRules()
  },
})
const prioRules = computed({
  get: () => form.value.priority?.rules ?? [],
  set: (rs: subscribeApi.WashRule[]) => {
    if (form.value.priority) form.value.priority.rules = rs
  },
})
async function seedCustomRules() {
  let rules: subscribeApi.WashRule[] = [{ resource_pix: '2160p,4k' }, { resource_pix: '1080p,1080i' }]
  try {
    const d = await subscribeApi.getConfig()
    if (d.data.priority?.mode === 'custom' && d.data.priority.rules?.length) rules = d.data.priority.rules
  } catch {
    // 读不到就用默认的两条
  }
  if (form.value.priority?.mode === 'custom' && !form.value.priority.rules?.length) form.value.priority.rules = rules
}

// 中字与大小改成自定义时从订阅设置的默认值抄一份起步，用户多半只是在默认上改一两项
async function setCondMode(v: string | undefined) {
  if (v !== 'custom') {
    form.value.cond = null
    return
  }
  form.value.cond = subscribeApi.blankCond()
  try {
    const d = await subscribeApi.getConfig()
    if (form.value.cond) form.value.cond = { ...subscribeApi.blankCond(), ...d.data.cond }
  } catch {
    // 读不到默认值就从空的开始
  }
}

const offlineHint = computed(() => {
  switch (form.value.offline_mode) {
    case 'pack':
      return tv.value
        ? '磁力挑不了集，115 的离线配额又按任务数扣：标题写着单集的磁力不下，只下季包 / 多集合集'
        : '电影一个磁力就是整部，照常下'
    case 'share_only':
      return '只转存 115 分享，不用离线配额'
    case 'any':
      return '单集磁力也下，追一部剧可能用掉几十次离线配额'
  }
  return '按「订阅设置 → 离线下载」的默认策略；每月上限、配额保留这些闸对所有订阅都生效'
})

const washHint = computed(() => {
  if (form.value.priority?.mode === 'custom') return '资源优先级是自定义，和整理的洗版策略判得不一样，不能洗版；改成「跟随洗版策略」'
  const until = props.edit?.wash_until ? `（洗到 ${props.edit.wash_until.slice(5, 10)}）` : ''
  return `库里已有、没达标（没命中洗版策略第 1 条）的集，出现更好的 115 分享就转进来，交给整理替换；补齐后再洗一段时间${until}。要求整理的洗版策略是替换（replace），不用离线；RE0 / 癫影要花积分的只在标题明确比库里好时解锁，上限与预算同自动解锁`
})

const scopeHint = computed(() => {
  if (!tv.value) return ''
  const f = form.value
  const head = f.scope === 'all' ? '范围内所有' : f.scope === 'season' ? `第 ${f.season} 季所有` : `第 ${f.season} 季 E${f.ep_start} 起`
  return f.follow === 'missing'
    ? `${head}已播的集都要有：缺的会去找，新播的也会跟上。`
    : '只要订阅之后播出的集，之前缺的不补。'
})

async function submit() {
  if (!head.value) return
  submitting.value = true
  try {
    const body: SubForm = { ...form.value, tmdb_id: head.value.tmdb_id, media_type: head.value.media_type }
    if (props.edit) {
      const d = await subscribeApi.update(props.edit.id, body)
      message.success(d.message || '已保存')
      emit('saved', d.data)
    } else {
      const d = await subscribeApi.create(body)
      message.success(d.message || '已订阅')
      if (d.job_id) void queue.submitted(d.job_id)
      emit('saved', d.data)
    }
    show.value = false
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <HModal v-model:show="show" :title="edit ? '修改订阅' : '订阅'" width="620px" persistent>
    <div v-if="head" class="body">
      <div class="head">
        <PosterImage
          class="poster"
          :src="head.poster ? resourcesApi.tmdbImageUrl(head.poster, 'w154') : null"
          :alt="head.title"
        />
        <div class="head-text">
          <b>{{ head.title }}</b>
          <span v-if="head.year" class="muted">{{ head.year }}</span>
          <MediaTypeChip :type="head.media_type" />
          <p class="muted desc">
            {{
              tv
                ? '定时盘点缺哪几集，到各资源站找，115 分享只转缺的那几集，转存后自动整理入库。'
                : '入库前定时到各资源站找，找到就转存整理；默认等数字发行之后再找（院线期多是枪版）。'
            }}
          </p>
        </div>
      </div>

      <template v-if="tv">
        <FieldRow label="范围">
          <HSegmented
            v-model="form.scope"
            :options="[
              { label: '全剧', value: 'all' },
              { label: '某一季', value: 'season' },
              { label: '集段', value: 'range' },
            ]"
          />
        </FieldRow>
        <FieldRow v-if="form.scope !== 'all'" label="季">
          <HSelect v-model="form.season" :options="seasonOptions" :disabled="loadingSeasons" aria-label="季" />
        </FieldRow>
        <FieldRow v-if="form.scope === 'range'" label="集号" hint="结束集号留空 = 到这一季最后一集">
          <div class="pair">
            <HNumberInput v-model="form.ep_start" :min="1" aria-label="起始集号" />
            <span class="muted">到</span>
            <HNumberInput v-model="epEnd" :min="1" placeholder="最后" aria-label="结束集号" />
          </div>
        </FieldRow>
        <FieldRow v-if="form.scope === 'all'" label="特别篇" hint="TMDB 的第 0 季：花絮、SP、OVA，资源少、命名乱，默认不要">
          <HSwitch v-model="form.specials" aria-label="包含特别篇" />
        </FieldRow>
        <FieldRow label="模式" :hint="scopeHint">
          <HSegmented
            v-model="form.follow"
            :options="[
              { label: '补缺集', value: 'missing' },
              { label: '只追新集', value: 'new' },
            ]"
          />
        </FieldRow>
      </template>

      <FieldRow label="洗版" :hint="washHint">
        <HSwitch v-model="form.wash" aria-label="洗版" />
      </FieldRow>

      <FieldRow
        v-if="libsV2 ? stagingItems.length > 1 : stagingItems.length"
        label="转存到"
        :hint="libsV2 ? '放进哪个库的转存文件夹（只能是主账号的库）。跟随设置 = 订阅设置里的默认值，没设就用默认转存库' : '选一个库的转存入口：整理时直接落进那个库，不看分类规则。跟随设置 = 订阅设置里的默认值'"
      >
        <HSelect v-model="form.subdir" :options="subdirOptions" aria-label="转存到" />
      </FieldRow>

      <button type="button" class="adv-toggle" :aria-expanded="advanced" @click="advanced = !advanced">
        <ChevronDown :size="14" :class="{ open: advanced }" />高级：来源、资源优先级、中字与大小、关键词、离线
      </button>
      <div v-if="advanced" class="adv">
        <FieldRow label="来源" hint="不选 = 跟随「发现与订阅 → 设置」里开着的来源">
          <HMultiSelect v-model="form.sources" :options="sourceOptions" placeholder="全部开着的来源" />
        </FieldRow>
        <SubPriorityFields v-model:mode="prioMode" v-model:rules="prioRules" v-model:limit="form.rank_limit" inherit />
        <FieldRow
          label="中字与大小"
          hint="要不要中文字幕、单个视频多大。分享里每个视频逐个判，不符合的不转；磁力只能看标题，标题没写的不下"
        >
          <HSegmented
            :model-value="form.cond ? 'custom' : 'follow'"
            :options="[
              { label: '跟随设置', value: 'follow' },
              { label: '自定义', value: 'custom' },
            ]"
            @update:model-value="setCondMode"
          />
        </FieldRow>
        <SubCondFields v-if="form.cond" v-model="form.cond" part="extras" />
        <FieldRow label="包含" hint="资源标题要含其中一个（逗号分隔），留空不限">
          <HInput v-model="form.include" placeholder="如：4K, 杜比视界" />
        </FieldRow>
        <FieldRow label="排除" hint="资源标题含任何一个就不要；全局排除词（枪版等）在订阅设置里">
          <HInput v-model="form.exclude" placeholder="如：国语, 无字" />
        </FieldRow>
        <FieldRow
          label="交付方式"
          hint="转存：转进网盘、整理入库。分享 STRM：不转存、不占网盘空间，只建缺的那几集的 STRM；只用分享（磁力用不了），分享失效后那几集重新算缺、接着找"
        >
          <HSegmented
            v-model="form.delivery"
            :options="[
              { label: '跟随设置', value: '' },
              { label: '转存', value: 'transfer' },
              { label: '分享 STRM', value: 'strm' },
            ]"
          />
        </FieldRow>
        <FieldRow label="离线下载" :hint="offlineHint">
          <HSegmented
            v-model="form.offline_mode"
            :options="[
              { label: '跟随设置', value: '' },
              { label: '只下合集包', value: 'pack' },
              { label: '只转存分享', value: 'share_only' },
              { label: '不限', value: 'any' },
            ]"
          />
        </FieldRow>
      </div>

      <HAlert v-if="tv && form.scope === 'all' && form.follow === 'missing' && seasons.length > 3" status="warning">
        全剧补缺集会把 {{ seasons.length }} 季里缺的集都找一遍；只想追最新一季的话选「某一季」。
      </HAlert>
    </div>

    <template #footer>
      <HButton variant="tertiary" @click="show = false">取消</HButton>
      <HButton variant="primary" :loading="submitting" :disabled="!head" @click="submit">
        {{ edit ? '保存' : '订阅并开始检查' }}
      </HButton>
    </template>
  </HModal>
</template>

<style scoped>
.body {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.head {
  display: flex;
  gap: 12px;
  margin-bottom: 8px;
}
.poster {
  width: 56px;
  height: 84px;
  flex: none;
  border-radius: var(--r-sm);
}
.head-text {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 4px 8px;
  min-width: 0;
}
.head-text b {
  font-size: 15px;
  color: var(--foreground);
}
.desc {
  flex-basis: 100%;
  margin: 2px 0 0;
  line-height: 1.6;
}
.muted {
  font-size: 12.5px;
  color: var(--muted);
}
.pair {
  display: flex;
  align-items: center;
  gap: 8px;
}
.pair > :not(span) {
  width: 120px;
}
.adv-toggle {
  all: unset;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  margin: 8px 0 2px;
  font-size: 12.5px;
  color: var(--accent);
  cursor: pointer;
}
.adv-toggle svg {
  transition: transform 0.15s;
  transform: rotate(-90deg);
}
.adv-toggle svg.open {
  transform: none;
}
.adv {
  display: flex;
  flex-direction: column;
}
</style>
