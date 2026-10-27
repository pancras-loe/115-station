<script setup lang="ts">
import { computed, onMounted, ref, type Component } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HModal from '@/components/hero/HModal.vue'
import HSearchField from '@/components/hero/HSearchField.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import { BrushCleaning, CalendarCheck, CloudOff, CloudUpload, FolderInput, Images, Play, Settings2, Stamp, Trash2 } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import SchedulePicker from '@/components/ui/SchedulePicker.vue'
import TestBanner, { type BannerState } from '@/components/ui/TestBanner.vue'
import CoverGenModal from '@/components/plugins/CoverGenModal.vue'
import AliTransferModal from '@/components/plugins/AliTransferModal.vue'
import RecycleCleanModal from '@/components/plugins/RecycleCleanModal.vue'
import DirCleanModal from '@/components/plugins/DirCleanModal.vue'
import OfflineCleanModal from '@/components/plugins/OfflineCleanModal.vue'
import AliDirCleanModal from '@/components/plugins/AliDirCleanModal.vue'
import AliRecycleCleanModal from '@/components/plugins/AliRecycleCleanModal.vue'
import PtSigninModal from '@/components/plugins/PtSigninModal.vue'
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

// ============ 清理 115 离线任务 ============
const ocShow = ref(false)
const ocRun = () =>
  bgRun({
    key: 'offclean',
    verb: '清理',
    confirm: {
      title: '立即清理离线任务',
      content: '按已保存的设置，把打开清理的账号里下载失败、卡住太久的离线任务删掉（只删任务，不删网盘里的文件）。确定？',
      action: '删除任务',
    },
    status: pluginsApi.offCleanConfig,
    run: pluginsApi.runOffClean,
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

// ============ PT 站签到 ============
const ptShow = ref(false)
async function ptRun() {
  busy.value.ptsignin = true
  results.value.ptsignin = { status: 'pending', title: '开始签到…' }
  try {
    const d = await pluginsApi.ptRunAll()
    results.value.ptsignin = { status: 'ok', title: d.message || '开始签到', detail: '一个站接一个跑，结果在「配置规则」里看，跑完推送汇总。' }
  } catch (e) {
    results.value.ptsignin = { status: 'err', title: '没能开始', detail: e instanceof Error ? e.message : '' }
  } finally {
    busy.value.ptsignin = false
  }
}

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
type CatKey = 'signin' | 'clean' | 'media' | 'transfer'

/** 分类按用途分，不按网盘分：同一件事（清理）115 与阿里云盘的放在一起，卡片上另标作用在哪个盘 */
const categories: { key: CatKey; name: string; hint: string }[] = [
  { key: 'signin', name: '签到保号', hint: '每天定时签到，领积分、保账号' },
  { key: 'clean', name: '网盘清理', hint: '定时清掉回收站、离线任务和指定目录，网盘不越用越满' },
  { key: 'media', name: '媒体库工具', hint: '媒体库封面、STRM 换地址与接管' },
  { key: 'transfer', name: '转存搬运', hint: '把别的网盘里的资源搬进 115' },
]

interface Plugin {
  key: string
  name: string
  icon: Component
  desc: string
  cat: CatKey
  /** 作用在哪：115 / 阿里云盘 / PT 站 / 本地 …，卡片右上角的小标签 */
  scope: string
  /** 搜索时额外认的词（拼音缩写、英文、常见叫法），不显示 */
  keywords?: string
  runLabel?: string
  onConfig?: () => void
  onRun?: () => void
}

const plugins: Plugin[] = [
  {
    key: 'checkin',
    cat: 'signin',
    scope: '115',
    keywords: 'qiandao 积分',
    name: '115 每日签到',
    icon: CalendarCheck,
    desc: '按 cron 计划自动签到领积分，结果推送企微 / TG。需先在「账号与媒体库」扫码登录。',
    runLabel: '立即签到',
    onConfig: ckOpen,
    onRun: ckRun,
  },
  {
    key: 'ptsignin',
    cat: 'signin',
    scope: 'PT 站',
    keywords: 'qiandao pt 保号 cookie',
    name: 'PT 站签到',
    icon: Stamp,
    desc: '给常用的 PT 站每天签到（织梦、蟹黄堡、听听歌、学校…），认不出签到结果时至少确认登录保号。贴 Cookie 就能用，每天在时间窗里随机一刻跑，结果推送汇总。',
    runLabel: '立即签到',
    onConfig: () => (ptShow.value = true),
    onRun: ptRun,
  },
  {
    key: 'covergen',
    cat: 'media',
    scope: 'Emby',
    keywords: '封面 cover poster',
    name: '媒体库海报',
    icon: Images,
    desc: '从 Emby 媒体库选取海报，合成带库名的封面并推送。未配置 Emby 时使用本地整理台账。支持定时与多种样式。',
    runLabel: '立即生成',
    onConfig: () => (cgShow.value = true),
    onRun: cgRun,
  },
  {
    key: 'rbclean',
    cat: 'clean',
    scope: '115',
    keywords: '回收站 recycle trash',
    name: '清理 115 回收站',
    icon: Trash2,
    desc: '按计划彻底删除回收站里超过保留天数（7 / 30 / 自定义天）的内容，保留期内的照旧能还原。每个 115 账号单独设清不清、保留几天。',
    runLabel: '立即清理',
    onConfig: () => (rbShow.value = true),
    onRun: rbRun,
  },
  {
    key: 'offclean',
    cat: 'clean',
    scope: '115',
    keywords: '离线 下载 磁力 offline',
    name: '清理 115 离线任务',
    icon: CloudOff,
    desc: '按计划删掉 115 离线列表里下载失败、卡在百分之几很久不动的任务，默认只动本站提交的。只删任务，不删网盘里的文件。',
    runLabel: '立即清理',
    onConfig: () => (ocShow.value = true),
    onRun: ocRun,
  },
  {
    key: 'dirclean',
    cat: 'clean',
    scope: '115',
    keywords: '清空 目录 最近接收 云下载',
    name: '定时清空 115 目录',
    icon: BrushCleaning,
    desc: '按计划把指定的几个 115 目录（云下载、最近接收、冗余……）里的内容删进回收站，目录本身留着。媒体库与转存文件夹不能选。',
    runLabel: '立即清空',
    onConfig: () => (dcShow.value = true),
    onRun: dcRun,
  },
  {
    key: 'ali2115',
    cat: 'transfer',
    scope: '阿里云盘',
    keywords: 'aliyun 秒传 分享 sha1',
    name: '阿里云盘秒传',
    icon: CloudUpload,
    desc: '贴阿里云盘分享，按 SHA1 秒传进 115 转存文件夹并自动整理入库，文件内容不经过本站。115 上没有的文件秒传不了。',
    runLabel: '去贴链接',
    onConfig: () => (aliShow.value = true),
    onRun: () => router.push({ name: 'media-transfer', query: { tab: 'link' } }),
  },
  {
    key: 'strmmigrate',
    cat: 'media',
    scope: '本地 STRM',
    keywords: 'strm 迁移 替换地址 接管 p115strmhelper 换ip',
    name: 'STRM 迁移',
    icon: FolderInput,
    desc: '换了 IP / 端口 / 挂载点：把本地 STRM 开头的旧地址批量换成新的，本站和别的项目写的都行。从 p115strmhelper 等项目换过来：接管已有 STRM，路径不动，Emby 观看记录保留。',
    runLabel: '接管其他项目',
    onConfig: () => smOpen('rebase'),
    onRun: () => smOpen('adopt'),
  },
  {
    key: 'alidirclean',
    cat: 'clean',
    scope: '阿里云盘',
    keywords: 'aliyun 清空 目录',
    name: '定时清空阿里云盘目录',
    icon: BrushCleaning,
    desc: '按计划把阿里云盘里指定的几个目录（来自分享……）里的内容删进回收站，目录本身留着。共用「阿里云盘秒传」登录的账号。',
    runLabel: '立即清空',
    onConfig: () => (adcShow.value = true),
    onRun: adcRun,
  },
  {
    key: 'alirbclean',
    cat: 'clean',
    scope: '阿里云盘',
    keywords: 'aliyun 回收站 recycle trash',
    name: '清理阿里云盘回收站',
    icon: Trash2,
    desc: '按计划彻底删除阿里云盘回收站的内容：整个清空，或只删超过保留天数的。资源盘、备份盘都清，每个账号单独设。',
    runLabel: '立即清理',
    onConfig: () => (arbShow.value = true),
    onRun: arbRun,
  },
]

// ============ 分类与搜索 ============
// 分类记在地址栏（?cat=），刷新、从别的页跳回来都还在
const cat = computed<CatKey | 'all'>({
  get: () => {
    const q = route.query.cat
    return categories.some((c) => c.key === q) ? (q as CatKey) : 'all'
  },
  set: (v) => {
    router.replace({ query: { ...route.query, cat: v === 'all' ? undefined : v } })
  },
})
const keyword = ref('')

const catOptions = computed(() => [
  { label: `全部 ${plugins.length}`, value: 'all' as const },
  ...categories.map((c) => ({ label: `${c.name} ${plugins.filter((p) => p.cat === c.key).length}`, value: c.key })),
])

/** 搜索不分大小写、忽略空白，名字 / 说明 / 作用范围 / 关键词任一包含就算 */
function hit(p: Plugin, kw: string) {
  const hay = [p.name, p.desc, p.scope, p.keywords ?? '', categories.find((c) => c.key === p.cat)?.name ?? '']
    .join(' ')
    .toLowerCase()
    .replace(/\s+/g, '')
  return kw.split(/\s+/).every((w) => hay.includes(w))
}

/** 搜索时跨分类找（选着分类也不限制，免得用户以为没有），按分类分组显示 */
const groups = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  return categories
    .filter((c) => kw || cat.value === 'all' || c.key === cat.value)
    .map((c) => ({ ...c, items: plugins.filter((p) => p.cat === c.key && (!kw || hit(p, kw))) }))
    .filter((g) => g.items.length)
})
const shownCount = computed(() => groups.value.reduce((n, g) => n + g.items.length, 0))
</script>

