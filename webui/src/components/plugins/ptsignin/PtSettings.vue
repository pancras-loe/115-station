<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import HNumberInput from '@/components/hero/HNumberInput.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSelect from '@/components/hero/HSelect.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import { pluginsApi } from '@/api'
import type { PtConfig } from '@/api/plugins'
import { toastError, useFeedback } from '@/composables/useFeedback'

/** 设置：整份存取（setting ptsignin 只有这一页改） */
const emit = defineEmits<{ saved: [] }>()
const { message } = useFeedback()

const cfg = ref<PtConfig | null>(null)
const saving = ref(false)

const MODE_OPTS = [
  { label: '每天随机一个时刻', value: 'window' as const },
  { label: '固定时间（cron）', value: 'cron' as const },
]
const NOTIFY_OPTS = [
  { label: '每轮推一条汇总', value: 'always' as const },
  { label: '只在有失败时推', value: 'fail_only' as const },
  { label: '不推', value: 'off' as const },
]

onMounted(async () => {
  try {
    cfg.value = await pluginsApi.ptConfig()
  } catch (e) {
    toastError(e, '读设置失败')
  }
})

const timeHint = computed(() => {
  const c = cfg.value
  if (!c) return ''
  if (c.mode === 'cron') return '5 段式：分 时 日 月 周。固定时间会和别人撞在同一分钟，推荐随机。'
  const today = c.today_at ? `今天挑好的是 ${c.today_at}。` : ''
  return `${today}每天挑一次、存下来，重启不变；避免所有人整点一起打站点。`
})

async function save() {
  const c = cfg.value
  if (!c) return
  saving.value = true
  try {
    await pluginsApi.ptSaveConfig({
      enabled: c.enabled,
      mode: c.mode,
      window_from: c.window_from.trim(),
      window_to: c.window_to.trim(),
      cron: c.cron.trim(),
      gap_min: c.gap_min,
      gap_max: c.gap_max,
      retry: c.retry,
      notify: c.notify,
    })
    message.success('保存成功')
    cfg.value = await pluginsApi.ptConfig()
    emit('saved')
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div v-if="cfg" class="set">
    <FieldRow label="定时签到">
      <HSwitch v-model="cfg.enabled" aria-label="定时签到" />
    </FieldRow>
    <FieldRow label="执行时间" wide :hint="timeHint">
      <div class="col">
        <HSegmented v-model="cfg.mode" :options="MODE_OPTS" size="sm" />
        <div v-if="cfg.mode === 'window'" class="inline">
          <HInput v-model="cfg.window_from" mono class="hm" placeholder="08:00" />
          <span>到</span>
          <HInput v-model="cfg.window_to" mono class="hm" placeholder="10:00" />
        </div>
        <HInput v-else v-model="cfg.cron" mono class="cron" placeholder="30 8 * * *" />
      </div>
    </FieldRow>
    <FieldRow label="站与站之间" hint="一个站跑完再跑下一个，不并发。今天已经签成（或确认登录）的站不再请求。">
      <div class="inline">
        <span>随机隔</span>
        <HNumberInput v-model="cfg.gap_min" :min="1" :max="60" class="num" />
        <span>到</span>
        <HNumberInput v-model="cfg.gap_max" :min="1" :max="120" class="num" />
        <span>秒</span>
      </div>
    </FieldRow>
    <FieldRow label="失败重试" hint="只重试网络错误、站点 5xx：30–60 分钟后再试一次。Cookie 失效、被 Cloudflare 拦下不重试，再试也是一样的结果。">
      <HSwitch v-model="cfg.retry" aria-label="失败重试" />
    </FieldRow>
    <FieldRow label="通知" hint="走「消息通知」里已经配好的通道。">
      <HSelect v-model="cfg.notify" :options="NOTIFY_OPTS" class="sel" />
    </FieldRow>
    <FieldRow label="机器人">
      <span class="txt">TG <code>/ptsignin</code> · 企微「PT签到」：立即跑一轮，跑完回复汇总。</span>
    </FieldRow>
    <FieldRow label="影巢">
      <span class="txt">影巢就是 RE0，签到在「发现与订阅 → 资源来源 → RE0」里，这里不重复做。</span>
    </FieldRow>
    <HAlert status="warning" class="risk">
      先看站规：有的站禁止脚本操作，被发现可能封号。本站每个站每天只发 1–2 个请求，风险需要你自己判断。
    </HAlert>
    <div class="actions">
      <HButton variant="primary" :loading="saving" @click="save">保存设置</HButton>
    </div>
  </div>
</template>

<style scoped>
.col {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.inline {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--foreground);
}
.hm {
  width: 90px;
}
.cron {
  max-width: 200px;
}
.num {
  width: 140px;
}
.sel {
  max-width: 240px;
}
.txt {
  font-size: 13px;
  color: var(--foreground);
}
.risk {
  margin-top: 12px;
}
.actions {
  display: flex;
  justify-content: flex-end;
  margin-top: 14px;
}
</style>
