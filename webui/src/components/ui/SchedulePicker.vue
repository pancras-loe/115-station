<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import HSelect, { type SelectOption } from '@/components/hero/HSelect.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import CronField from '@/components/ui/CronField.vue'
import { syncApi } from '@/api'
import {
  HOUR_STEPS,
  MINUTE_STEPS,
  WEEKDAYS,
  buildCron,
  describeSchedule,
  parseCron,
  type Schedule,
  type ScheduleMode,
} from '@/utils/schedule'

/**
 * 定时计划选择：v-model 仍是 5 段 cron，后端不用改。
 * 常见写法（每天 / 每周 / 每月 / 每隔几小时 / 每隔几分钟）用下拉与点选填，不懂 cron 也能配；
 * 认不出来的表达式落到「自定义」，原样保留，熟悉 cron 的照旧手写（CronField）。
 *
 * 带 `toggle` 时左边多一个定时开关（v-model:enabled），开关与计划放同一行；关着时计划变灰不能改。
 */
const props = defineProps<{ modelValue: string; disabled?: boolean; placeholder?: string; toggle?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [string] }>()
const enabled = defineModel<boolean>('enabled', { default: false })
const off = computed(() => props.disabled || (props.toggle && !enabled.value))

const s = ref<Schedule>(parseCron(props.modelValue, props.placeholder))

// 外面换了值（读完配置）才重新解析，而且解析出来的这份不回写：
// 空串（自动整理「留空 = 不定时」）解析成默认计划，回写就等于悄悄打开了定时；
// `1-5` 解析后写回是 `1,2,3,4,5`，没动过也会被标成「有改动」。只有用户在这里改了才往外发
let fromOutside = false
watch(
  () => props.modelValue,
  (v) => {
    if ((v ?? '').trim() !== buildCron(s.value)) {
      fromOutside = true
      s.value = parseCron(v, props.placeholder)
    }
  },
)
watch(
  s,
  (v) => {
    if (fromOutside) {
      fromOutside = false
      return
    }
    const cron = buildCron(v)
    if (cron !== (props.modelValue ?? '').trim()) emit('update:modelValue', cron)
  },
  { deep: true },
)

const MODES: SelectOption<ScheduleMode>[] = [
  { label: '每天', value: 'daily' },
  { label: '每周', value: 'weekly' },
  { label: '每月', value: 'monthly' },
  { label: '每隔几小时', value: 'hours' },
  { label: '每隔几分钟', value: 'minutes' },
  { label: '自定义（cron）', value: 'custom' },
]

const pad = (n: number) => String(n).padStart(2, '0')

const mode = computed({
  get: () => s.value.mode,
  set: (m: ScheduleMode) => {
    const cur = s.value
    if (m === 'custom') {
      cur.cron = buildCron(cur) // 带着当前的计划进自定义，在它上面改
    } else if (m === 'hours' && !HOUR_STEPS.includes(cur.every)) {
      cur.every = 2
    } else if (m === 'minutes' && !MINUTE_STEPS.includes(cur.every)) {
      cur.every = 30
    }
    cur.mode = m
  },
})

const withCurrent = (list: number[], cur: number) => (list.includes(cur) ? list : [...list, cur].sort((a, b) => a - b))

const hourOpts = computed<SelectOption<number>[]>(() =>
  Array.from({ length: 24 }, (_, i) => ({ label: `${pad(i)} 时`, value: i })),
)
// 分钟按 5 分钟一档；配置里原本是别的分钟数（03:07）也列出来，免得一打开就被改掉
const minuteOpts = computed<SelectOption<number>[]>(() =>
  withCurrent(
    Array.from({ length: 12 }, (_, i) => i * 5),
    s.value.minute,
  ).map((m) => ({ label: `${pad(m)} 分`, value: m })),
)
const fromOpts = computed<SelectOption<number>[]>(() =>
  Array.from({ length: 24 }, (_, i) => ({ label: `${pad(i)}:00`, value: i })),
)
const toOpts = computed<SelectOption<number>[]>(() =>
  Array.from({ length: 24 }, (_, i) => ({ label: `${pad(i)}:59`, value: i, disabled: i < s.value.fromHour })),
)
watch(
  () => s.value.fromHour,
  (f) => {
    if (s.value.toHour < f) s.value.toHour = f
  },
)
const dayOpts = computed<SelectOption<number>[]>(() =>
  Array.from({ length: 31 }, (_, i) => ({ label: `${i + 1} 日`, value: i + 1 })),
)
const hourStepOpts = computed<SelectOption<number>[]>(() =>
  withCurrent(HOUR_STEPS, s.value.every).map((n) => ({ label: n === 1 ? '每小时' : `每 ${n} 小时`, value: n })),
)
const minuteStepOpts = computed<SelectOption<number>[]>(() =>
  withCurrent(MINUTE_STEPS, s.value.every).map((n) => ({ label: `每 ${n} 分钟`, value: n })),
)

function toggleDay(d: number) {
  const set = new Set(s.value.weekdays)
  if (set.has(d)) {
    if (set.size === 1) return // 至少留一天
    set.delete(d)
  } else {
    set.add(d)
  }
  s.value.weekdays = [...set].sort((a, b) => a - b)
}

