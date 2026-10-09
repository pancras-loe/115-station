<script setup lang="ts">
import { onMounted, ref } from 'vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import HMultiSelect from '@/components/hero/HMultiSelect.vue'
import HNumberInput from '@/components/hero/HNumberInput.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import SubCondFields from '@/components/subscribe/SubCondFields.vue'
import * as subscribeApi from '@/api/subscribe'
import type { SubscribeConfig } from '@/api/subscribe'
import { toastError, useFeedback } from '@/composables/useFeedback'

/**
 * 订阅的全局设置（setting "subscribe"，整存整取）。
 * 在影视转存「设置」页签里按小节一次显示一张卡片（section），整个组件保持挂载：
 * 小节之间切换不丢没保存的改动，底下的「保存」存的是全部小节。
 */
defineProps<{ section: 'check' | 'cond' | 'offline' | 'unlock' | 'notify' }>()
const { message } = useFeedback()
const cfg = ref<SubscribeConfig | null>(null)
const spent = ref(0)
const saving = ref(false)
const notify = ref<string[]>([])

async function load() {
  try {
    const d = await subscribeApi.getConfig()
    cfg.value = { ...d.data, cond: { ...subscribeApi.blankCond(), ...d.data.cond } }
    spent.value = d.re0_spent_today
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
    <SectionCard v-show="section === 'check'" title="检查">
      <FieldRow label="定时检查" hint="关掉后不再自动检查，订阅详情里的「立即搜索」仍可用">
        <HSwitch v-model="cfg.enabled" aria-label="定时检查" />
      </FieldRow>
      <FieldRow label="播出后多久开始找" hint="TMDB 只有播出日期没有时刻，网盘资源一般几小时内出现；太早去找只会白白多查几轮">
        <div class="unit"><HNumberInput v-model="cfg.air_delay_hours" :min="0" :max="72" aria-label="小时" /><span>小时</span></div>
      </FieldRow>
      <FieldRow label="电影什么时候开始找">
        <HSegmented
          v-model="cfg.movie_wait"
          :options="[
            { label: '数字发行后', value: 'digital' },
            { label: '院线 45 天后', value: 'theatrical_plus' },
            { label: '立刻', value: 'now' },
          ]"
        />
      </FieldRow>
      <FieldRow label="新集没找到时隔" hint="缺的集里有刚播出的（播出 3 天内）时用这个间隔，新集的资源多在这几天冒出来。查一轮不发 115 请求，只有出现没试过的资源才会去列分享">
        <div class="unit"><HNumberInput v-model="cfg.fresh_interval_min" :min="10" :max="720" aria-label="分钟" /><span>分钟</span></div>
      </FieldRow>
      <FieldRow label="补老集没找到时隔" hint="只缺播出 3 天以上的集时用这个间隔，固定不放慢；约两周还没找到会标成「长期找不到」并通知一次">
        <div class="unit"><HNumberInput v-model="cfg.gap_interval_hours" :min="1" :max="72" aria-label="小时" /><span>小时</span></div>
      </FieldRow>
      <FieldRow label="一轮最多检查" hint="到期的订阅超过这个数时，剩下的下一分钟接着查">
        <div class="unit"><HNumberInput v-model="cfg.max_subs_per_round" :min="1" :max="50" aria-label="订阅数" /><span>个订阅</span></div>
      </FieldRow>
      <FieldRow label="每个订阅一轮最多补" hint="按集数算：一条分享常常只有一集，补到这么多集就停，剩下的下一轮（约 30 分钟后）接着找">
        <div class="unit"><HNumberInput v-model="cfg.max_eps_per_sub" :min="1" :max="500" aria-label="集数" /><span>集</span></div>
      </FieldRow>
      <FieldRow label="一轮最多试" hint="搜到的资源从头试到尾，没用上的接着试下一条；这个上限只防一次搜出几百条，平时碰不到">
        <div class="unit"><HNumberInput v-model="cfg.max_res_per_sub" :min="1" :max="200" aria-label="资源数" /><span>条资源</span></div>
      </FieldRow>
      <FieldRow label="资源之间歇" hint="试完一条再试下一条前等一会儿（±30% 随机），叠在 115 请求节流之上，免得连着列分享、转存被风控；0 = 不歇">
        <div class="unit"><HNumberInput v-model="cfg.try_cooldown_sec" :min="0" :max="120" aria-label="秒" /><span>秒</span></div>
      </FieldRow>
      <FieldRow label="列分享最多进" hint="按集挑选要把分享逐个目录列一遍，每个目录一次 115 请求（走节流）">
        <div class="unit"><HNumberInput v-model="cfg.max_snap_dirs" :min="1" :max="200" aria-label="目录数" /><span>个目录</span></div>
      </FieldRow>
      <FieldRow label="先看几条分享的文件" hint="很多整季包标题只写「剧名 4K」，看不出有几集。先列排在前面的几条 115 分享，按文件实际能补几集重排，整季包一次转完，不用一条条转单集；转存时直接用列出的结果，不会再列一次。0 = 不预看，只按标题排">
        <div class="unit"><HNumberInput v-model="cfg.share_preview_max" :min="0" :max="30" aria-label="条数" /><span>条分享</span></div>
      </FieldRow>
      <FieldRow label="排除词" hint="资源标题含任何一个就不要，所有订阅都生效（逗号分隔）；单个订阅还能另加">
        <HInput v-model="cfg.exclude_default" />
      </FieldRow>
    </SectionCard>

    <SectionCard
      v-show="section === 'cond'"
      title="资源条件"
      hint="所有订阅默认用这一组，单个订阅可以在「修改订阅 → 高级」里自定义。写法同洗版规则：逗号分隔命中任一，「!」开头排除；留空不限"
    >
      <SubCondFields v-model="cfg.cond" />
      <HAlert status="accent" class="note">
        资源标题写明不符合的直接跳过。分享里每个视频再逐个判：文件名没写的看资源标题，两边都没写算不符合、不转。
        磁力只能看标题、又要扣离线配额，标题没写的不下；RE0 要花积分的资源同样要标题写明符合才解锁。
      </HAlert>
    </SectionCard>

    <SectionCard
      v-show="section === 'offline'"
      title="离线下载"
      hint="磁力在下载前看不到里面有哪些文件，挑不了集；115 的离线配额按任务数扣，一集一个磁力的话追一部剧就是几十次。所以磁力排在 115 分享之后，还有下面几道闸"
    >
      <FieldRow label="默认策略" hint="单个订阅可以在「修改订阅 → 高级」里另设。电影一个磁力就是整部，「只下合集包」对电影照常下">
        <HSegmented
          v-model="cfg.offline_mode"
          :options="[
            { label: '只下合集包', value: 'pack' },
            { label: '只转存分享', value: 'share_only' },
            { label: '不限', value: 'any' },
          ]"
        />
      </FieldRow>
      <FieldRow label="新集先等分享" hint="一集播出后这么久之内只找 115 分享，新集的分享一般几小时内就有；0 = 不等">
        <div class="unit"><HNumberInput v-model="cfg.offline_wait_hours" :min="0" :max="168" aria-label="小时" /><span>小时</span></div>
      </FieldRow>
      <FieldRow label="每月最多" hint="订阅每月最多提交多少个离线任务（每个订阅每轮最多一个）；0 = 不限">
        <div class="unit"><HNumberInput v-model="cfg.offline_monthly" :min="0" aria-label="任务数" /><span>个任务</span></div>
      </FieldRow>
      <FieldRow label="配额保留" hint="要提交离线时先查一次 115 剩余配额，低于这个数就停下，留给手动离线；0 = 不查">
        <div class="unit"><HNumberInput v-model="cfg.offline_reserve" :min="0" aria-label="次数" /><span>次</span></div>
      </FieldRow>
    </SectionCard>

    <SectionCard v-show="section === 'unlock'" title="RE0 自动解锁" hint="解锁之前看不到分享里有什么，积分花了拿不回来，所以只有下面几条都满足才会花">
      <FieldRow label="单条上限" hint="一条资源要的积分不超过它才自动解锁；0 = 不自动解锁（解锁过的、免费的照常用）">
        <div class="unit"><HNumberInput v-model="cfg.re0_unlock_max" :min="0" aria-label="积分" /><span>积分</span></div>
      </FieldRow>
      <FieldRow label="每日预算" :hint="`每天自动解锁最多花多少；0 = 不限。今天已花 ${spent} 积分`">
        <div class="unit"><HNumberInput v-model="cfg.re0_daily_budget" :min="0" aria-label="积分" /><span>积分</span></div>
      </FieldRow>
      <HAlert status="accent" class="note">
        另外几道闸是固定的：资源标题要能看出包含缺的集（「全集」「合集」这种估不出的不花钱）、画质要命中洗版策略里至少一条规则、
        一个订阅一轮最多花钱解锁 1 条，解锁报积分不足或没登录时这一轮全部停下。超出上限的资源只通知一次，到「找资源」手动解锁。
      </HAlert>
    </SectionCard>

    <SectionCard v-show="section === 'notify'" title="通知">
      <FieldRow label="推送" hint="自动解锁花了积分、资源内容不对（整理认成了别的片）这两种总是推">
        <HMultiSelect
          v-model="notify"
          :options="[
            { label: '新订阅', value: 'created' },
            { label: '提交了资源', value: 'submit' },
            { label: '补上了缺集', value: 'ingested' },
            { label: '订阅完成', value: 'done' },
            { label: '长期找不到', value: 'stalled' },
          ]"
          placeholder="都不推"
        />
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
.unit > :first-child {
  width: 120px;
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
.note {
  margin-top: 6px;
  font-size: 12px;
  line-height: 1.6;
}
</style>
