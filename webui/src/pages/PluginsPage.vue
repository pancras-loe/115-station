<script setup lang="ts">
import { computed, h, ref } from 'vue'
import {
  NButton,
  NDataTable,
  NInput,
  NModal,
  NPopconfirm,
  NRadioButton,
  NRadioGroup,
  NSelect,
  NTag,
  type DataTableColumns,
} from 'naive-ui'
import { CalendarCheck, Images, LibraryBig, Play, Settings2, Trash2 } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import CronField from '@/components/ui/CronField.vue'
import TestBanner, { type BannerState } from '@/components/ui/TestBanner.vue'
import { pluginsApi } from '@/api'
import type { EmbyLibraryItem } from '@/api/plugins'
import { toastError, useFeedback } from '@/composables/useFeedback'

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

// ============ 一键创建 Emby 媒体库 ============
const libShow = ref(false)
const libItems = ref<EmbyLibraryItem[]>([])
const libError = ref('')
const libScanning = ref(false)
const libCreating = ref(false)

const pending = computed(() => libItems.value.filter((x) => !x.exists))
const existing = computed(() => libItems.value.length - pending.value.length)

const libColumns: DataTableColumns<EmbyLibraryItem> = [
  { title: '库名', key: 'name', width: 150, ellipsis: { tooltip: true } },
  { title: '类型', key: 'type_label', width: 80 },
  { title: 'Emby 路径', key: 'emby_path', ellipsis: { tooltip: true } },
  {
    title: '状态',
    key: 'exists',
    width: 90,
    render: (row) =>
      h(NTag, { size: 'small', bordered: false, type: row.exists ? 'warning' : 'success' }, () =>
        row.exists ? '已存在' : '将创建',
      ),
  },
]

async function libScan() {
  libScanning.value = true
  libError.value = ''
  try {
    const d = await pluginsApi.embyLibraries()
    if (!d.emby_configured) {
      libError.value = '未配置 Emby 服务器，请先在「系统配置 → EMBY 管理」填写地址与 API 密钥'
      libItems.value = []
      return false
    }
    libItems.value = d.data ?? []
    if (!libItems.value.length) {
      libError.value = '未在媒体库根目录下发现分类目录（需要 根/分类/子目录 或 根/分类 结构）'
      return false
    }
    return true
  } catch (e) {
    libError.value = e instanceof Error ? e.message : '扫描失败'
    return false
  } finally {
    libScanning.value = false
  }
}

async function libOpen() {
  libShow.value = true
  await libScan()
}

async function libCreate() {
  libCreating.value = true
  try {
    const d = await pluginsApi.createEmbyLibraries()
    message.success(d.message || `已创建 ${d.created ?? pending.value.length} 个媒体库`)
    results.value.embylib = { status: 'ok', title: d.message || '创建完成' }
    await libScan()
  } catch (e) {
    toastError(e, '创建失败')
    results.value.embylib = { status: 'err', title: '创建失败', detail: e instanceof Error ? e.message : '' }
  } finally {
    libCreating.value = false
  }
}

async function libRun() {
  busy.value.embylib = true
  results.value.embylib = { status: 'pending', title: '扫描中…' }
  try {
    if (!(await libScan())) {
      results.value.embylib = { status: 'err', title: libError.value }
      return
    }
    if (!pending.value.length) {
      results.value.embylib = { status: 'ok', title: '没有需要创建的媒体库，全部已存在' }
      return
    }
    libShow.value = true // 有待创建的就打开弹窗让用户确认清单，不直接建
  } finally {
    busy.value.embylib = false
  }
}

// ============ 媒体库封面 ============
const cgShow = ref(false)
const cgSaving = ref(false)
const cgForm = ref({ cron: '0 0 * * *', style: '1', strategy: 'added', blacklist: '', advanced: '' })
const covers = ref<{ name: string; time?: string }[]>([])

const CG_STYLES = [
  { v: '1', label: '样式一', desc: '彩色底 + 斜排海报 + 库名' },
  { v: '2', label: '样式二', desc: '深色横幅 + 底部海报排' },
  { v: '3', label: '样式三', desc: '大字库名 + 右侧大图' },
  { v: 'random', label: '随机', desc: '每个库按名称随机样式' },
]