const summary = computed(() => describeSchedule(s.value))

// 接下来几次（自定义模式由 CronField 自己预览）
const next = ref<string[]>([])
const err = ref('')
let timer: number | undefined
watch(
  () => [s.value.mode, buildCron(s.value)] as const,
  ([m, cron]) => {
    clearTimeout(timer)
    if (m === 'custom') return
    timer = window.setTimeout(async () => {
      try {
        const d = await syncApi.cronPreview(cron)
        next.value = d.next ?? []
        err.value = ''
      } catch (e) {
        next.value = []
        err.value = e instanceof Error ? e.message : '计划无效'
      }
    }, 300)
  },
  { immediate: true },
)
</script>

<template>
  <div class="sched-outer">
    <HSwitch v-if="toggle" v-model="enabled" :disabled="disabled" aria-label="定时运行" class="sched-switch" />
    <div class="sched" :class="{ off }">
      <div class="sched-line">
        <div class="w-mode">
          <HSelect v-model="mode" :options="MODES" :disabled="off" aria-label="频率" />
        </div>
        <template v-if="mode === 'monthly'">
          <div class="w-num"><HSelect v-model="s.day" :options="dayOpts" :disabled="off" aria-label="几号" /></div>
        </template>
        <template v-if="mode === 'hours'">
          <div class="w-step"><HSelect v-model="s.every" :options="hourStepOpts" :disabled="off" aria-label="间隔" /></div>
          <span class="word">第</span>
          <div class="w-num"><HSelect v-model="s.minute" :options="minuteOpts" :disabled="off" aria-label="分" /></div>
        </template>
        <template v-if="mode === 'minutes'">
          <div class="w-step"><HSelect v-model="s.every" :options="minuteStepOpts" :disabled="off" aria-label="间隔" /></div>
          <span class="word">时段</span>
          <div class="w-num"><HSelect v-model="s.fromHour" :options="fromOpts" :disabled="off" aria-label="从几点" /></div>
          <span class="word">–</span>
          <div class="w-num"><HSelect v-model="s.toHour" :options="toOpts" :disabled="off" aria-label="到几点" /></div>
        </template>
        <template v-if="mode === 'daily' || mode === 'weekly' || mode === 'monthly'">
          <div class="w-num"><HSelect v-model="s.hour" :options="hourOpts" :disabled="off" aria-label="时" /></div>
          <div class="w-num"><HSelect v-model="s.minute" :options="minuteOpts" :disabled="off" aria-label="分" /></div>
        </template>
      </div>

      <div v-if="mode === 'weekly'" class="days" role="group" aria-label="周几">
        <button
          v-for="d in WEEKDAYS"
          :key="d.v"
          type="button"
          class="day"
          :class="{ on: s.weekdays.includes(d.v) }"
          :aria-pressed="s.weekdays.includes(d.v)"
          :disabled="off"
          @click="toggleDay(d.v)"
        >
          {{ d.label }}
        </button>
      </div>

      <div v-if="mode === 'custom'" class="custom">
        <CronField v-model="s.cron" :placeholder="placeholder" />
      </div>
      <div v-else class="preview">
        <span v-if="err" class="err">{{ err }}</span>
        <template v-else>
          <span class="sum">{{ summary }}</span>
          <template v-if="next.length">
            <span class="sep">·</span>接下来：<span v-for="(t, i) in next.slice(0, 3)" :key="i" class="t">{{ t }}</span>
          </template>
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped>
.sched-outer {
  display: flex;
  align-items: flex-start;
  gap: 14px;
}
/* 开关 20px 高，按下拉的 36px 行高居中 */
.sched-switch {
  min-height: 36px;
  justify-content: center;
}
.sched {
  flex: 1;
  min-width: 0;
}
.sched.off .preview {
  opacity: 0.6;
}
.sched-line {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}
.w-mode {
  width: 150px;
}
.w-step {
  width: 120px;
}
.w-num {
  width: 92px;
}
.word {
  font-size: 13px;
  color: var(--muted);
}
.days {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 8px;
}
.day {
  width: 34px;
  height: 30px;
  border-radius: var(--r-sm);
  border: 1px solid var(--border);
  background: transparent;
  color: var(--foreground);
  font-size: 13px;
  cursor: pointer;
  transition:
    background 0.15s,
    border-color 0.15s;
}
.day.on {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--accent-foreground);
}
.day:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}
.custom {
  margin-top: 8px;
}
.preview {
  margin-top: 6px;
  font-size: 12px;
  line-height: 1.7;
  color: var(--muted);
}
.sum {
  color: var(--foreground);
}
.sep {
  margin: 0 6px;
}
.t {
  font-variant-numeric: tabular-nums;
  color: var(--foreground);
}
.t + .t::before {
  content: '·';
  margin: 0 6px;
  color: var(--muted);
}
.err {
  color: var(--danger);
}
@media (max-width: 720px) {
  .w-mode {
    width: 100%;
  }
}
</style>