<template>
  <SectionCard title="插件扩展" :hint="keyword.trim() ? `找到 ${shownCount} 个插件` : `共 ${plugins.length} 个插件`">
    <div class="toolbar">
      <HSegmented v-model="cat" :options="catOptions" size="sm" aria-label="插件分类" class="cats" />
      <HSearchField v-model="keyword" class="search" placeholder="搜索插件：签到、回收站、阿里云盘…" />
    </div>

    <EmptyState v-if="!groups.length" :text="`没有和「${keyword.trim()}」相关的插件`" />

    <section v-for="g in groups" :key="g.key" class="group">
      <header class="group-head">
        <h3 class="group-name">{{ g.name }}</h3>
        <span class="group-hint">{{ g.hint }}</span>
      </header>
      <div class="grid">
        <div v-for="p in g.items" :key="p.key" class="plugin">
          <div class="head">
            <div class="ico"><component :is="p.icon" :size="17" :stroke-width="1.8" /></div>
            <div class="name">{{ p.name }}</div>
            <HChip>{{ p.scope }}</HChip>
          </div>

          <p class="desc">{{ p.desc }}</p>

          <TestBanner :state="results[p.key]" />

          <div class="foot">
            <HButton variant="tertiary" size="sm" @click="p.onConfig?.()">
              <template #icon><Settings2 :size="14" /></template>
              配置规则
            </HButton>
            <HButton variant="secondary" size="sm" :loading="busy[p.key]" @click="p.onRun?.()">
              <template #icon><Play :size="14" /></template>
              {{ p.runLabel }}
            </HButton>
          </div>
        </div>
      </div>
    </section>

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
    <OfflineCleanModal v-model:show="ocShow" />
    <AliDirCleanModal v-model:show="adcShow" />
    <AliRecycleCleanModal v-model:show="arbShow" />
    <PtSigninModal v-model:show="ptShow" />
    <StrmMigrateModal v-model:show="smShow" v-model:tab="smTab" />
  </SectionCard>
</template>

<style scoped>
.toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px 12px;
  margin-bottom: 4px;
}
.cats {
  min-width: 0;
  max-width: 100%;
}
.search {
  flex: 1 1 220px;
  max-width: 320px;
  margin-left: auto;
}
@media (max-width: 720px) {
  .search {
    max-width: none;
    margin-left: 0;
  }
}

.group + .group {
  margin-top: 22px;
}
.group-head {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 4px 10px;
  margin: 14px 0 10px;
}
.group-name {
  margin: 0;
  font-size: 13.5px;
  font-weight: 600;
  color: var(--foreground);
}
.group-hint {
  font-size: 12px;
  color: var(--muted);
}

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
  .plugin:hover {
    background: color-mix(in oklab, var(--surface-secondary) 70%, var(--surface-tertiary));
  }
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
