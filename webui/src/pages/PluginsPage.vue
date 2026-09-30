<script setup lang="ts">
import { ref } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HModal from '@/components/hero/HModal.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import { heroTone } from '@/components/hero/tone'
import { CalendarCheck, FileSearch, Images, Play, Settings2, UsersRound } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import CronField from '@/components/ui/CronField.vue'
import TestBanner, { type BannerState } from '@/components/ui/TestBanner.vue'
import CoverGenModal from '@/components/plugins/CoverGenModal.vue'
import PersonFillModal from '@/components/plugins/PersonFillModal.vue'
import MetaFillModal from '@/components/plugins/MetaFillModal.vue'
import { pluginsApi } from '@/api'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { useQueueStore } from '@/stores/queue'

const { message } = useFeedback()

/** 每张插件卡的「立即运行」结果，展示在卡片内 */
const results = ref<Record<string, BannerState | null>>({})
const busy = ref<Record<string, boolean>>({})

// ============ 115 每日签到 ============
const ckShow = ref(false)
const ckForm = ref({ enabled: false, cron: '0 8 * * *' })
const ckSaving = ref(false)

async function ckOpen() {
  ckShow.value = true
  try {
    const d = await pluginsApi.checkinConfig()
    ckForm.value = { enabled: !!d.enabled, cron: d.cron || '0 8 * * *' }
  } catch {
    // 首次使用尚无配置
  }
}

async function ckSave() {
  ckSaving.value = true
  try {
    await pluginsApi.saveCheckin({ enabled: ckForm.value.enabled, cron: ckForm.value.cron.trim() || '0 8 * * *' })
    message.success('保存成功')
    ckShow.value = false
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    ckSaving.value = false
  }
}

async function ckRun() {
  busy.value.checkin = true
  results.value.checkin = { status: 'pending', title: '签到中…' }
  try {
    const d = await pluginsApi.runCheckin()
    results.value.checkin = { status: 'ok', title: d.message || '签到成功', trailing: new Date().toLocaleTimeString('zh-CN') }
  } catch (e) {
    results.value.checkin = { status: 'err', title: '签到失败', detail: e instanceof Error ? e.message : '' }
  } finally {
    busy.value.checkin = false
  }
}

// ============ 媒体库海报 ============
const cgShow = ref(false)
const cgModal = ref<InstanceType<typeof CoverGenModal> | null>(null)

async function cgRun() {
  busy.value.covergen = true
  results.value.covergen = { status: 'pending', title: '生成中…' }
  try {
    const d = await pluginsApi.runCoverGen()
    results.value.covergen = { status: d.warnings?.length ? 'err' : 'ok', title: d.warnings?.length ? '封面已生成，部分项目未完成' : '封面生成完成', detail: d.message }
    await cgModal.value?.loadCovers()
  } catch (e) {
    results.value.covergen = { status: 'err', title: '生成失败', detail: e instanceof Error ? e.message : '' }
  } finally {
    busy.value.covergen = false
  }
}

// ============ 演职人员补全 ============
// 跑一次可能几十分钟，入任务队列（人物队列）后立即返回，进度在顶栏任务面板 / 任务中心
const pfShow = ref(false)
const queue = useQueueStore()

async function pfRun() {
  busy.value.personfill = true
  try {
    const d = await pluginsApi.runPersonFill()
    results.value.personfill = { status: 'ok', title: d.message || '已加入任务队列', detail: '进度见顶栏任务面板或任务中心' }
    await queue.submitted(d.job_id)
  } catch (e) {
    results.value.personfill = { status: 'err', title: '提交失败', detail: e instanceof Error ? e.message : '' }
  } finally {
    busy.value.personfill = false
  }
}

// ============ 媒体信息补全 ============
// 扫描后建刮削 / 探测任务，入任务队列（刮削队列）后立即返回
const mfShow = ref(false)

async function mfRun() {
  busy.value.metafill = true
  try {
    const d = await pluginsApi.runMetaFill()
    results.value.metafill = { status: 'ok', title: d.message || '已加入任务队列', detail: '扫描完会另建刮削 / 探测任务，进度见任务中心' }
    await queue.submitted(d.job_id)
  } catch (e) {
    results.value.metafill = { status: 'err', title: '提交失败', detail: e instanceof Error ? e.message : '' }
  } finally {
    busy.value.metafill = false
  }
}

