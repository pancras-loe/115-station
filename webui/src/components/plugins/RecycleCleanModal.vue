<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HModal from '@/components/hero/HModal.vue'
import HAlert from '@/components/hero/HAlert.vue'
import HChip from '@/components/hero/HChip.vue'
import HNumberInput from '@/components/hero/HNumberInput.vue'
import HSelect from '@/components/hero/HSelect.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import { heroTone } from '@/components/hero/tone'
import FieldRow from '@/components/ui/FieldRow.vue'
import SchedulePicker from '@/components/ui/SchedulePicker.vue'
import SecretInput from '@/components/ui/SecretInput.vue'
import TestBanner, { type BannerState } from '@/components/ui/TestBanner.vue'
import { configApi, pluginsApi } from '@/api'
import type { RbCleanAcctReq } from '@/api/plugins'
import { toastError, useFeedback } from '@/composables/useFeedback'

const show = defineModel<boolean>('show', { default: false })
const { message } = useFeedback()

const DEFAULT_CRON = '30 4 * * *'
const PRESETS = [7, 30, 90]
const PRESET_OPTS = [
  { label: '保留 7 天', value: '7' },
  { label: '保留 30 天', value: '30' },
  { label: '保留 90 天', value: '90' },
  { label: '自定义', value: 'custom' },
]

interface Row {
  id: number
  name: string
  main: boolean
  loggedIn: boolean
  on: boolean
  days: number
  preset: string
  hasPassword: boolean
  /** 已保存的回显掩码 SECRET_MASK，点眼睛按需取明文 */
  password: string
  /** 动过密钥框才提交 password（不带 = 后端不改） */
  pwTouched: boolean
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
    const d = await pluginsApi.rbCleanConfig()
    enabled.value = !!d.enabled
    cron.value = d.cron || DEFAULT_CRON
    rows.value = (d.accounts ?? []).map((a) => {
      const days = a.keep_days || 30
      return {
        id: a.id,
        name: a.name,
        main: a.main,
        loggedIn: a.logged_in,
        on: a.on,
        days,
        preset: PRESETS.includes(days) ? String(days) : 'custom',
        hasPassword: a.has_password,
        password: a.has_password ? configApi.SECRET_MASK : '',
        pwTouched: false,
      }
    })
  } catch (e) {
    toastError(e, '读取配置失败')
  } finally {
    loading.value = false
  }
})

function setPreset(r: Row, v: string | null) {
  r.preset = v ?? '30'
  if (r.preset !== 'custom') r.days = Number(r.preset)
}

function setPassword(r: Row, v: string) {
  r.password = v
  r.pwTouched = true
}

async function runPreview() {
  // 开着的账号预览开着的；一个都没开时看所有登录了的，打开之前先看一眼
  const pick = rows.value.filter((r) => r.loggedIn && (onCount.value === 0 || r.on))
  if (!pick.length) {
    preview.value = { status: 'err', title: '没有已登录的 115 账号' }
    return
  }
  previewing.value = true
  preview.value = { status: 'pending', title: '读回收站中…（条目多时要翻好几页）' }
  try {
    const d = await pluginsApi.previewRbClean(pick.map((r) => ({ id: r.id, keep_days: r.days || 30 })))
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
    const accounts: RbCleanAcctReq[] = rows.value.map((r) => {
      const a: RbCleanAcctReq = { id: r.id, on: r.on, keep_days: r.days || 30 }
      if (r.pwTouched && r.password !== configApi.SECRET_MASK) a.password = r.password.trim()
      return a
    })
    await pluginsApi.saveRbClean({ enabled: enabled.value, cron: cron.value.trim() || DEFAULT_CRON, accounts })
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
  <HModal v-model:show="show" title="清理 115 回收站" width="600px">
    <HAlert status="warning" class="warn">
      早于保留期的回收站内容会被<b>彻底删除，无法还原</b>。本站的空目录清理、深度删除、洗版让位都是删进回收站，保留期内还能找回。
    </HAlert>

    <FieldRow label="执行时间" wide tip="所有打开的账号在同一时间依次清理，有删除或出错时推送一条汇总通知。">
      <SchedulePicker v-model="cron" v-model:enabled="enabled" toggle :placeholder="DEFAULT_CRON" />
    </FieldRow>

    <FieldRow
      label="账号"
      wide
      tip="每个账号单独设：清不清、保留最近几天进回收站的内容（按每条的删除时间判断）、安全密钥。安全密钥在 115「账号安全 → 安全密钥」开着「文件(隐藏模式/清空删除回收站)」时必填，6 位数字；没开可以不填。新加的账号默认不清。"
    >
      <div v-if="!rows.length && !loading" class="empty">还没登录 115 账号</div>
      <div class="accts">
        <div v-for="r in rows" :key="r.id" class="acct" :class="{ off: !r.on }">
          <div class="acct-head">
            <HSwitch v-model="r.on" size="sm" :aria-label="`清理 ${r.name} 的回收站`" />
            <span class="acct-name">{{ r.name }}</span>
            <HChip v-if="r.main" :color="heroTone('accent')">主号</HChip>
            <HChip v-if="!r.loggedIn" :color="heroTone('warning')">未登录</HChip>
          </div>
          <div v-if="r.on" class="acct-body">
            <HSelect
              :model-value="r.preset"
              :options="PRESET_OPTS"
              :aria-label="`${r.name} 保留天数`"
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
            <SecretInput
              :model-value="r.password"
              :name="`rbclean-secret-${r.id}`"
              :reveal="r.hasPassword ? { key: 'rbclean', field: `accounts.${r.id}.password` } : undefined"
              placeholder="安全密钥（没开可不填）"
              class="pw"
              @update:model-value="(v) => setPassword(r, v)"
            />
          </div>
        </div>
      </div>
    </FieldRow>

    <TestBanner :state="preview" />

    <template #footer>
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
.warn {
  margin-bottom: 14px;
}
.empty {
  font-size: 12.5px;
  color: var(--muted);
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
.pw {
  flex: 1;
  min-width: 200px;
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
