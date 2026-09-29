<script setup lang="ts">
import { onMounted, ref } from 'vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HInput from '@/components/hero/HInput.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import { heroTone } from '@/components/hero/tone'
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
  probe_streams: false,
  skip_shared_stills: true,
})
const saving = ref(false)

async function load() {
  try {
    const res = await organizeApi.getScrapeConfig()
    // 后端是 { cfg, status }：此前这里取 res.data ?? res 摊平读，字段全是 undefined，
    // 于是「整理后自动刮削」无论后端存的是什么都显示「关闭」，
    // 点一次保存还会把实际配置按这份假显示写回去
    const c = res.cfg ?? {}
    cfg.value = {
      local_root: c.local_root ?? '',
      // 这两项后端缺省视为开启，所以判 !== false 而不是 !!
      write_nfo: c.write_nfo !== false,
      write_images: c.write_images !== false,
      force: !!c.force,
      auto_after_organize: !!c.auto_after_organize,
      probe_streams: !!c.probe_streams,
      // 后端缺省开启
      skip_shared_stills: c.skip_shared_stills !== false,
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
      <HSegmented v-model="cfg.auto_after_organize" :options="[{ label: '开启', value: true }, { label: '关闭', value: false }]" />
    </FieldRow>

    <FieldRow
      label="上传到 115"
      tip="这里只显示监控上传总开关的当前状态；刮削始终先写入本地媒体库。"
    >
      <div class="upload-status">
        <HChip :color="heroTone(monitor.model.value.enabled ? 'success' : 'default')">
          {{ monitor.model.value.enabled ? '已允许上传' : '已禁止上传（默认）' }}
        </HChip>
        <HButton variant="ghost" class="text-btn" @click="router.push({ name: 'upload-download', query: { tab: 'upload' } })">
          前往上传开关配置
        </HButton>
      </div>
    </FieldRow>

    <HAlert status="accent" class="note">
      按 TMDB 直接生成标准 NFO + 海报到本地媒体库对应片目目录；仅在允许上传时由「监控上传」回传 115
      —— 替代「Emby 刮削到本地」。
      电影生成与视频同名的 NFO（口径与 Emby 自己刮削一致），剧集生成 tvshow.nfo、season.nfo、季海报、
      逐集同名 NFO 与集剧照。
      刮削在单独的「刮削队列」里执行，不占任务锁：几百集的剧刮半小时，整理与同步照常进行。
      进度见顶栏任务队列，逐个文件的下载地址、大小与去向见实时日志（搜「[影视刮削]」）。
    </HAlert>

    <!-- Emby 默认开着联网刮削：不关的话它照样去 TMDB 拉一遍、下一遍图，本站写的 NFO / 海报会被它的结果盖掉，流量白花 -->
    <HAlert status="warning" class="note" title="用本站刮削时，Emby 媒体库要这样设">
      在 Emby「媒体库 → 编辑媒体库」里（每个媒体库都要改）：
      <ol class="emby-steps">
        <li>元数据读取器：只勾 <b>Nfo</b></li>
        <li>元数据下载器：<b>全部取消勾选</b></li>
        <li>元数据存储方式：选 <b>Nfo</b></li>
        <li>图像获取器：<b>全部取消勾选</b></li>
        <li>关闭「保存媒体图片到媒体文件夹中」「在服务器的元数据文件夹中保留图像的缓存副本」「预先下载图像」</li>
      </ol>
      不改的话 Emby 会自己再联网刮一遍、再下一遍图，最终以 Emby 的结果为准，本站刮的白刮，还多耗流量。
      <br />
      <b>想切回 Emby 自己刮削</b>：先把上面的「整理后自动刮削」关掉，再在 Emby 里勾回元数据下载器与图像获取器（TheMovieDb 等）。
      本站已写好的 NFO 与海报不会浪费：Nfo 读取器保持勾选时 Emby 先读现成的，只联网补缺；
      媒体目录里的海报 Emby 本来就会读，不需要为此打开「保存媒体图片到媒体文件夹中」。
      那个开关管的是 Emby <b>新下载</b>的图片写到哪 —— 要让「监控上传」把 Emby 的刮削结果回传 115 才需要打开。
      想让 Emby 完全按自己的重刮，对媒体库「刷新元数据」并选「替换所有元数据 / 图像」。
    </HAlert>

    <FieldRow
      label="本地媒体库根目录"
      tip="统一位置配置；片目目录 = 根目录 + 台账相对路径。"
    >
      <HInput :model-value="media.model.value.local_path || '未配置'" readonly />
      <HButton variant="ghost" class="text-btn location-link" @click="router.push({ name: 'accounts' })">
        前往「账号与媒体库」修改
      </HButton>
    </FieldRow>

    <FieldRow v-for="s in SWITCHES" :key="s.key" :label="s.label">
      <HSegmented v-model="cfg[s.key]" :options="[{ label: s.on, value: true }, { label: s.off, value: false }]" />
    </FieldRow>

    <FieldRow label="覆盖模式">
      <HSegmented v-model="cfg.force" :options="[{ label: '只补缺失', value: false }, { label: '强制覆盖', value: true }]" />
    </FieldRow>

    <FieldRow
      label="占位剧照"
      hint="综艺常见同一季几十集挂同一张剧照。同一季里 3 集以上共用一张（或内容完全相同）时判为占位图，这些集不写集剧照，Emby 会改用剧的背景图。只在同一季内比较，别的季用过同一张图不算。"
    >
      <HSegmented
        v-model="cfg.skip_shared_stills"
        :options="[{ label: '不写', value: true }, { label: '照写', value: false }]"
      />
    </FieldRow>

    <FieldRow
      label="轨道探测"
      hint="入库后让 Emby 提前探测媒体信息（分辨率、音轨、内嵌字幕），第一次播放就不用现场探测，起播和第二次一样快。整理、同步入库确认后自动进行；在「本地文件」手动刮削时也可以给所选片目补上（所选视频超过 100 个要确认两次）。Emby 已有媒体信息的条目不碰。后台一次探一个、间隔 3 秒，每个条目会产生一次 115 直链请求。需要先在「EMBY管理」配好服务器地址与 API 密钥。"
    >
      <HSegmented v-model="cfg.probe_streams" :options="[{ label: '关闭', value: false }, { label: '开启', value: true }]" />
    </FieldRow>

    <!-- 用户反馈过「未识别的文件手动刮削没用」：刮削只认已入库的片目，这里把范围说清楚，并给出正确入口 -->
    <HAlert status="warning" class="note" title="手动刮削在「本地文件」页，只处理已入库的片目">
      这里只是刮削的默认配置。要手动刮削，到「本地文件」页勾选片目后点「刮削」（不再提供全库刮削）。
      未识别、整理失败的文件还在网盘的待整理 / 冗余目录里，本地没有 STRM，刮不到它们 ——
      请到「任务中心 → 整理记录」对它们「重新整理」并指定 TMDB 条目，入库时会自动刮削。
      <template #actions>
        <HButton variant="tertiary" size="sm" @click="router.push({ name: 'tasks', query: { tab: 'records', status: 'problem' } })">
          去处理未识别 / 失败的条目
        </HButton>
      </template>
    </HAlert>

    <FormActions>
      <HButton variant="primary" :loading="saving" @click="save">保存配置</HButton>    </FormActions>
  </SectionCard>
</template>

<style scoped>
.note {
  margin-bottom: 12px;
}
.emby-steps {
  margin: 6px 0;
  padding-left: 20px;
  list-style: decimal;
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
