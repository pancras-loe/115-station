<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import HButton from '@/components/hero/HButton.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import HChip from '@/components/hero/HChip.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import Cid115Input from '@/components/Cid115Input.vue'
import GySource from '@/components/transfer/sources/GySource.vue'
import PansouSource from '@/components/transfer/sources/PansouSource.vue'
import MukakuSource from '@/components/transfer/sources/MukakuSource.vue'
import Re0Source from '@/components/transfer/sources/Re0Source.vue'
import { storageApi } from '@/api'
import type { SourceKey } from '@/api/transfer'
import { useSetting } from '@/composables/useSetting'
import { useTransferSources } from '@/composables/transferSources'
import { useFeedback } from '@/composables/useFeedback'

const { message } = useFeedback()
const route = useRoute()
const { state, reload, setEnabled } = useTransferSources()

// ---- 转存目录（原在「上传下载 → 转存下载」） ----
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

async function saveShare() {
  const cid = (await shareInput.value?.ensureCid()) ?? ''
  if (!cid) {
    message.error('目录路径无法识别：请点「选择目录」重新选择，或输入纯数字 cid')
    return
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
  if (await share.save()) reload()
}

// ---- 来源 ----
const CARDS: { key: SourceKey; title: string; hint: string }[] = [
  { key: 'gy', title: '观影', hint: '站内种子 → 115 离线下载' },
  { key: 'pansou', title: '盘搜 PanSou', hint: '聚合全网网盘分享，免登录' },
  { key: 'mukaku', title: '不太灵影视', hint: '站内资源需 VIP Token' },
  { key: 're0', title: 'RE0', hint: '官方 OpenAPI，解锁花站内积分' },
]

const byKey = computed(() => Object.fromEntries((state.value?.sources ?? []).map((s) => [s.key, s])))

// 旧地址 ?tab=gy 这类进来时，滚到对应的卡片
onMounted(async () => {
  const focus = route.query.focus as string | undefined
  if (!focus) return
  await nextTick()
  document.getElementById(`src-${focus}`)?.scrollIntoView({ block: 'start', behavior: 'smooth' })
})
</script>

<template>
  <div class="stack">
    <SectionCard title="转存目录" hint="磁力 / ed2k / 分享链接转存后落在这里，由自动整理接管入库">
      <FieldRow label="转存目录" tip="必须与媒体库目录互不包含。提交后立即触发一次整理，没赶上的由守望者每分钟检查一次这个目录兜底。">
        <Cid115Input ref="shareInput" v-model="shareCid" placeholder="转存 / 离线下载的目标目录" />
      </FieldRow>
      <FormActions>
        <HButton variant="primary" :loading="share.saving.value" @click="saveShare">保存目录</HButton>
      </FormActions>
    </SectionCard>

    <SectionCard v-for="c in CARDS" :id="`src-${c.key}`" :key="c.key" :title="c.title" :hint="c.hint">
      <template #extra>
        <div class="src-extra">
          <HChip v-if="byKey[c.key]?.reason" color="warning">{{ byKey[c.key]?.reason }}</HChip>
          <HChip v-else-if="byKey[c.key]" color="success">可用</HChip>
          <label class="src-switch">
            <span>{{ byKey[c.key]?.enabled === false ? '搜索时跳过' : '参与搜索' }}</span>
            <HSwitch
              :model-value="byKey[c.key]?.enabled !== false"
              :aria-label="`${c.title} 参与搜索`"
              size="sm"
              @update:model-value="(v: boolean) => setEnabled(c.key, v)"
            />
          </label>
        </div>
      </template>
      <GySource v-if="c.key === 'gy'" @changed="reload" />
      <PansouSource v-else-if="c.key === 'pansou'" @changed="reload" />
      <MukakuSource v-else-if="c.key === 'mukaku'" @changed="reload" />
      <Re0Source v-else-if="c.key === 're0'" @changed="reload" />
    </SectionCard>
  </div>
</template>

<style scoped>
.stack {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.src-extra {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
}
.src-switch {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 12.5px;
  color: var(--muted);
  cursor: pointer;
}
</style>
