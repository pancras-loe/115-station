<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Plus, X } from '@lucide/vue'
import HButton from '@/components/hero/HButton.vue'
import HModal from '@/components/hero/HModal.vue'
import HTabs from '@/components/hero/HTabs.vue'
import PluginLogPanel from './PluginLogPanel.vue'
import { usePluginLogTab } from './logTab'
import HAlert from '@/components/hero/HAlert.vue'
import HInput from '@/components/hero/HInput.vue'
import HSelect from '@/components/hero/HSelect.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import SchedulePicker from '@/components/ui/SchedulePicker.vue'
import TestBanner, { type BannerState } from '@/components/ui/TestBanner.vue'
import { pluginsApi } from '@/api'
import type { AliCleanAcct, AliDirCleanDir } from '@/api/plugins'
import { toastError, useFeedback } from '@/composables/useFeedback'

const show = defineModel<boolean>('show', { default: false })
const { tab, logTabs } = usePluginLogTab(show)
const { message } = useFeedback()

const DEFAULT_CRON = '10 5 * * *'
const MAX_DIRS = 20
const DRIVE_OPTS = [
  { label: '资源盘', value: 'resource' },
  { label: '备份盘', value: 'backup' },
]

interface Row {
  key: number
  account: number
  drive: 'resource' | 'backup'
  path: string
}

let seq = 0
const enabled = ref(false)
const cron = ref(DEFAULT_CRON)
const rows = ref<Row[]>([])
const accounts = ref<AliCleanAcct[]>([])
const loading = ref(false)
const saving = ref(false)
const preview = ref<BannerState | null>(null)
const previewing = ref(false)

const multiAcct = computed(() => accounts.value.length > 1)
const acctOpts = computed(() => accounts.value.map((a) => ({ label: a.name, value: String(a.id) })))

watch(show, async (v) => {
  if (!v) return
  preview.value = null
  loading.value = true
  try {
    const d = await pluginsApi.aliDirCleanConfig()
    enabled.value = !!d.enabled
    cron.value = d.cron || DEFAULT_CRON
    accounts.value = d.accounts ?? []
    rows.value = (d.dirs ?? []).map((x) => ({ key: ++seq, account: x.account, drive: x.drive || 'resource', path: x.path }))
  } catch (e) {
    toastError(e, '读取配置失败')
  } finally {
    loading.value = false
  }
})

function addRow() {
  rows.value.push({ key: ++seq, account: accounts.value[0]?.id ?? 0, drive: 'resource', path: '' })
}

function removeRow(r: Row) {
  rows.value = rows.value.filter((x) => x !== r)
}

function collect(): AliDirCleanDir[] {
  return rows.value
    .filter((r) => r.path.trim())
    .map((r) => ({ account: r.account, drive: r.drive, path: r.path.trim() }))
}

async function runPreview() {
  const dirs = collect()
  if (!dirs.length) {
    preview.value = { status: 'err', title: '还没填目录' }
    return
  }
  previewing.value = true
  preview.value = { status: 'pending', title: '查目录并列内容中…' }
  try {
    const d = await pluginsApi.previewAliDirClean(dirs)
    const errs = (d.data ?? []).some((a) => a.error)
    preview.value = { status: errs ? 'err' : 'ok', title: '预览（没有删除任何东西）', detail: d.message }
  } catch (e) {
    preview.value = { status: 'err', title: '预览失败', detail: e instanceof Error ? e.message : '' }
  } finally {
    previewing.value = false
  }
}

async function save() {
  saving.value = true
  try {
    await pluginsApi.saveAliDirClean({ enabled: enabled.value, cron: cron.value.trim() || DEFAULT_CRON, dirs: collect() })
    message.success('保存成功')
    show.value = false
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <HModal v-model:show="show" title="定时清空阿里云盘目录" width="640px">
    <HTabs v-model="tab" :items="logTabs" variant="secondary" class="plog-tabs" />
    <template v-if="tab === 'set'">
    <HAlert v-if="!accounts.length && !loading" status="warning" class="warn">
      还没登录阿里云盘：先到「阿里云盘秒传」的配置里扫码登录，这里共用那个账号。
    </HAlert>
    <HAlert v-else status="warning" class="warn">
      到点把这些目录里的<b>全部内容</b>删进阿里云盘回收站，目录本身留着。回收站里还能还原；开着「清理阿里云盘回收站」的话，过了保留天数就彻底删除。
    </HAlert>

    <FieldRow label="执行时间" wide tip="所有目录在同一时间依次清空，有删除或出错时推送一条汇总通知。">
      <SchedulePicker v-model="cron" v-model:enabled="enabled" toggle :placeholder="DEFAULT_CRON" />
    </FieldRow>

    <FieldRow
      label="目录"
      wide
      :tip="`填阿里云盘里的完整路径，如「/来自分享」，最多 ${MAX_DIRS} 个。保存时按路径查出目录记下来；之后目录改名、移动或删了重建，就不再清，要重新保存一次。网盘根和秒传用的临时目录不能选。没分资源盘的老账号，资源盘和备份盘是同一个。`"
    >
      <div v-if="!rows.length && !loading" class="empty">还没添加目录</div>
      <div class="dirs">
        <div v-for="r in rows" :key="r.key" class="dir">
          <HSelect
            v-if="multiAcct"
            :model-value="String(r.account)"
            :options="acctOpts"
            aria-label="阿里云盘账号"
            class="acct"
            @update:model-value="(v) => (r.account = Number(v ?? 0))"
          />
          <HSelect
            :model-value="r.drive"
            :options="DRIVE_OPTS"
            aria-label="资源盘或备份盘"
            class="drive"
            @update:model-value="(v) => (r.drive = (v as 'resource' | 'backup') || 'resource')"
          />
          <HInput v-model="r.path" placeholder="/来自分享" aria-label="目录路径" class="pick" />
          <HButton variant="tertiary" size="sm" aria-label="移除这个目录" class="rm" @click="removeRow(r)">
            <template #icon><X :size="14" /></template>
          </HButton>
        </div>
      </div>
      <HButton
        variant="tertiary"
        size="sm"
        class="add"
        :disabled="rows.length >= MAX_DIRS || !accounts.length"
        @click="addRow"
      >
        <template #icon><Plus :size="14" /></template>
        添加目录
      </HButton>
    </FieldRow>

    <TestBanner :state="preview" />

    </template>
    <PluginLogPanel v-else plugin-key="alidirclean" />

    <template v-if="tab === 'set'" #footer>
      <div class="foot">
        <HButton variant="tertiary" :loading="previewing" :disabled="!rows.length" @click="runPreview">
          预览会删什么
        </HButton>
        <span class="grow" />
        <HButton variant="tertiary" @click="show = false">取消</HButton>
        <HButton variant="primary" :loading="saving" @click="save">保存</HButton>
      </div>
    </template>
  </HModal>
</template>

<style scoped>
.plog-tabs {
  margin-bottom: 14px;
}
.warn {
  margin-bottom: 14px;
}
.empty {
  font-size: 12.5px;
  color: var(--muted);
}
.dirs {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.dir {
  display: flex;
  align-items: center;
  gap: 8px;
}
.acct {
  flex: none;
  width: 130px;
}
.drive {
  flex: none;
  width: 100px;
}
.pick {
  flex: 1;
  min-width: 0;
}
.rm {
  flex: none;
}
.add {
  margin-top: 8px;
}
.foot {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
}
.grow {
  flex: 1;
}
@media (max-width: 720px) {
  .dir {
    flex-wrap: wrap;
  }
  .acct {
    width: 100%;
  }
}
</style>