// ============ 插件清单 ============
const plugins = [
  {
    key: 'checkin',
    name: '115 每日签到',
    icon: CalendarCheck,
    desc: '按 cron 计划自动签到领积分，结果推送企微 / TG。需先在「账号与媒体库」扫码登录。',
    available: true,
    runLabel: '立即签到',
    onConfig: ckOpen,
    onRun: ckRun,
  },
  {
    key: 'covergen',
    name: '媒体库海报',
    icon: Images,
    desc: '从 Emby 媒体库选取海报，合成带库名的封面并推送。未配置 Emby 时使用本地整理台账。支持定时与多种样式。',
    available: true,
    runLabel: '立即生成',
    onConfig: () => (cgShow.value = true),
    onRun: cgRun,
  },
  {
    key: 'personfill',
    name: '演职人员补全',
    icon: UsersRound,
    desc: '给 Emby 里缺头像、名字不是中文的演员 / 导演 / 编剧补上 TMDB 头像、中文名与中文简介。头像经本站代理下载后通过 Emby API 写入，支持定时。',
    available: true,
    runLabel: '立即运行',
    onConfig: () => (pfShow.value = true),
    onRun: pfRun,
  },
  {
    key: 'metafill',
    name: '媒体信息补全',
    icon: FileSearch,
    desc: '定时找出本地媒体库里缺 NFO、海报、背景图的片目补刮，Emby 里还没有媒体信息的视频让它提前探测。探测按自动规则、有单次上限，避免反复取 115 直链。',
    available: true,
    runLabel: '立即运行',
    onConfig: () => (mfShow.value = true),
    onRun: mfRun,
  },
]

const availableCount = plugins.filter((p) => p.available).length
</script>

<template>
  <SectionCard title="插件扩展" :hint="`共 ${plugins.length} 个插件 · ${availableCount} 个可用`">
    <div class="grid">
      <div v-for="p in plugins" :key="p.key" class="plugin" :class="{ off: !p.available }">
        <div class="head">
          <div class="ico"><component :is="p.icon" :size="17" :stroke-width="1.8" /></div>
          <div class="name">{{ p.name }}</div>
          <HChip :color="heroTone(p.available ? 'success' : 'default')">
            {{ p.available ? '可用' : '规划中' }}
          </HChip>
        </div>

        <p class="desc">{{ p.desc }}</p>

        <TestBanner :state="results[p.key]" />

        <div class="foot">
          <HButton variant="tertiary" size="sm" :disabled="!p.available" @click="p.onConfig()">
            <template #icon><Settings2 :size="14" /></template>
            配置规则
          </HButton>
          <HButton variant="secondary" size="sm" :disabled="!p.available" :loading="busy[p.key]" @click="p.onRun()">
            <template #icon><Play :size="14" /></template>
            {{ p.runLabel }}
          </HButton>
        </div>
      </div>
    </div>

    <!-- 115 签到配置 -->
    <HModal v-model:show="ckShow" title="115 每日签到" width="480px">
      <FieldRow
        label="自动签到"
        tip="开启后每天自动签到领积分（连续签到有加成，积分可在 115 App 积分中心使用）。"
      >
        <HSegmented v-model="ckForm.enabled" :options="[{ label: '开启', value: true }, { label: '关闭', value: false }]" />
      </FieldRow>
      <FieldRow
        label="执行计划"
        tip="例：0 8 * * * = 每天 08:00；30 7 * * 1-5 = 工作日 07:30。失败自动重试 3 次，结果推送通知。"
      >
        <CronField v-model="ckForm.cron" placeholder="0 8 * * *" />
      </FieldRow>
      <template #footer>
        <div class="foot-right">
          <HButton variant="tertiary" @click="ckShow = false">取消</HButton>
          <HButton variant="primary" :loading="ckSaving" @click="ckSave">保存</HButton>
        </div>
      </template>
    </HModal>

    <CoverGenModal ref="cgModal" v-model:show="cgShow" />
    <PersonFillModal v-model:show="pfShow" />
    <MetaFillModal v-model:show="mfShow" />
  </SectionCard>
</template>

<style scoped>
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(290px, 1fr));
  gap: 14px;
}

.plugin {
  display: flex;
  flex-direction: column;
  padding: 18px;
  border-radius: 20px;
  background: var(--surface-secondary);
  transition: background-color 0.15s;
}
@media (hover: hover) {
  .plugin:not(.off):hover {
    background: color-mix(in oklab, var(--surface-secondary) 70%, var(--surface-tertiary));
  }
}
.plugin.off {
  opacity: 0.6;
}

.head {
  display: flex;
  align-items: center;
  gap: 9px;
  margin-bottom: 9px;
}
.ico {
  width: 30px;
  height: 30px;
  flex-shrink: 0;
  border-radius: 999px;
  display: grid;
  place-items: center;
  background: var(--accent-soft);
  color: var(--accent-soft-foreground);
}
.plugin.off .ico {
  background: var(--default);
  color: var(--muted);
}
.name {
  flex: 1;
  min-width: 0;
  font-size: 13.5px;
  font-weight: 600;
  color: var(--foreground);
}

.desc {
  flex: 1;
  margin: 0 0 12px;
  font-size: 12.5px;
  line-height: 1.7;
  color: var(--muted);
}

.foot {
  display: flex;
  gap: 8px;
  margin-top: 10px;
}

.foot-right {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
