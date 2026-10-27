<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Plus, X } from '@lucide/vue'
import HButton from '@/components/hero/HButton.vue'
import HModal from '@/components/hero/HModal.vue'
import HTabs from '@/components/hero/HTabs.vue'
import PluginLogPanel from './PluginLogPanel.vue'
import { usePluginLogTab } from './logTab'
import HAlert from '@/components/hero/HAlert.vue'
import HSelect from '@/components/hero/HSelect.vue'
import Cid115Input, { type CidValue } from '@/components/Cid115Input.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import SchedulePicker from '@/components/ui/SchedulePicker.vue'
import TestBanner, { type BannerState } from '@/components/ui/TestBanner.vue'
import { pluginsApi } from '@/api'
import type { DirCleanAcct, DirCleanDir } from '@/api/plugins'
import { toastError, useFeedback } from '@/composables/useFeedback'

const show = defineModel<boolean>('show', { default: false })
const { tab, logTabs } = usePluginLogTab(show)
const { message } = useFeedback()

const DEFAULT_CRON = '0 5 * * *'
const MAX_DIRS = 20

interface Row {
  key: number
  account: number
  value: CidValue
}

let seq = 0
const enabled = ref(false)
const cron = ref(DEFAULT_CRON)
const rows = ref<Row[]>([])
const accounts = ref<DirCleanAcct[]>([])
const loading = ref(false)
const saving = ref(false)
const preview = ref<BannerState | null>(null)
const previewing = ref(false)
const inputs = ref<Record<number, InstanceType<typeof Cid115Input> | null>>({})

/** 只有一个账号时不显示账号下拉 */
const multiAcct = computed(() => accounts.value.length > 1)
const acctOpts = computed(() =>
  accounts.value.map((a) => ({ label: a.name + (a.logged_in ? '' : '（未登录）'), value: String(a.id) })),
)

watch(show, async (v) => {
  if (!v) return
  preview.value = null
  loading.value = true
  try {
    const d = await pluginsApi.dirCleanConfig()
    enabled.value = !!d.enabled
    cron.value = d.cron || DEFAULT_CRON
    accounts.value = d.accounts ?? []
    rows.value = (d.dirs ?? []).map((x) => ({ key: ++seq, account: x.account, value: { cid: x.cid, path: x.path || x.cid } }))
  } catch (e) {
    toastError(e, '读取配置失败')
  } finally {
    loading.value = false
  }
})

function addRow() {
  rows.value.push({ key: ++seq, account: 0, value: { cid: '', path: '' } })
}

function removeRow(r: Row) {
  rows.value = rows.value.filter((x) => x !== r)
}

function setAccount(r: Row, v: string | null) {
  const n = Number(v ?? 0)
  if (n === r.account) return
  r.account = n
  r.value = { cid: '', path: '' } // 换了账号，原来选的目录不在这个账号上
}

/** 收集可信的 cid：输入框防抖还没解析的主动解析一次；空行跳过 */
async function collect(): Promise<DirCleanDir[] | null> {
  const out: DirCleanDir[] = []
  for (const r of rows.value) {
    if (!r.value.path.trim() && !r.value.cid) continue
    const cid = (await inputs.value[r.key]?.ensureCid()) || r.value.cid
    if (!cid) {
      message.error(`目录「${r.value.path}」没找到，重新选一次`)
      return null
    }
    out.push({ account: r.account, cid, path: /^\d+$/.test(r.value.path.trim()) ? '' : r.value.path.trim() })
  }
  return out
}

async function runPreview() {
  const dirs = await collect()
  if (!dirs) return
  if (!dirs.length) {
    preview.value = { status: 'err', title: '还没添加目录' }
    return
  }
  previewing.value = true
  preview.value = { status: 'pending', title: '核对位置并列目录中…' }
  try {
    const d = await pluginsApi.previewDirClean(dirs)
    const errs = (d.data ?? []).some((a) => a.error)
    preview.value = { status: errs ? 'err' : 'ok', title: '预览（没有删除任何东西）', detail: d.message }
  } catch (e) {
    preview.value = { status: 'err', title: '预览失败', detail: e instanceof Error ? e.message : '' }
  } finally {
    previewing.value = false
  }
}

async function save() {
  const dirs = await collect()
  if (!dirs) return
  saving.value = true
  try {
    await pluginsApi.saveDirClean({ enabled: enabled.value, cron: cron.value.trim() || DEFAULT_CRON, dirs })
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
  <HModal v-model:show="show" title="定时清空 115 目录" width="620px">
    <HTabs v-model="tab" :items="logTabs" variant="secondary" class="plog-tabs" />
    <template v-if="tab === 'set'">
    <HAlert status="warning" class="warn">
      到点把这些目录里的<b>全部内容</b>删进 115 回收站，目录本身留着。回收站里还能还原；开着「清理 115 回收站」的话，过了保留天数就彻底删除。
    </HAlert>

    <FieldRow label="执行时间" wide tip="所有目录在同一时间依次清空，有删除或出错时推送一条汇总通知。">
      <SchedulePicker v-model="cron" v-model:enabled="enabled" toggle :placeholder="DEFAULT_CRON" />
    </FieldRow>

    <FieldRow
      label="目录"
      wide
      :tip="`适合「云下载」「最近接收」、已存在 / 冗余这类只进不出的目录，最多 ${MAX_DIRS} 个。媒体库目录、转存/待整理文件夹，以及它们的上级目录不能选（保存时会核对）；网盘根不能选。`"
    >
      <div v-if="!rows.length && !loading" class="empty">还没添加目录</div>
      <div class="dirs">
        <div v-for="r in rows" :key="r.key" class="dir">
          <HSelect
            v-if="multiAcct"
            :model-value="String(r.account)"
            :options="acctOpts"
            aria-label="账号"
            class="acct"
            @update:model-value="(v) => setAccount(r, v as string | null)"
          />
          <Cid115Input
            :ref="(el) => (inputs[r.key] = el as InstanceType<typeof Cid115Input> | null)"
            v-model="r.value"
            :account="r.account || undefined"
            placeholder="选择要清空的目录"
            class="pick"
          />
          <HButton variant="tertiary" size="sm" aria-label="移除这个目录" class="rm" @click="removeRow(r)">
            <template #icon><X :size="14" /></template>
          </HButton>
        </div>
      </div>
      <HButton variant="tertiary" size="sm" class="add" :disabled="rows.length >= MAX_DIRS" @click="addRow">
        <template #icon><Plus :size="14" /></template>
        添加目录
      </HButton>
    </FieldRow>

    <TestBanner :state="preview" />

    </template>
    <PluginLogPanel v-else plugin-key="dirclean" />

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
