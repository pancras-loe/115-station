<script setup lang="ts">
import { onMounted, ref } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import HNumberInput from '@/components/hero/HNumberInput.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import HSelect from '@/components/hero/HSelect.vue'
import HAlert from '@/components/hero/HAlert.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import { libraryApi, shareStrmApi } from '@/api'
import { FOLLOW_OPTIONS, LAYOUT_OPTIONS, type ShareLinkAction, type ShareStrmConfig, type ShareStrmConfigResp } from '@/api/sharestrm'
import { toastError, useFeedback } from '@/composables/useFeedback'

const props = defineProps<{ sources: number }>()
const emit = defineEmits<{ saved: []; goStrm: [] }>()
const { message, dialog } = useFeedback()

const form = ref<ShareStrmConfig>({
  enabled: false,
  default_action: 'ask',
  root: '分享',
  min_size_mb: 50,
  skip_owned: true,
  assets: true,
  follow_hours: 0,
  layout: 'keep',
  scheme_id: 0,
  scrape: false,
})
const info = ref<ShareStrmConfigResp | null>(null)
const saving = ref(false)
const schemes = ref<{ label: string; value: number }[]>([{ label: '只分电影 / 剧集', value: 0 }])

async function load() {
  try {
    const d = await shareStrmApi.config()
    info.value = d
    form.value = { ...d.config }
  } catch (e) {
    toastError(e, '读取失败')
  }
  try {
    // 分类方案在「自动整理 → 二级分类」里建，这里只选
    const libs = await libraryApi.getMediaLibs()
    schemes.value = [{ label: '只分电影 / 剧集', value: 0 }, ...(libs.schemes ?? []).map((s) => ({ label: s.name, value: s.id }))]
  } catch {
    // 读不到方案清单时只留「只分电影 / 剧集」
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
    const d = await shareStrmApi.saveConfig({
      ...form.value,
      min_size_mb: Number(form.value.min_size_mb) || 0,
      follow_hours: Number(form.value.follow_hours) || 0,
      scheme_id: Number(form.value.scheme_id) || 0,
    })
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

    <FieldRow
      label="新分享默认追更"
      tip="隔一段时间重新列一遍分享：分享者补了新集就补上 STRM，删了的文件隔一轮确认后删掉本地。每个分享可以在列表里单独改。剧集分享建议开，电影一般不用。"
    >
      <HSelect v-model="form.follow_hours" :options="FOLLOW_OPTIONS" class="follow" aria-label="新分享默认追更" />
    </FieldRow>

    <FieldRow
      label="新分享默认放法"
      tip="原样：按分享里的目录放在分享目录下。本地整理：认出是哪部片，按「自动整理」的命名模板改名、按分类方案放（电影 / 剧集 / 分类 / 片名 (年份) / Season 01），只改本地，115 不动。认不出的先按原样放，可以手动指定。每个分享可以在列表里单独改。"
    >
      <HSegmented v-model="form.layout" :options="LAYOUT_OPTIONS" size="sm" />
    </FieldRow>

    <FieldRow
      label="本地整理的分类"
      tip="用哪套分类方案（「自动整理 → 二级分类」里建的）。选「只分电影 / 剧集」就只按类型分两个目录。"
    >
      <HSelect v-model="form.scheme_id" :options="schemes" class="follow" aria-label="本地整理的分类" />
    </FieldRow>

    <FieldRow
      label="本站刮削"
      tip="本地整理认出是哪部的分享，生成完用本站刮削写 NFO 与图片（只补缺失、只写本地，不传网盘、不让 Emby 提前探测）。只在这一轮新写了 STRM 时刮；想补刮点列表里的「刮削」。关着交给 Emby 刮。原样放的分享认不出是哪部，一律交给 Emby。"
    >
      <HSwitch v-model="form.scrape" aria-label="本站刮削" />
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
.follow {
  width: 160px;
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
