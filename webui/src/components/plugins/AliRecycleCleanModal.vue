<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HModal from '@/components/hero/HModal.vue'
import HTabs from '@/components/hero/HTabs.vue'
import PluginLogPanel from './PluginLogPanel.vue'
import { usePluginLogTab } from './logTab'
import HAlert from '@/components/hero/HAlert.vue'
import HChip from '@/components/hero/HChip.vue'
import HNumberInput from '@/components/hero/HNumberInput.vue'
import HSelect from '@/components/hero/HSelect.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import { heroTone } from '@/components/hero/tone'
import FieldRow from '@/components/ui/FieldRow.vue'
import SchedulePicker from '@/components/ui/SchedulePicker.vue'
import TestBanner, { type BannerState } from '@/components/ui/TestBanner.vue'
import { pluginsApi } from '@/api'
import { toastError, useFeedback } from '@/composables/useFeedback'

const show = defineModel<boolean>('show', { default: false })
const { tab, logTabs } = usePluginLogTab(show)
const { message } = useFeedback()

const DEFAULT_CRON = '40 4 * * *'
const PRESETS = [0, 7, 30]
const PRESET_OPTS = [
  { label: '整个清空', value: '0' },
  { label: '保留 7 天', value: '7' },
  { label: '保留 30 天', value: '30' },
  { label: '自定义', value: 'custom' },
]

interface Row {
  id: number
  name: string
  lastErr: string
  on: boolean
  days: number
  preset: string
}

const enabled = ref(false)
const cron = ref(DEFAULT_CRON)
const rows = ref<Row[]>([])
const loading = ref(false)
const saving = ref(false)
const preview = ref<BannerState | null>(null)
const previewing = ref(false)

const onCount = computed(() => rows.value.filter((r) => r.on).length)

watch(show, async (v) => {
  if (!v) return
  preview.value = null
  loading.value = true
  try {
    const d = await pluginsApi.aliRbCleanConfig()
    enabled.value = !!d.enabled
    cron.value = d.cron || DEFAULT_CRON
    rows.value = (d.accounts ?? []).map((a) => {
      const days = a.keep_days || 0
      return {
        id: a.id,
        name: a.name,
        lastErr: a.last_err || '',
        on: a.on,
        days,
        preset: PRESETS.includes(days) ? String(days) : 'custom',
      }
    })
  } catch (e) {
    toastError(e, '读取配置失败')
  } finally {
    loading.value = false
  }
})

function setPreset(r: Row, v: string | null) {
  r.preset = v ?? '0'
  if (r.preset === 'custom') {
    if (r.days < 1) r.days = 14
  } else r.days = Number(r.preset)
}

async function runPreview() {
  const pick = rows.value.filter((r) => onCount.value === 0 || r.on)
  if (!pick.length) {
    preview.value = { status: 'err', title: '还没登录阿里云盘' }
    return
  }
  previewing.value = true
  preview.value = { status: 'pending', title: '读回收站中…' }
  try {
    const d = await pluginsApi.previewAliRbClean(pick.map((r) => ({ id: r.id, keep_days: r.days })))
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
    await pluginsApi.saveAliRbClean({
      enabled: enabled.value,
      cron: cron.value.trim() || DEFAULT_CRON,
      accounts: rows.value.map((r) => ({ id: r.id, on: r.on, keep_days: r.days })),
    })
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
  <HModal v-model:show="show" title="清理阿里云盘回收站" width="560px">
    <HTabs v-model="tab" :items="logTabs" variant="secondary" class="plog-tabs" />
    <template v-if="tab === 'set'">
    <HAlert v-if="!rows.length && !loading" status="warning" class="warn">
      还没登录阿里云盘：先到「阿里云盘秒传」的配置里扫码登录，这里共用那个账号。
    </HAlert>
    <HAlert v-else status="warning" class="warn">
      回收站里的内容会被<b>彻底删除，无法还原</b>。资源盘和备份盘的回收站都清。
    </HAlert>

    <FieldRow label="执行时间" wide tip="所有打开的账号在同一时间依次清理，有删除或出错时推送一条汇总通知。">
      <SchedulePicker v-model="cron" v-model:enabled="enabled" toggle :placeholder="DEFAULT_CRON" />
    </FieldRow>

    <FieldRow
      label="账号"
      wide
      tip="每个账号单独设清不清、清多少：「整个清空」删掉回收站里的全部内容；保留 N 天按每条进回收站的时间判断，读不出时间的不删。新登录的账号默认不清。"
    >
      <div class="accts">
        <div v-for="r in rows" :key="r.id" class="acct" :class="{ off: !r.on }">
          <div class="acct-head">
            <HSwitch v-model="r.on" size="sm" :aria-label="`清理 ${r.name} 的回收站`" />
            <span class="acct-name">{{ r.name }}</span>
            <HChip v-if="r.lastErr" :color="heroTone('warning')" :title="r.lastErr">登录有问题</HChip>
          </div>
          <div v-if="r.on" class="acct-body">
            <HSelect
              :model-value="r.preset"
              :options="PRESET_OPTS"
              :aria-label="`${r.name} 清理范围`"
              class="sel"
              @update:model-value="(v) => setPreset(r, v as string | null)"
            />
            <HNumberInput
              v-if="r.preset === 'custom'"
              v-model="r.days"
              :min="1"
              :max="3650"
              :aria-label="`${r.name} 自定义保留天数`"
              class="num"
            >
              <template #suffix>天</template>
            </HNumberInput>
          </div>
        </div>
      </div>
    </FieldRow>

    <TestBanner :state="preview" />

    </template>
    <PluginLogPanel v-else plugin-key="alirbclean" />

    <template v-if="tab === 'set'" #footer>
      <div class="foot">
        <HButton variant="tertiary" :loading="previewing" :disabled="!rows.length" @click="runPreview">
          预览会删多少
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
.accts {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.acct {
  padding: 10px 12px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.acct-head {
  display: flex;
  align-items: center;
  gap: 8px;
}
.acct-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13px;
  font-weight: 600;
  color: var(--foreground);
}
.acct.off .acct-name {
  color: var(--muted);
}
.acct-body {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-top: 10px;
}
.sel {
  width: 130px;
}
.num {
  width: 140px;
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
</style>
