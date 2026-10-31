<script setup lang="ts">
import { onMounted, ref } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import HNumberInput from '@/components/hero/HNumberInput.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import HAlert from '@/components/hero/HAlert.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import { shareStrmApi } from '@/api'
import type { ShareLinkAction, ShareStrmConfig, ShareStrmConfigResp } from '@/api/sharestrm'
import { toastError, useFeedback } from '@/composables/useFeedback'

const props = defineProps<{ sources: number }>()
const emit = defineEmits<{ saved: []; goStrm: [] }>()
const { message, dialog } = useFeedback()

const form = ref<ShareStrmConfig>({ enabled: false, default_action: 'ask', root: '分享', min_size_mb: 50, skip_owned: true, assets: true })
const info = ref<ShareStrmConfigResp | null>(null)
const saving = ref(false)

async function load() {
  try {
    const d = await shareStrmApi.config()
    info.value = d
    form.value = { ...d.config }
  } catch (e) {
    toastError(e, '读取失败')
  }
}
onMounted(load)

const actions: { label: string; value: ShareLinkAction }[] = [
  { label: '每次问', value: 'ask' },
  { label: '转存', value: 'transfer' },
  { label: '生成 STRM', value: 'strm' },
]

async function save() {
  // 从开到关：说清楚已经生成的照常能播，关开关不是删除
  if (info.value?.config.enabled && !form.value.enabled && props.sources > 0) {
    const ok = await dialog.confirm({
      title: '关掉分享 STRM',
      content: `已经生成的 ${props.sources} 个分享照常能播，只是不能再添加、也不再检查。要删掉它们，在「分享」页签里逐个删除。`,
      actions: [
        { label: '取消', value: false, variant: 'tertiary' },
        { label: '关掉', value: true, variant: 'primary' },
      ],
    })
    if (!ok) return
  }
  saving.value = true
  try {
    const d = await shareStrmApi.saveConfig({ ...form.value, min_size_mb: Number(form.value.min_size_mb) || 0 })
    message.success(d.message)
    await load()
    emit('saved')
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <SectionCard title="分享 STRM 设置" hint="不转存、不占网盘空间：本地 STRM 直接指向 115 分享，播放时用你的 115 账号按分享取直链。">
    <HAlert v-if="info && !info.domain_ok" status="warning" title="还没填直连域名">
      STRM 里写的是本站的播放地址，没有直连域名写出来的 STRM 播不了。
      <template #actions>
        <HButton size="sm" variant="secondary" @click="emit('goStrm')">去 Strm 管理填</HButton>
      </template>
    </HAlert>

    <FieldRow label="开启" tip="关着时不能添加、不再检查；已经生成的 STRM 照常能播（关开关不是删除）。">
      <HSwitch v-model="form.enabled" aria-label="开启分享 STRM" />
    </FieldRow>

    <FieldRow
      label="贴 115 分享链接时"
      tip="机器人与「发现与订阅 → 链接转存」贴 115 分享链接时默认怎么处理。每次问：机器人回「转存」或「STRM」，网页上两个按钮都给。开关关着时一律转存。"
    >
      <HSegmented v-model="form.default_action" :options="actions" size="sm" />
    </FieldRow>

    <FieldRow
      label="本地目录"
      wide
      :hint="info ? `媒体根 ${info.media_root} 下，现在是 ${info.root_abs}。在 Emby 里给它单独建库，或加进已有媒体库的路径。` : ''"
      :error="info?.root_problem"
      tip="不能和任何媒体库的本地目录重叠：重叠的话全量同步和整理会把这里的 STRM 当成库里的东西。已经有分享生成后不能再改。"
    >
      <HInput v-model="form.root" mono :disabled="sources > 0" placeholder="分享" />
    </FieldRow>

    <FieldRow label="最小体积" tip="比这小的视频不生成 STRM，挡掉样片和广告。0 = 不限。">
      <div class="inline">
        <HNumberInput v-model="form.min_size_mb" :min="0" :max="102400" class="num" />
        <span>MB</span>
      </div>
    </FieldRow>

    <FieldRow label="库里已有的跳过" tip="分享里的文件和你媒体库里某个文件是同一份（sha1 一样）时，不再生成一份 STRM，免得 Emby 里同一部出现两次。">
      <HSwitch v-model="form.skip_owned" aria-label="库里已有的跳过" />
    </FieldRow>

    <FieldRow label="下载附属文件" tip="分享里的字幕、NFO、海报（标准命名的）下载到 STRM 旁边，Emby 才认得到。单个最大 10MB，一个分享最多 200 个。">
      <HSwitch v-model="form.assets" aria-label="下载附属文件" />
    </FieldRow>

    <div class="foot">
      <HButton variant="primary" :loading="saving" @click="save">保存</HButton>
    </div>
  </SectionCard>
</template>

<style scoped>
.inline {
  display: flex;
  align-items: center;
  gap: 8px;
}
.num {
  width: 140px;
}
.foot {
  display: flex;
  justify-content: flex-end;
  margin-top: 8px;
}
</style>
