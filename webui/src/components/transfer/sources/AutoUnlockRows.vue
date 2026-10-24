<script setup lang="ts">
import { onMounted, ref } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HNumberInput from '@/components/hero/HNumberInput.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import * as subscribeApi from '@/api/subscribe'
import { toastError, useFeedback } from '@/composables/useFeedback'

/**
 * 订阅的自动解锁积分（单条上限 / 每日预算），按站点放在 RE0 / 癫影的来源设置里。
 * 值存在订阅配置（setting "subscribe"）里，保存走只改这两项的接口：订阅设置在另一个页签整存整取，
 * 这里整份提交会互相覆盖。卡片自己的「保存配置」存的是账号，所以这里另有一个保存按钮。
 */
const props = defineProps<{ site: 're0' | 'dian' }>()
const { message } = useFeedback()
const max = ref(0)
const budget = ref(0)
const spent = ref(0)
const loaded = ref(false)
const saving = ref(false)

onMounted(async () => {
  try {
    const d = await subscribeApi.getConfig()
    if (props.site === 're0') {
      max.value = d.data.re0_unlock_max
      budget.value = d.data.re0_daily_budget
      spent.value = d.re0_spent_today
    } else {
      max.value = d.data.dian_unlock_max
      budget.value = d.data.dian_daily_budget
      spent.value = d.dian_spent_today ?? 0
    }
    loaded.value = true
  } catch (e) {
    toastError(e, '读取自动解锁设置失败')
  }
})

async function save() {
  saving.value = true
  try {
    const d = await subscribeApi.saveUnlock({ site: props.site, unlock_max: max.value, daily_budget: budget.value })
    message.success(d.message || '已保存')
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div v-if="loaded" class="unlock">
    <div class="head">
      <span class="title">订阅自动解锁</span>
      <span class="sub">订阅找到要花积分的资源时，满足下面两条才自动解锁；积分花了拿不回来</span>
    </div>
    <FieldRow
      label="单条上限"
      :hint="
        site === 'dian'
          ? '一条资源要的积分不超过它才自动解锁；0 = 不自动解锁（解锁过的、免费的照常用）。癫影的分享自带文件清单，解锁前按文件核对缺集；剧集的离线（磁力 / ed2k）分享订阅不用（电影照常）'
          : '一条资源要的积分不超过它才自动解锁；0 = 不自动解锁（解锁过的、免费的照常用）。解锁前先读文件预览核对缺集，读不到预览的不解锁'
      "
    >
      <div class="unit"><HNumberInput v-model="max" :min="0" aria-label="单条上限积分" /><span>积分</span></div>
    </FieldRow>
    <FieldRow label="每日预算" :hint="`每天自动解锁最多花多少；0 = 不限。今天已花 ${spent} 积分`">
      <div class="unit"><HNumberInput v-model="budget" :min="0" aria-label="每日预算积分" /><span>积分</span></div>
    </FieldRow>
    <p class="note">
      另外几道闸是固定的：要能确认包含缺的集、画质要命中资源优先级里至少一条规则、一个订阅一轮最多花钱解锁 1 条，
      解锁报积分不足或没登录时这一轮全部停下。超出上限的资源只通知一次，到「找资源」手动解锁。
    </p>
    <div class="actions">
      <HButton variant="secondary" :loading="saving" @click="save">保存自动解锁</HButton>
    </div>
  </div>
</template>

<style scoped>
.unlock {
  margin-top: 16px;
  padding-top: 14px;
  border-top: 1px solid var(--separator);
}
.head {
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin-bottom: 4px;
}
.title {
  font-size: 14px;
  font-weight: 600;
}
.sub,
.note {
  font-size: 12px;
  color: var(--muted);
  line-height: 1.6;
}
.note {
  margin: 6px 0 0;
}
.unit {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--muted);
}
.unit > :first-child {
  width: 120px;
}
.actions {
  display: flex;
  justify-content: flex-end;
  margin-top: 8px;
}
</style>
