<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import HTabs from '@/components/hero/HTabs.vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import Cid115Input from '@/components/Cid115Input.vue'
import { storageApi, transferApi } from '@/api'
import { useSetting } from '@/composables/useSetting'
import { useTabQuery } from '@/composables/useTabQuery'
import { useFullSetting } from '@/pages/strm/fullSetting'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { confirmUnsaved } from '@/composables/confirmUnsaved'

const { message } = useFeedback()
const router = useRouter()
const tab = useTabQuery('download')
const media = useFullSetting()

// ---- 转存目录 ----
const share = useSetting('share', { folder: '', folder_path: '' })
const shareCid = ref({ cid: '', path: '' })
const shareInput = ref<InstanceType<typeof Cid115Input> | null>(null)

/** 输入框里给人看的是路径，落库的是 cid；folder_path 只为显示而存 */
watch(
  () => [share.model.value.folder, share.model.value.folder_path] as const,
  async ([cid, savedPath]) => {
    if (!cid) {
      shareCid.value = { cid: '', path: '' }
      return
    }
    const displayPath = savedPath || cid
    if (cid !== shareCid.value.cid || displayPath !== shareCid.value.path) {
      shareCid.value = { cid, path: displayPath }
    }
    // 只存过 cid 的旧配置：进页面反查一次可读路径，别让用户对着数字猜目录。
    if (!savedPath) {
      try {
        const resolved = await storageApi.path115(cid)
        if (share.model.value.folder === cid && resolved.path) {
          shareCid.value = { cid, path: resolved.path }
        }
      } catch {
        // Cookie 暂不可用时保留 cid；重新选择目录或下次保存仍可补齐。
      }
    }
  },
  { immediate: true },
)

async function saveShare(): Promise<boolean> {
  const cid = (await shareInput.value?.ensureCid()) ?? ''
  if (!cid) {
    message.error('目录路径无法识别：请点「选择目录」重新选择，或输入纯数字 cid')
    return false
  }
  share.model.value.folder = cid
  let readablePath = shareCid.value.path.trim()
  if (!readablePath || /^\d+$/.test(readablePath)) {
    try {
      readablePath = (await storageApi.path115(cid)).path
    } catch {
      readablePath = ''
    }
  }
  share.model.value.folder_path = readablePath
  return await share.save()
}

/**
 * 转存目录改了没保存 = 东西会落进旧目录：提交时 target_cid 传空，
 * 后端回落到 setting `share` 里已保存的那份（见 share.go shareReceiveCore）。
 *
 * 比 cid 不比 path：Cid115Input 在路径一改就把 cid 作废，改动立刻可见；
 * 重新选中同一个目录时 cid 不变，也不会误报。
 */
const shareDirty = computed(
  () => share.dirty.value || shareCid.value.cid.trim() !== (share.model.value.folder || '').trim(),
)

// ---- 监控上传 ----
const monitor = useSetting('monitor', { enabled: false })

// ---- 提交链接 ----
const link = ref('')
const code = ref('')
const submitting = ref(false)
/** 「转存后整理」是本机偏好，不入库，与旧版一致 */
const organize = ref(localStorage.getItem('transfer-organize') !== 'false')
watch(organize, (v) => localStorage.setItem('transfer-organize', v ? 'true' : 'false'))