const CG_STRATEGIES = [
  { label: '按加入日期排序，选最新的 9 个', value: 'added' },
  { label: '按发行日期排序，选最新的 9 个', value: 'release' },
  { label: '按标题排序，选前面的 9 个', value: 'title' },
  { label: '按评分排序，选最高的 9 个', value: 'rating' },
]

async function cgOpen() {
  cgShow.value = true
  try {
    const d = await pluginsApi.coverGenConfig()
    const c = d.data ?? {}
    cgForm.value = {
      cron: c.cron || '0 0 * * *',
      style: c.style || '1',
      strategy: c.strategy || 'added',
      blacklist: c.blacklist || '',
      advanced: c.advanced || '',
    }
  } catch {
    // 首次使用尚无配置
  }
  cgLoadList()
}

async function cgLoadList() {
  try {
    covers.value = (await pluginsApi.coverGenList()).data ?? []
  } catch {
    covers.value = []
  }
}

async function cgSave() {
  cgSaving.value = true
  try {
    await pluginsApi.saveCoverGen({ ...cgForm.value, cron: cgForm.value.cron.trim() || '0 0 * * *' })
    message.success('配置已保存')
    cgShow.value = false
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    cgSaving.value = false
  }
}

async function cgRun() {
  busy.value.covergen = true
  results.value.covergen = { status: 'pending', title: '生成中…' }
  try {
    const d = await pluginsApi.runCoverGen()
    results.value.covergen = { status: 'ok', title: d.message || '生成已开始' }
    // 后端异步生成，等一会儿再刷列表才能看到新图
    setTimeout(cgLoadList, 8000)
  } catch (e) {
    results.value.covergen = { status: 'err', title: '生成失败', detail: e instanceof Error ? e.message : '' }
  } finally {
    busy.value.covergen = false
  }
}

const previewUrl = pluginsApi.coverPreviewUrl

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
    key: 'embylib',
    name: '一键创建 Emby 媒体库',
    icon: LibraryBig,
    desc: '把媒体库分类目录下的每个二级子目录一键建成同名 Emby 媒体库，已存在的自动跳过。',
    available: true,
    runLabel: '立即运行',
    onConfig: libOpen,
    onRun: libRun,
  },
  {
    key: 'covergen',
    name: '媒体库海报',
    icon: Images,
    desc: '按分类聚合入库海报，合成带库名的封面图并推送 Emby 媒体库。支持定时与多种样式。',
    available: true,
    runLabel: '立即生成',
    onConfig: cgOpen,
    onRun: cgRun,
  },
  {
    key: 'clear115',
    name: '115 文件夹清空',
    icon: Trash2,
    desc: '一键清空 115 指定文件夹，配合分享同步使用。',
    available: false,
    runLabel: '立即运行',
    onConfig: () => {},
    onRun: () => {},
  },
]

const availableCount = plugins.filter((p) => p.available).length
</script>

