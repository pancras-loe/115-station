<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import HMultiSelect from '@/components/hero/HMultiSelect.vue'
import HNumberInput from '@/components/hero/HNumberInput.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSelect from '@/components/hero/HSelect.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import StatusStrip from '@/components/ui/StatusStrip.vue'
import type { StripItem } from '@/components/ui/StatusStrip.vue'
import SubCondFields from '@/components/subscribe/SubCondFields.vue'
import SubPriorityFields from '@/components/subscribe/SubPriorityFields.vue'
import * as subscribeApi from '@/api/subscribe'
import type { SubscribeConfig } from '@/api/subscribe'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { stagingSubChoices, useStagingSubs } from '@/composables/stagingSubs'

/**
 * 订阅的全局设置（setting "subscribe"，整存整取）。
 * 在发现与订阅「订阅设置」页签里按小节一次显示一张卡片（section），整个组件保持挂载：
 * 小节之间切换不丢没保存的改动，底下的「保存」存的是全部小节。
 * 自动解锁的积分（re0_* / dian_*）显示在 RE0 / 癫影的来源设置里、走单独的接口改；
 * 这里照原样带回去，两个页签不同时挂载，进来时读到的就是最新的。
 *
 * 2026-10-24 改版（维护者嫌项太多、一条条看不下去，参考 MoviePilot 订阅设定）：按用途分节，
 * 顶上一行摘要；检查间隔给三档预设；说明只留一句，长的进问号；防风控的几个上限单放「节流与限额」。
 */
export type SubSettingsSec = 'check' | 'cond' | 'wash' | 'offline' | 'notify' | 'limits'
defineProps<{ section: SubSettingsSec }>()
const emit = defineEmits<{ go: [sec: SubSettingsSec] }>()
const { message } = useFeedback()
const cfg = ref<SubscribeConfig | null>(null)
const saving = ref(false)
const notify = ref<string[]>([])
const { items: stagingItems, v2: libsV2 } = useStagingSubs()
/** 默认转存到：老口径是转存目录本身或某个绑定子目录；V2 是主号的某个库，空 = 默认转存库 */
const subdirOptions = computed(() =>
  libsV2.value
    ? [{ label: '默认转存库', value: '' }, ...stagingSubChoices({ mainOnly: true })]
    : [{ label: '转存目录（按分类选库）', value: '' }, ...stagingSubChoices()],
)
const subdir = computed({
  get: () => cfg.value?.subdir ?? '',
  set: (v: string) => {
    if (cfg.value) cfg.value.subdir = v
  },
})

// 改成自定义、还没有规则时给两条常见的起步（4K、1080p），用户在上面改
function setPrioMode(v: subscribeApi.PrioMode | '') {
  if (!cfg.value) return
  cfg.value.priority.mode = v || 'follow'
  if (v === 'custom' && !cfg.value.priority.rules.length) {
    cfg.value.priority.rules = [{ resource_pix: '2160p,4k' }, { resource_pix: '1080p,1080i' }]
  }
}

// 检查节奏：新集间隔（分钟）/ 老集间隔（小时）两个数配成三档预设，对不上任何一档就是自定义。
// 「标准」= 后端默认值（defaultSubscribeCfg），改一边要改另一边
const PACES = { fast: [30, 6], std: [60, 12], slow: [120, 24] } as const
type Pace = keyof typeof PACES | 'custom'
const paceCustom = ref(false)
const pace = computed<Pace>({
  get: () => {
    const c = cfg.value
    if (!c || paceCustom.value) return 'custom'
    const hit = (Object.keys(PACES) as (keyof typeof PACES)[]).find(
      (k) => PACES[k][0] === c.fresh_interval_min && PACES[k][1] === c.gap_interval_hours,
    )
    return hit ?? 'custom'
  },
  set: (v) => {
    paceCustom.value = v === 'custom'
    if (v !== 'custom' && cfg.value) [cfg.value.fresh_interval_min, cfg.value.gap_interval_hours] = PACES[v]
  },
})
function fmtMin(m: number) {
  return m >= 60 && m % 60 === 0 ? `${m / 60} 小时` : `${m} 分钟`
}

const OFFLINE_TEXT = { pack: '磁力只下合集包', share_only: '不用离线', any: '离线不限' }
const OFFLINE_HINT = { pack: '标题写着单集的磁力不下；电影照常下', share_only: '不提交离线任务', any: '单集磁力也下，每集扣一次配额' }