async function submit() {
  const raw = link.value.trim()
  if (!raw) {
    message.warning('请填写链接')
    return
  }
  // 与后端 classifyLink 保持一致，避免备用分享域名误走离线下载。
  const isShare = ['115.com/s/', '115cdn.com/s/', 'anxia.com/s/'].some((domain) =>
    raw.toLowerCase().includes(domain),
  )
  let url = raw
  let pwd = code.value.trim()

  if (isShare) {
    // 提取码三种来源：独立输入框 > URL 里的 ?password= / #xxxx > 「链接 空格 提取码」同框写法
    if (!pwd) {
      const m = raw.match(/[?#](?:password=)?([a-zA-Z0-9]{4,})/)
      if (m) {
        pwd = m[1]
        url = raw.split(/[?#]/)[0]
      }
    }
    if (!pwd) {
      const parts = raw.split(/\s+/)
      if (parts.length >= 2) {
        url = parts[0]
        pwd = parts[1]
      }
    }
    if (!pwd) {
      message.warning('115 分享链接需要提取码，请填写后重试')
      return
    }
  }

  if (shareDirty.value && !(await confirmUnsaved('直接开始会存进原来的目录。', saveShare))) {
    return
  }

  submitting.value = true
  message.info(isShare ? '转存进行中…' : '离线下载提交中…')
  try {
    const body = { url, code: pwd, target_cid: '', organize: organize.value }
    const d = isShare ? await transferApi.shareReceive(body) : await transferApi.offlineAdd(body)
    message.success(d.message || '已提交')
    link.value = ''
    code.value = ''
  } catch (e) {
    toastError(e, isShare ? '转存失败' : '离线下载失败')
  } finally {
    submitting.value = false
  }
}

</script>

<template>
  <div class="h-tabs-page">
<HTabs v-model="tab" :items="[{ value: 'download', label: '转存下载' }, { value: 'upload', label: '监控上传' }]" />
<template v-if="tab === 'download'">
      <div class="stack">
        <SectionCard title="转存目录">
          <FieldRow
            label="转存目录"
            tip="磁力 / ed2k / 分享链接转存后的落盘目录。提交后自动整理入库（开启「转存后整理」时）。必须与媒体库目录互不包含。"
          >
            <Cid115Input ref="shareInput" v-model="shareCid" placeholder="转存 / 离线下载的目标目录" />
          </FieldRow>
          <FormActions>
            <HButton variant="primary" :loading="share.saving.value" @click="saveShare">保存目录</HButton>
          </FormActions>
        </SectionCard>

        <SectionCard title="提交链接">
          <FieldRow
            label="链接"
            wide
            tip="支持磁力 / ed2k / HTTP / 115 分享链接，提交后自动整理入库。"
            hint="115 分享链接需提取码：填在右侧输入框，或直接附在链接后（?password=xxxx / 空格加提取码均可）。"
          >
            <div class="link-row">
              <HInput v-model="link" placeholder="粘贴磁力 / ed2k / 115 分享链接" @enter="submit" />
              <HInput v-model="code" placeholder="提取码" class="code" />
            </div>
          </FieldRow>

          <FieldRow
            label="转存后整理"
            tip="开启后转存 / 下载完成自动识别入库并生成 STRM（识别 → 分类 → 重命名 → 同步全流程）。"
          >
            <HSegmented v-model="organize" :options="[{ label: '开启', value: true }, { label: '关闭', value: false }]" />
          </FieldRow>

          <FormActions>
            <HButton variant="primary" :loading="submitting" @click="submit">开始转存</HButton>
          </FormActions>
        </SectionCard>
      </div>
</template>
<template v-if="tab === 'upload'">
      <SectionCard title="监控上传" hint="默认禁止，显式开启后才向 115 写入">
        <HAlert status="warning" class="note">
          上传属于 115 风控敏感操作，默认关闭。开启后会监控统一配置的本地媒体库根目录，
          自动检测本站或 Emby 新产生的标准图片（poster / fanart / banner / seasonXX-poster 等）与
          NFO（tvshow / movie / season / 每集同名 .nfo），按相对路径上传到 115 对应目录。
        </HAlert>

        <FieldRow
          label="允许上传到 115"
          tip="总开关。关闭时，定时监控、刮削结束回传和兜底回传都不会上传任何文件。"
        >
          <HSegmented v-model="monitor.model.value.enabled" :options="[{ label: '允许', value: true }, { label: '禁止（推荐）', value: false }]" />
        </FieldRow>

        <FieldRow
          label="本地媒体库根目录"
          tip="与全量同步、增量同步、整理和影视刮削共用同一位置；目标固定为 115 媒体库。"
        >
          <HInput :model-value="media.model.value.local_path || '未配置'" readonly />
          <HButton variant="ghost" class="text-btn location-link" @click="router.push({ name: 'accounts' })">
            前往「账号与媒体库」修改
          </HButton>
        </FieldRow>

        <FormActions>
          <HButton variant="primary" :loading="monitor.saving.value" @click="monitor.save()">保存配置</HButton>
          <HButton variant="tertiary" @click="monitor.reset">重置配置</HButton>
        </FormActions>
      </SectionCard>
</template>
</div>
</template>

<style scoped>
.stack {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.link-row {
  display: flex;
  gap: 8px;
}
.code {
  width: 120px;
  flex: none;
}
.note {
  margin-bottom: 12px;
}
.location-link {
  margin-top: 6px;
}
</style>