<template>
  <SectionCard title="插件扩展" :hint="`共 ${plugins.length} 个插件 · ${availableCount} 个可用`">
    <div class="grid">
      <div v-for="p in plugins" :key="p.key" class="card" :class="{ off: !p.available }">
        <div class="head">
          <div class="ico"><component :is="p.icon" :size="17" :stroke-width="1.8" /></div>
          <div class="name">{{ p.name }}</div>
          <NTag size="small" :bordered="false" :type="p.available ? 'success' : 'default'">
            {{ p.available ? '可用' : '规划中' }}
          </NTag>
        </div>

        <p class="desc">{{ p.desc }}</p>

        <TestBanner :state="results[p.key]" />

        <div class="foot">
          <NButton size="small" :disabled="!p.available" @click="p.onConfig()">
            <template #icon><Settings2 :size="14" /></template>
            配置规则
          </NButton>
          <NButton
            size="small"
            type="primary"
            ghost
            :disabled="!p.available"
            :loading="busy[p.key]"
            @click="p.onRun()"
          >
            <template #icon><Play :size="14" /></template>
            {{ p.runLabel }}
          </NButton>
        </div>
      </div>
    </div>

    <!-- 115 签到配置 -->
    <NModal v-model:show="ckShow" preset="card" title="115 每日签到" style="width: 480px">
      <FieldRow
        label="自动签到"
        tip="开启后每天自动签到领积分（连续签到有加成，积分可在 115 App 积分中心使用）。"
      >
        <NRadioGroup v-model:value="ckForm.enabled">
          <NRadioButton :value="true">开启</NRadioButton>
          <NRadioButton :value="false">关闭</NRadioButton>
        </NRadioGroup>
      </FieldRow>
      <FieldRow
        label="执行计划"
        tip="例：0 8 * * * = 每天 08:00；30 7 * * 1-5 = 工作日 07:30。失败自动重试 3 次，结果推送通知。"
      >
        <CronField v-model="ckForm.cron" placeholder="0 8 * * *" />
      </FieldRow>
      <template #footer>
        <div class="foot-right">
          <NButton @click="ckShow = false">取消</NButton>
          <NButton type="primary" :loading="ckSaving" @click="ckSave">保存</NButton>
        </div>
      </template>
    </NModal>

    <!-- Emby 媒体库创建 -->
    <NModal v-model:show="libShow" preset="card" title="一键创建 Emby 媒体库" style="width: 720px">
      <p class="modal-desc">
        扫描媒体库根目录下的分类子目录，<b>库名 = 子目录名</b>，类型按目录名推断（电影 / 剧集 / 音乐 /
        混合）。创建时自动应用：媒体文件夹路径（含 EMBY 管理卡的路径映射）、中文元数据（zh-CN）、NFO
        与图片保存到媒体文件夹。
      </p>

      <p v-if="libError" class="err">{{ libError }}</p>
      <NDataTable
        v-else
        :columns="libColumns"
        :data="libItems"
        :loading="libScanning"
        size="small"
        :max-height="340"
        :bordered="false"
      />

      <template #footer>
        <div class="foot-split">
          <span class="muted">{{ existing ? `${existing} 个已存在将跳过` : '' }}</span>
          <NButton :loading="libScanning" @click="libScan">重新扫描</NButton>
          <NButton @click="libShow = false">关闭</NButton>
          <NPopconfirm :disabled="!pending.length" @positive-click="void libCreate()">
            <template #trigger>
              <NButton type="primary" :disabled="!pending.length" :loading="libCreating">
                创建 {{ pending.length }} 个库
              </NButton>
            </template>
            将创建 {{ pending.length }} 个 Emby 媒体库（{{ pending.map((x) => x.name).join('、') }}），确认执行？
          </NPopconfirm>
        </div>
      </template>
    </NModal>

    <!-- 封面生成配置 -->
    <NModal v-model:show="cgShow" preset="card" title="媒体库封面生成配置" style="width: 640px">
      <FieldRow label="定时执行" tip="例：0 0 * * * = 每天 0 点。到点自动重新生成全部封面并推送 Emby。">
        <CronField v-model="cgForm.cron" placeholder="0 0 * * *" />
      </FieldRow>

      <FieldRow label="封面样式" wide>
        <div class="styles">
          <button
            v-for="s in CG_STYLES"
            :key="s.v"
            class="style"
            :class="{ on: cgForm.style === s.v }"
            @click="cgForm.style = s.v"
          >
            <span class="radio" />
            <span class="style-body">
              <span class="style-name">{{ s.label }}</span>
              <span class="style-desc">{{ s.desc }}</span>
            </span>
          </button>
        </div>
      </FieldRow>

      <FieldRow label="海报选取策略">
        <NSelect v-model:value="cgForm.strategy" :options="CG_STRATEGIES" />
      </FieldRow>

      <FieldRow label="媒体库黑名单" hint="一行一个分类名，写在这里的媒体库不会生成封面">
        <NInput v-model:value="cgForm.blacklist" type="textarea" :rows="2" />
      </FieldRow>

      <FieldRow label="高级配置" hint="预留：后续支持自定义背景色 / 尺寸等参数（JSON 格式）">
        <NInput v-model:value="cgForm.advanced" type="textarea" :rows="2" />
      </FieldRow>

      <div class="covers">
        <div class="covers-head">
          <span>已生成的封面</span>
          <NButton size="tiny" @click="cgLoadList">刷新</NButton>
        </div>
        <div v-if="covers.length" class="covers-grid">
          <figure v-for="c in covers" :key="c.name" class="cover">
            <img :src="previewUrl(c.name)" loading="lazy" :alt="c.name" />
            <figcaption>{{ c.name }}<span>{{ c.time }}</span></figcaption>
          </figure>
        </div>
        <p v-else class="muted small">还没有生成过封面，点「立即生成」试试</p>
      </div>

      <template #footer>
        <div class="foot-right">
          <NButton @click="cgShow = false">取消</NButton>
          <NButton type="primary" :loading="cgSaving" @click="cgSave">保存</NButton>
        </div>
      </template>
    </NModal>
  </SectionCard>
