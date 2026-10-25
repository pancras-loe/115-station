<script setup lang="ts">
import { onMounted, ref, type Component } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HModal from '@/components/hero/HModal.vue'
import { heroTone } from '@/components/hero/tone'
import { BrushCleaning, CalendarCheck, CloudUpload, FolderInput, Images, Play, Settings2, Trash2 } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import SchedulePicker from '@/components/ui/SchedulePicker.vue'
import TestBanner, { type BannerState } from '@/components/ui/TestBanner.vue'
import CoverGenModal from '@/components/plugins/CoverGenModal.vue'
import AliTransferModal from '@/components/plugins/AliTransferModal.vue'
import RecycleCleanModal from '@/components/plugins/RecycleCleanModal.vue'
import DirCleanModal from '@/components/plugins/DirCleanModal.vue'
import AliDirCleanModal from '@/components/plugins/AliDirCleanModal.vue'
import AliRecycleCleanModal from '@/components/plugins/AliRecycleCleanModal.vue'
import StrmMigrateModal, { type StrmMigrateTab } from '@/components/plugins/StrmMigrateModal.vue'
import { pluginsApi } from '@/api'
import { toastError, useFeedback } from '@/composables/useFeedback'

const { message, dialog } = useFeedback()
const router = useRouter()
const route = useRoute()

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

// ============ 后台跑的清理任务（回收站清理 / 清空目录，115 与阿里云盘各一份） ============
interface BgStatus {
  running?: boolean
  last_ok?: boolean
  last_result?: string
  last_result_at?: string
}
interface BgTask {
  key: string
  confirm: { title: string; content: string; action: string }
  verb: string // 清理 / 清空
  status: () => Promise<BgStatus>
  run: () => Promise<unknown>
}

/** 确认后让后端在后台跑，这里轮询配置里的 running / last_result，结果显示在卡片里 */
async function bgRun(t: BgTask) {
  const ok = await dialog.confirm({
    title: t.confirm.title,
    content: t.confirm.content,
    tone: 'danger',
    actions: [
      { label: '取消', value: false, variant: 'tertiary' },
      { label: t.confirm.action, value: true, variant: 'danger' },
    ],
  })
  if (!ok) return
  busy.value[t.key] = true
  results.value[t.key] = { status: 'pending', title: `${t.verb}中…` }
  try {
    const before = (await t.status()).last_result_at
    await t.run()
    for (let i = 0; i < 200; i++) {
      await new Promise((r) => setTimeout(r, 3000))
      const d = await t.status()
      if (!d.running && d.last_result_at && d.last_result_at !== before) {
        results.value[t.key] = { status: d.last_ok ? 'ok' : 'err', title: d.last_ok ? `${t.verb}完成` : `${t.verb}出错`, detail: d.last_result }
        return
      }
    }
    results.value[t.key] = { status: 'pending', title: `还在${t.verb}，结果稍后见通知与日志` }
  } catch (e) {
    results.value[t.key] = { status: 'err', title: `${t.verb}失败`, detail: e instanceof Error ? e.message : '' }
  } finally {
    busy.value[t.key] = false
  }
}

// ============ 清理 115 回收站 ============
const rbShow = ref(false)
const rbRun = () =>
  bgRun({
    key: 'rbclean',
    verb: '清理',
    confirm: {
      title: '立即清理回收站',
      content: '按已保存的设置，把打开清理的账号里超过保留天数的回收站内容彻底删除，无法还原。确定？',
      action: '彻底删除',
    },
    status: pluginsApi.rbCleanConfig,
    run: pluginsApi.runRbClean,
  })

// ============ 定时清空 115 目录 ============
const dcShow = ref(false)
const dcRun = () =>
  bgRun({
    key: 'dirclean',
    verb: '清空',
    confirm: {
      title: '立即清空目录',
      content: '按已保存的设置，把这些目录里的全部内容删进 115 回收站（目录本身留着，回收站里能还原）。确定？',
      action: '清空',
    },
    status: pluginsApi.dirCleanConfig,
    run: pluginsApi.runDirClean,
  })

// ============ 定时清空阿里云盘目录 ============
const adcShow = ref(false)
const adcRun = () =>
  bgRun({
    key: 'alidirclean',
    verb: '清空',
    confirm: {
      title: '立即清空阿里云盘目录',
      content: '按已保存的设置，把这些目录里的全部内容删进阿里云盘回收站（目录本身留着，回收站里能还原）。确定？',
      action: '清空',
    },
    status: pluginsApi.aliDirCleanConfig,
    run: pluginsApi.runAliDirClean,
  })

// ============ 清理阿里云盘回收站 ============
const arbShow = ref(false)
const arbRun = () =>
  bgRun({
    key: 'alirbclean',
    verb: '清理',
    confirm: {
      title: '立即清理阿里云盘回收站',
      content: '按已保存的设置，把打开清理的阿里云盘账号回收站里的内容彻底删除，无法还原。确定？',
      action: '彻底删除',
    },
    status: pluginsApi.aliRbCleanConfig,
    run: pluginsApi.runAliRbClean,
  })

// ============ 阿里云盘秒传 ============
const aliShow = ref(false)

// ============ STRM 迁移（替换地址 / 接管其他项目） ============
const smShow = ref(false)
const smTab = ref<StrmMigrateTab>('rebase')
function smOpen(tab: StrmMigrateTab) {
  smTab.value = tab
  smShow.value = true
}
// 「Strm 管理 → 全量」的「接管已有 STRM」跳过来：/plugins?open=strm-migrate&tab=adopt
onMounted(() => {
  if (route.query.open === 'strm-migrate') smOpen(route.query.tab === 'adopt' ? 'adopt' : 'rebase')
})

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
  {
    key: 'strmmigrate',
    name: 'STRM 迁移',
    icon: FolderInput,
    desc: '换了 IP / 端口 / 挂载点：把本地 STRM 开头的旧地址批量换成新的，本站和别的项目写的都行。从 p115strmhelper 等项目换过来：接管已有 STRM，路径不动，Emby 观看记录保留。',
    available: true,
    runLabel: '接管其他项目',
    onConfig: () => smOpen('rebase'),
    onRun: () => smOpen('adopt'),
  },
  {
    key: 'alidirclean',
    name: '定时清空阿里云盘目录',
    icon: BrushCleaning,
    desc: '按计划把阿里云盘里指定的几个目录（来自分享……）里的内容删进回收站，目录本身留着。共用「阿里云盘秒传」登录的账号。',
    available: true,
    runLabel: '立即清空',
    onConfig: () => (adcShow.value = true),
    onRun: adcRun,
  },
  {
    key: 'alirbclean',
    name: '清理阿里云盘回收站',
    icon: Trash2,
    desc: '按计划彻底删除阿里云盘回收站的内容：整个清空，或只删超过保留天数的。资源盘、备份盘都清，每个账号单独设。',
    available: true,
    runLabel: '立即清理',
    onConfig: () => (arbShow.value = true),
    onRun: arbRun,
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
    <AliDirCleanModal v-model:show="adcShow" />
    <AliRecycleCleanModal v-model:show="arbShow" />
    <StrmMigrateModal v-model:show="smShow" v-model:tab="smTab" />
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