// 顶上一行摘要：每节最要紧的一两个值，点「改」跳到那一节（只看不做，同 Strm 管理的状态条）
const strip = computed<StripItem[]>(() => {
  const c = cfg.value
  if (!c) return []
  const prio = { basic: '基础', follow: '跟随洗版策略', custom: '自定义' }[c.priority.mode] ?? '跟随洗版策略'
  return [
    {
      key: 'check',
      title: '检查',
      tone: c.enabled ? 'ok' : 'off',
      text: c.enabled ? `新集 ${fmtMin(c.fresh_interval_min)} · 老集 ${c.gap_interval_hours} 小时` : '定时检查已关',
      action: '改',
    },
    {
      key: 'cond',
      title: '筛选',
      tone: 'ok',
      text: [prio, c.priority.limit ? `前 ${c.priority.limit} 条` : '', c.cond.zh ? '要中字' : ''].filter(Boolean).join(' · '),
      action: '改',
    },
    {
      key: 'wash',
      title: '洗版',
      tone: c.wash_default ? 'ok' : 'off',
      text: c.wash_default ? `默认开 · 补齐后 ${c.wash_days} 天` : '新订阅默认关',
      action: '改',
    },
    {
      key: 'offline',
      title: '离线',
      tone: 'ok',
      text: c.offline_mode === 'share_only' ? OFFLINE_TEXT.share_only : `${OFFLINE_TEXT[c.offline_mode]} · 每月 ${c.offline_monthly || '不限'}`,
      action: '改',
    },
    {
      key: 'notify',
      title: '通知',
      tone: notify.value.length ? 'ok' : 'off',
      text: notify.value.length ? `推 ${notify.value.length} 类` : '都不推',
      action: '改',
    },
  ]
})

async function load() {
  try {
    const d = await subscribeApi.getConfig()
    cfg.value = {
      ...d.data,
      cond: { ...subscribeApi.blankCond(), ...d.data.cond },
      priority: { ...{ mode: 'follow' as const, limit: 0 }, ...d.data.priority, rules: d.data.priority?.rules ?? [] },
    }
    notify.value = (d.data.notify || '').split(',').filter(Boolean)
  } catch (e) {
    toastError(e, '读取订阅设置失败')
  }
}
onMounted(load)

