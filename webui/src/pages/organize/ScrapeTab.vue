<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NAlert, NButton, NInput, NRadioButton, NRadioGroup, NTag } from 'naive-ui'
import { useRouter } from 'vue-router'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import { organizeApi } from '@/api'
import { useSetting } from '@/composables/useSetting'
import { useFullSetting } from '@/pages/strm/fullSetting'
import type { ScrapeConfig } from '@/api/organize'
import { toastError, useFeedback } from '@/composables/useFeedback'

const { message } = useFeedback()
const router = useRouter()
const media = useFullSetting()
const monitor = useSetting('monitor', { enabled: false })

const cfg = ref<ScrapeConfig>({
  local_root: '',
  write_nfo: true,
  write_images: true,
  force: false,
  auto_after_organize: false,
})
const saving = ref(false)
const starting = ref(false)

async function load() {
  try {
    const res = await organizeApi.getScrapeConfig()
    const c = res.data ?? res
    cfg.value = {
      local_root: c.local_root ?? '',
      // 这两项后端缺省视为开启，所以判 !== false 而不是 !!
      write_nfo: c.write_nfo !== false,
      write_images: c.write_images !== false,
      force: !!c.force,
      auto_after_organize: !!c.auto_after_organize,
    }
  } catch {
    // 首次使用尚无配置
  }
}

async function save() {
  saving.value = true
  try {
    cfg.value.local_root = media.model.value.local_path
    await organizeApi.saveScrapeConfig(cfg.value)
    message.success('保存成功')
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}

async function run() {
  if (!media.model.value.local_path) {
    message.warning('请先到「账号与媒体库」配置本地媒体库根目录')
    return
  }
  starting.value = true
  try {
    cfg.value.local_root = media.model.value.local_path
    // 先存再跑：后端跑的是已保存的配置，不是请求体
    await organizeApi.saveScrapeConfig(cfg.value)
    await organizeApi.runScrape()
    message.success('刮削已开始，进度与结果见实时日志')
  } catch (e) {
    toastError(e, '启动失败')
  } finally {
    starting.value = false
  }
}

async function stop() {
  try {
    await organizeApi.stopScrape()
    message.success('已请求停止')
  } catch (e) {
    toastError(e, '停止失败')
  }
}

const SWITCHES = [
  { key: 'write_nfo' as const, label: 'NFO 元数据', on: '生成', off: '跳过' },
  { key: 'write_images' as const, label: '图片海报', on: '生成', off: '跳过' },
]

onMounted(load)
</script>

<template>
  <SectionCard title="影视刮削" hint="TMDB → 本地 NFO / 海报">
    <FieldRow
      label="整理后自动刮削"
      hint="整理完成后只刮本轮新入库的片目（不扫全库）；是否上传到 115 由下方上传开关独立控制。"
    >
      <NRadioGroup v-model:value="cfg.auto_after_organize">
        <NRadioButton :value="true">开启</NRadioButton>
        <NRadioButton :value="false">关闭</NRadioButton>
      </NRadioGroup>
    </FieldRow>

    <FieldRow
      label="上传到 115"
      tip="这里只显示监控上传总开关的当前状态；刮削始终先写入本地媒体库。"
    >
      <div class="upload-status">
        <NTag :type="monitor.model.value.enabled ? 'success' : 'default'" :bordered="false">
          {{ monitor.model.value.enabled ? '已允许上传' : '已禁止上传（默认）' }}
        </NTag>
        <NButton
          text
          type="primary"
          @click="router.push({ name: 'upload-download', query: { tab: 'upload' } })"
        >
          前往上传开关配置
        </NButton>
      </div>
    </FieldRow>

    <NAlert class="note" type="info" :bordered="false">
      按 TMDB 直接生成标准 NFO + 海报到本地媒体库对应片目目录；仅在允许上传时由「监控上传」回传 115
      —— 替代「Emby 刮削到本地」。Emby 侧建议把元数据读取器设为「仅 NFO」，以本站数据为准。
      剧集生成 tvshow.nfo、整季海报与逐集同名 NFO；NFO 内含 fileinfo/streamdetails
      轨道信息（ffprobe 探测的多音轨 / 内嵌字幕），播放器无需探测 strm 远端即可显示音轨字幕。
    </NAlert>

    <FieldRow
      label="本地媒体库根目录"
      tip="统一位置配置；片目目录 = 根目录 + 台账相对路径。"
    >
      <NInput :value="media.model.value.local_path || '未配置'" readonly />
      <NButton class="location-link" text type="primary" @click="router.push({ name: 'accounts' })">
        前往「账号与媒体库」修改
      </NButton>
    </FieldRow>

    <FieldRow v-for="s in SWITCHES" :key="s.key" :label="s.label">
      <NRadioGroup v-model:value="cfg[s.key]">
        <NRadioButton :value="true">{{ s.on }}</NRadioButton>
        <NRadioButton :value="false">{{ s.off }}</NRadioButton>
      </NRadioGroup>
    </FieldRow>

    <FieldRow label="覆盖模式">
      <NRadioGroup v-model:value="cfg.force">
        <NRadioButton :value="false">只补缺失</NRadioButton>
        <NRadioButton :value="true">强制覆盖</NRadioButton>
      </NRadioGroup>
    </FieldRow>

    <FormActions>
      <NButton type="primary" :loading="saving" @click="save">保存配置</NButton>
      <NButton type="primary" ghost :loading="starting" @click="run">开始刮削</NButton>
      <NButton @click="stop">停止</NButton>
    </FormActions>
  </SectionCard>
</template>

<style scoped>
.note {
  margin-bottom: 12px;
}
.location-link {
  margin-top: 6px;
}
.upload-status {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
</style>
