<script setup lang="ts">
import { ref, type Component } from 'vue'
import { useRouter } from 'vue-router'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HModal from '@/components/hero/HModal.vue'
import { heroTone } from '@/components/hero/tone'
import { BrushCleaning, CalendarCheck, CloudUpload, Images, Play, Settings2, Trash2 } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import SchedulePicker from '@/components/ui/SchedulePicker.vue'
import TestBanner, { type BannerState } from '@/components/ui/TestBanner.vue'
import CoverGenModal from '@/components/plugins/CoverGenModal.vue'
import AliTransferModal from '@/components/plugins/AliTransferModal.vue'
import RecycleCleanModal from '@/components/plugins/RecycleCleanModal.vue'
import DirCleanModal from '@/components/plugins/DirCleanModal.vue'
import { pluginsApi } from '@/api'
import { toastError, useFeedback } from '@/composables/useFeedback'

const { message, dialog } = useFeedback()
const router = useRouter()

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

// ============ 清理 115 回收站 ============
const rbShow = ref(false)

/** 立即清理在后台跑（回收站大时要翻很多页），这里轮询配置里的 running / last_result */
async function rbRun() {
  const ok = await dialog.confirm({
    title: '立即清理回收站',
    content: '按已保存的设置，把打开清理的账号里超过保留天数的回收站内容彻底删除，无法还原。确定？',
    tone: 'danger',
    actions: [
      { label: '取消', value: false, variant: 'tertiary' },
      { label: '彻底删除', value: true, variant: 'danger' },
    ],
  })
  if (!ok) return
  busy.value.rbclean = true
  results.value.rbclean = { status: 'pending', title: '清理中…' }
  try {
    const before = (await pluginsApi.rbCleanConfig()).last_result_at
    await pluginsApi.runRbClean()
    for (let i = 0; i < 200; i++) {
      await new Promise((r) => setTimeout(r, 3000))
      const d = await pluginsApi.rbCleanConfig()
      if (!d.running && d.last_result_at && d.last_result_at !== before) {
        results.value.rbclean = { status: d.last_ok ? 'ok' : 'err', title: d.last_ok ? '清理完成' : '清理出错', detail: d.last_result }
        return
      }
    }
    results.value.rbclean = { status: 'pending', title: '还在清理，结果稍后见通知与日志' }
  } catch (e) {
    results.value.rbclean = { status: 'err', title: '清理失败', detail: e instanceof Error ? e.message : '' }
  } finally {
    busy.value.rbclean = false
  }
}

// ============ 定时清空 115 目录 ============
const dcShow = ref(false)

/** 立即清空在后台跑，这里轮询配置里的 running / last_result（同回收站清理） */
async function dcRun() {
  const ok = await dialog.confirm({
    title: '立即清空目录',
    content: '按已保存的设置，把这些目录里的全部内容删进 115 回收站（目录本身留着，回收站里能还原）。确定？',
    tone: 'danger',
    actions: [
      { label: '取消', value: false, variant: 'tertiary' },
      { label: '清空', value: true, variant: 'danger' },
    ],
  })
  if (!ok) return
  busy.value.dirclean = true
  results.value.dirclean = { status: 'pending', title: '清空中…' }
  try {
    const before = (await pluginsApi.dirCleanConfig()).last_result_at
    await pluginsApi.runDirClean()
    for (let i = 0; i < 200; i++) {
      await new Promise((r) => setTimeout(r, 3000))
      const d = await pluginsApi.dirCleanConfig()
      if (!d.running && d.last_result_at && d.last_result_at !== before) {
        results.value.dirclean = { status: d.last_ok ? 'ok' : 'err', title: d.last_ok ? '清空完成' : '清空出错', detail: d.last_result }
        return
      }
    }
    results.value.dirclean = { status: 'pending', title: '还在清空，结果稍后见通知与日志' }
  } catch (e) {
    results.value.dirclean = { status: 'err', title: '清空失败', detail: e instanceof Error ? e.message : '' }
  } finally {
    busy.value.dirclean = false
  }
}

// ============ 阿里云盘秒传 ============
const aliShow = ref(false)

// ============ 插件清单 ============
interface Plugin {
  key: string
  name: string
  icon: Component
  desc: string
  available: boolean
  runLabel?: string
  onConfig?: () => void
  onRun?: () => void
}

const plugins: Plugin[] = [
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
    key: 'rbclean',
    name: '清理 115 回收站',
    icon: Trash2,
    desc: '按计划彻底删除回收站里超过保留天数（7 / 30 / 自定义天）的内容，保留期内的照旧能还原。每个 115 账号单独设清不清、保留几天。',
    available: true,
    runLabel: '立即清理',
    onConfig: () => (rbShow.value = true),
    onRun: rbRun,
  },
  {
    key: 'dirclean',
    name: '定时清空 115 目录',
    icon: BrushCleaning,
    desc: '按计划把指定的几个 115 目录（云下载、最近接收、冗余……）里的内容删进回收站，目录本身留着。媒体库与转存文件夹不能选。',
    available: true,
    runLabel: '立即清空',
    onConfig: () => (dcShow.value = true),
    onRun: dcRun,
  },
  {
    key: 'ali2115',
    name: '阿里云盘秒传',
    icon: CloudUpload,
    desc: '贴阿里云盘分享，按 SHA1 秒传进 115 转存文件夹并自动整理入库，文件内容不经过本站。115 上没有的文件秒传不了。',
    available: true,
    runLabel: '去贴链接',
    onConfig: () => (aliShow.value = true),
    onRun: () => router.push({ name: 'media-transfer', query: { tab: 'link' } }),
  },
]

const availableCount = plugins.filter((p) => p.available).length
</script>

<template>
  <SectionCard title="插件扩展" :hint="`共 ${availableCount} 个插件`">
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
          <HButton variant="tertiary" size="sm" :disabled="!p.available" @click="p.onConfig?.()">
            <template #icon><Settings2 :size="14" /></template>
            配置规则
          </HButton>
          <HButton variant="secondary" size="sm" :disabled="!p.available" :loading="busy[p.key]" @click="p.onRun?.()">
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
        wide
        tip="开启后按计划自动签到领积分（连续签到有加成，积分可在 115 App 积分中心使用）。失败自动重试 3 次，结果推送通知。"
      >
        <SchedulePicker v-model="ckForm.cron" v-model:enabled="ckForm.enabled" toggle placeholder="0 8 * * *" />
      </FieldRow>
      <template #footer>
        <div class="foot-right">
          <HButton variant="tertiary" @click="ckShow = false">取消</HButton>
          <HButton variant="primary" :loading="ckSaving" @click="ckSave">保存</HButton>
        </div>
      </template>
    </HModal>

    <CoverGenModal ref="cgModal" v-model:show="cgShow" />
    <AliTransferModal v-model:show="aliShow" />
    <RecycleCleanModal v-model:show="rbShow" />
    <DirCleanModal v-model:show="dcShow" />
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
