<script setup lang="ts">
import { ref, watch } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import Cid115Input from '@/components/Cid115Input.vue'
import { storageApi } from '@/api'
import { useSetting } from '@/composables/useSetting'
import { useTransferSources } from '@/composables/transferSources'
import { useFeedback } from '@/composables/useFeedback'

/** 转存目录（setting `share`）。原在「链接转存」页签下方，并页时挪进设置 */
const { message } = useFeedback()
const { reload } = useTransferSources()

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
  const ok = await share.save()
  if (ok) reload() // 找资源 / 链接转存上方显示的转存目录跟着变
  return ok
}
</script>

<template>
  <SectionCard title="转存目录" hint="链接转存、离线下载、找资源与订阅提交的内容都落在这里，由自动整理接管入库">
    <FieldRow label="转存目录" tip="必须与媒体库目录互不包含。提交后立即触发一次整理，没赶上的由守望者每分钟检查一次这个目录兜底。">
      <Cid115Input ref="shareInput" v-model="shareCid" placeholder="转存 / 离线下载的目标目录" />
    </FieldRow>
    <FormActions>
      <HButton variant="primary" :loading="share.saving.value" @click="saveShare">保存目录</HButton>
    </FormActions>
  </SectionCard>
</template>
