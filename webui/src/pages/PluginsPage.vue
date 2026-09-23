<script setup lang="ts">
import { computed, h, ref } from 'vue'
import {
  NButton,
  NDataTable,
  NModal,
  NPopconfirm,
  NRadioButton,
  NRadioGroup,
  NTag,
  type DataTableColumns,
} from 'naive-ui'
import { CalendarCheck, Images, LibraryBig, Play, Settings2, Trash2 } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import CronField from '@/components/ui/CronField.vue'
import TestBanner, { type BannerState } from '@/components/ui/TestBanner.vue'
import CoverGenModal from '@/components/plugins/CoverGenModal.vue'
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
    desc: '从 Emby 媒体库选取海报，合成带库名的封面并推送。未配置 Emby 时使用本地整理台账。支持定时与多种样式。',
    available: true,
    runLabel: '立即生成',
    onConfig: () => (cgShow.value = true),
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

    <CoverGenModal ref="cgModal" v-model:show="cgShow" />
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

</style>