async function save() {
  if (!cfg.value) return
  saving.value = true
  try {
    const d = await subscribeApi.saveConfig({ ...cfg.value, notify: notify.value.join(',') })
    cfg.value = d.data
    message.success(d.message || '已保存')
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div v-if="cfg" class="stack">
    <StatusStrip :items="strip" @act="(k) => emit('go', k as SubSettingsSec)" />

    <SectionCard v-show="section === 'check'" title="检查计划" hint="多久查一次、播出后多久开始找。查一轮不发 115 请求，间隔只影响资源站">
      <FieldRow label="定时检查" :hint="cfg.enabled ? undefined : '关了也能在订阅详情里点「立即搜索」'">
        <HSwitch v-model="cfg.enabled" aria-label="定时检查" />
      </FieldRow>
      <FieldRow
        label="检查节奏"
        tip="缺的集里有播出 3 天内的按「新集」间隔查，新集的资源多在这几天冒出来；只缺老集时按「老集」间隔，固定不放慢，约两周还没找到会标成「长期找不到」并通知一次。"
        :hint="pace === 'custom' ? undefined : `有新集时每 ${fmtMin(cfg.fresh_interval_min)}，只缺老集时每 ${cfg.gap_interval_hours} 小时`"
      >
        <HSegmented
          v-model="pace"
          :options="[
            { label: '积极', value: 'fast' },
            { label: '标准', value: 'std' },
            { label: '省着用', value: 'slow' },
            { label: '自定义', value: 'custom' },
          ]"
        />
        <div v-if="pace === 'custom'" class="sub">
          <div class="unit">
            <HNumberInput v-model="cfg.fresh_interval_min" :min="10" :max="720" aria-label="新集间隔分钟" /><span>分钟：有刚播出的新集时</span>
          </div>
          <div class="unit">
            <HNumberInput v-model="cfg.gap_interval_hours" :min="1" :max="72" aria-label="老集间隔小时" /><span>小时：只缺老集时</span>
          </div>
        </div>
      </FieldRow>
      <FieldRow label="播出后开始找" tip="TMDB 只有播出日期没有时刻，网盘资源一般几小时内出现，太早去找只会白白多查几轮。">
        <div class="unit"><HNumberInput v-model="cfg.air_delay_hours" :min="0" :max="72" aria-label="小时" /><span>小时后</span></div>
      </FieldRow>
      <FieldRow label="电影开始找">
        <HSegmented
          v-model="cfg.movie_wait"
          :options="[
            { label: '数字发行后', value: 'digital' },
            { label: '院线 45 天后', value: 'theatrical_plus' },
            { label: '立刻', value: 'now' },
          ]"
        />
      </FieldRow>
    </SectionCard>

    <SectionCard
      v-show="section === 'cond'"
      title="资源筛选"
      hint="挑资源时谁更好、哪些不要，和整理时洗版同一套比法。单个订阅可以在「修改订阅 → 高级」里另选"
    >
      <SubPriorityFields
        :mode="cfg.priority.mode"
        @update:mode="setPrioMode"
        v-model:rules="cfg.priority.rules"
        v-model:limit="cfg.priority.limit"
      />
      <SubCondFields v-model="cfg.cond" part="extras" />
      <FieldRow label="排除词" hint="标题含任何一个就不要，逗号分隔" tip="对所有订阅生效，单个订阅还能另加。">
        <HInput v-model="cfg.exclude_default" />
      </FieldRow>
    </SectionCard>

    <SectionCard v-show="section === 'wash'" title="洗版" hint="库里已有、没达标的集，出现更好的 115 分享就换进来">
      <FieldRow label="新订阅默认洗版" tip="新建订阅时「洗版」开关的默认值，机器人订阅也用它。">
        <HSwitch v-model="cfg.wash_default" aria-label="新订阅默认洗版" />
      </FieldRow>
      <FieldRow label="补齐后再洗" hint="补集期间一直洗，补齐后再洗这么多天" tip="期间出现比库里更好的就换，全部达标或到期就结束。">
        <div class="unit"><HNumberInput v-model="cfg.wash_days" :min="1" :max="365" aria-label="天" /><span>天</span></div>
      </FieldRow>
      <p v-if="cfg.priority.mode === 'custom'" class="warn">
        「资源筛选」里的优先级是自定义时不洗版：订阅和整理判得不一样，换进来的会被移进「已存在」。
      </p>
    </SectionCard>

    <SectionCard v-show="section === 'offline'" title="转存与离线" hint="转进哪个库、磁力怎么用。115 分享总是排在磁力前面">
      <FieldRow
        v-if="libsV2 ? stagingItems.length > 1 : stagingItems.length"
        label="默认转存到"
        tip="订阅的内容整理时直接落进这个库，不看分类规则。单个订阅可以另选。"
      >
        <HSelect v-model="subdir" :options="subdirOptions" aria-label="默认转存到" />
      </FieldRow>
      <FieldRow
        label="交付方式"
        tip="找到 115 分享后：转存进网盘、整理入库；或者不转存、建成分享 STRM（要先在扩展功能里打开分享 STRM）。分享 STRM 只建缺的那几集、只用分享（磁力用不了），分享失效后那几集重新算缺。单个订阅可以另选。"
      >
        <HSegmented
          v-model="cfg.delivery"
          :options="[
            { label: '转存', value: 'transfer' },
            { label: '分享 STRM', value: 'strm' },
          ]"
        />
      </FieldRow>
      <FieldRow
        label="离线策略"
        :hint="OFFLINE_HINT[cfg.offline_mode]"
        tip="磁力下载前看不到里面有哪些文件，挑不了集；115 的离线配额按任务数扣，一集一个磁力的话追一部剧就是几十次。单个订阅可以在「修改订阅 → 高级」里另设。"
      >
        <HSegmented
          v-model="cfg.offline_mode"
          :options="[
            { label: '只下合集包', value: 'pack' },
            { label: '只转存分享', value: 'share_only' },
            { label: '不限', value: 'any' },
          ]"
        />
      </FieldRow>
      <template v-if="cfg.offline_mode !== 'share_only'">
        <FieldRow label="新集先等分享" hint="这段时间里只找 115 分享，0 = 不等">
          <div class="unit"><HNumberInput v-model="cfg.offline_wait_hours" :min="0" :max="168" aria-label="小时" /><span>小时</span></div>
        </FieldRow>
        <FieldRow
          label="离线配额"
          tip="每个订阅每轮最多一个离线任务。「剩余低于」是提交前查一次 115 的剩余配额，留给你手动离线用。两项填 0 都是不限 / 不查。"
        >
          <div class="unit wrap">
            <span>每月最多</span>
            <HNumberInput v-model="cfg.offline_monthly" :min="0" aria-label="每月最多任务数" />
            <span>个，115 剩余低于</span>
            <HNumberInput v-model="cfg.offline_reserve" :min="0" aria-label="配额保留次数" />
            <span>次就停</span>
          </div>
        </FieldRow>
      </template>
    </SectionCard>

    <SectionCard v-show="section === 'notify'" title="通知" hint="花了积分、资源内容不对（整理认成了别的片）这两种总是推">
      <FieldRow label="推送">
        <HMultiSelect
          v-model="notify"
          :options="[
            { label: '新订阅', value: 'created' },
            { label: '提交了资源', value: 'submit' },
            { label: '补上了缺集', value: 'ingested' },
            { label: '订阅完成', value: 'done' },
            { label: '长期找不到', value: 'stalled' },
            { label: '有资源要积分（没自动解锁）', value: 'paid' },
          ]"
          placeholder="都不推"
        />
      </FieldRow>
    </SectionCard>

    <SectionCard v-show="section === 'limits'" title="节流与限额" hint="防风控、防一次搜出几百条的保险，平时不用改">
      <FieldRow label="一轮最多检查" tip="到期的订阅超过这个数时，剩下的下一分钟接着查。">
        <div class="unit"><HNumberInput v-model="cfg.max_subs_per_round" :min="1" :max="50" aria-label="订阅数" /><span>个订阅</span></div>
      </FieldRow>
      <FieldRow label="每个订阅一轮最多补" tip="按集数算：一条分享常常只有一集，补到这么多集就停，剩下的下一轮（约 30 分钟后）接着找。">
        <div class="unit"><HNumberInput v-model="cfg.max_eps_per_sub" :min="1" :max="500" aria-label="集数" /><span>集</span></div>
      </FieldRow>
      <FieldRow label="一轮最多试" tip="搜到的资源从头试到尾，没用上的接着试下一条；这个上限只防一次搜出几百条，平时碰不到。">
        <div class="unit"><HNumberInput v-model="cfg.max_res_per_sub" :min="1" :max="200" aria-label="资源数" /><span>条资源</span></div>
      </FieldRow>
      <FieldRow label="资源之间歇" hint="0 = 不歇" tip="试完一条再试下一条前等一会儿（±30% 随机），叠在 115 请求节流之上，免得连着列分享、转存被风控。">
        <div class="unit"><HNumberInput v-model="cfg.try_cooldown_sec" :min="0" :max="120" aria-label="秒" /><span>秒</span></div>
      </FieldRow>
      <FieldRow label="列分享最多进" tip="按集挑选要把分享逐个目录列一遍，每个目录一次 115 请求（走节流）。">
        <div class="unit"><HNumberInput v-model="cfg.max_snap_dirs" :min="1" :max="200" aria-label="目录数" /><span>个目录</span></div>
      </FieldRow>
      <FieldRow
        label="先看几条分享的文件"
        hint="0 = 不预看，只按标题排"
        tip="很多整季包标题只写「剧名 4K」，看不出有几集。先列排在前面的几条 115 分享，按文件实际能补几集重排，整季包一次转完；转存时直接用列出的结果，不会再列一次。"
      >
        <div class="unit"><HNumberInput v-model="cfg.share_preview_max" :min="0" :max="30" aria-label="条数" /><span>条分享</span></div>
      </FieldRow>
    </SectionCard>

    <div class="save">
      <span class="save-hint">各小节一起保存</span>
      <HButton variant="primary" :loading="saving" @click="save">保存订阅设置</HButton>
    </div>
  </div>
</template>

<style scoped>
.stack {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.unit {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--muted);
}
.unit > .number-field {
  width: 120px;
}
.unit.wrap {
  flex-wrap: wrap;
}
.unit.wrap > .number-field {
  width: 100px;
}
.sub {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-top: 8px;
  padding: 10px 12px;
  border-radius: var(--r-sm);
  background: var(--surface-secondary);
}
.warn {
  margin: 6px 0 0;
  font-size: 12px;
  color: var(--warning);
}
.save {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 12px;
}
.save-hint {
  font-size: 12px;
  color: var(--muted);
}
</style>
