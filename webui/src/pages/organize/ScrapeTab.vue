<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NAlert, NButton, NRadioButton, NRadioGroup } from 'naive-ui'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import LocalPathInput from '@/components/LocalPathInput.vue'
import { organizeApi } from '@/api'
import type { ScrapeConfig } from '@/api/organize'
import { toastError, useFeedback } from '@/composables/useFeedback'

const { message } = useFeedback()

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
    await organizeApi.saveScrapeConfig(cfg.value)
    message.success('保存成功')
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}

async function run() {
  if (!cfg.value.local_root) {
    message.warning('请先填写本地媒体库根目录')
    return
  }
  starting.value = true
  try {
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
    <NAlert class="note" type="info" :bordered="false">
      按 TMDB 直接生成标准 NFO + 海报到本地媒体库对应片目目录，落盘后由「监控上传」自动回传 115
      —— 替代「Emby 刮削到本地」。Emby 侧建议把元数据读取器设为「仅 NFO」，以本站数据为准。
      剧集生成 tvshow.nfo、整季海报与逐集同名 NFO；NFO 内含 fileinfo/streamdetails
      轨道信息（ffprobe 探测的多音轨 / 内嵌字幕），播放器无需探测 strm 远端即可显示音轨字幕。
    </NAlert>

    <FieldRow
      label="本地媒体库根目录"
      tip="本地挂载的媒体库根目录（如 /media）。片目目录 = 根目录 + 台账相对路径，与监控上传的监控目录通常是同一个。"
    >
      <LocalPathInput v-model="cfg.local_root" placeholder="如 /media" />
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

    <FieldRow
      label="整理后自动刮削"
      hint="增量同步动过媒体库后自动开始刮削；刮削期间元数据边生成边回传 115。"
    >
      <NRadioGroup v-model:value="cfg.auto_after_organize">
        <NRadioButton :value="true">开启</NRadioButton>
        <NRadioButton :value="false">关闭</NRadioButton>
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
</style>