</template>

<style scoped>
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(290px, 1fr));
  gap: 14px;
}

.card {
  display: flex;
  flex-direction: column;
  padding: 16px;
  border: 1px solid var(--c-border);
  border-radius: var(--radius-lg);
  background: var(--c-bg-raised);
  transition: border-color 0.15s, box-shadow 0.15s;
}
.card:not(.off):hover {
  border-color: var(--c-border-strong);
  box-shadow: var(--shadow-sm);
}
.card.off {
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
  border-radius: var(--radius-sm);
  display: grid;
  place-items: center;
  background: var(--c-primary-soft);
  color: var(--c-primary);
}
.card.off .ico {
  background: var(--c-bg-hover);
  color: var(--c-text-4);
}
.name {
  flex: 1;
  min-width: 0;
  font-size: 13.5px;
  font-weight: 600;
  color: var(--c-text-1);
}

.desc {
  flex: 1;
  margin: 0 0 12px;
  font-size: 12.5px;
  line-height: 1.7;
  color: var(--c-text-3);
}

.foot {
  display: flex;
  gap: 8px;
  margin-top: 10px;
}

.modal-desc {
  margin: 0 0 12px;
  font-size: 12.5px;
  line-height: 1.7;
  color: var(--c-text-2);
}
.err {
  padding: 18px 0;
  text-align: center;
  color: var(--c-danger);
  font-size: 13px;
}
.muted {
  color: var(--c-text-3);
  font-size: 12px;
}
.small {
  font-size: 12px;
}

.foot-right {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
.foot-split {
  display: flex;
  align-items: center;
  gap: 8px;
}
.foot-split .muted {
  flex: 1;
}

.styles {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 10px;
}
.style {
  all: unset;
  display: flex;
  align-items: flex-start;
  gap: 9px;
  padding: 10px 12px;
  border: 1px solid var(--c-border);
  border-radius: var(--radius);
  cursor: pointer;
  transition: border-color 0.15s, background-color 0.15s;
}
.style:hover {
  border-color: var(--c-border-strong);
}
.style.on {
  border-color: var(--c-primary);
  background: var(--c-primary-soft);
}
.radio {
  width: 14px;
  height: 14px;
  margin-top: 2px;
  flex-shrink: 0;
  border-radius: 50%;
  border: 1.5px solid var(--c-border-strong);
  transition: border-color 0.15s, box-shadow 0.15s;
}
.style.on .radio {
  border-color: var(--c-primary);
  border-width: 4px;
}
.style-body {
  display: flex;
  flex-direction: column;
}
.style-name {
  font-size: 13px;
  color: var(--c-text-1);
}
.style-desc {
  font-size: 11.5px;
  color: var(--c-text-3);
}

.covers {
  margin-top: 16px;
  padding-top: 12px;
  border-top: 1px solid var(--c-border);
}
.covers-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 9px;
  font-size: 13px;
  font-weight: 600;
  color: var(--c-text-1);
}
.covers-grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 8px;
  max-height: 260px;
  overflow: auto;
}
.cover {
  margin: 0;
  border: 1px solid var(--c-border);
  border-radius: var(--radius);
  overflow: hidden;
}
.cover img {
  width: 100%;
  display: block;
}
.cover figcaption {
  display: flex;
  justify-content: space-between;
  gap: 6px;
  padding: 4px 8px;
  font-size: 11px;
  color: var(--c-text-3);
}

@media (max-width: 560px) {
  .styles,
  .covers-grid {
    grid-template-columns: 1fr;
  }
}
</style>
