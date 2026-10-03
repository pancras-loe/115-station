<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { ChevronRight, HardDrive, Info, TriangleAlert, Upload } from '@lucide/vue'
import HButton from '@/components/hero/HButton.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import { organizeApi } from '@/api'
import { useSetting } from '@/composables/useSetting'
import { useFullSetting } from '@/pages/strm/fullSetting'
import type { ScrapeConfig } from '@/api/organize'
import { toastError, useFeedback } from '@/composables/useFeedback'

/**
 * 影视刮削配置。2026-09-30 改版：原来一张卡里夹着三块长提示框（Emby 设置步骤 / 刮削做什么 / 手动刮削在哪），
 * 配置项被挤到第二屏。现在左边是开关，右边是去向与说明，Emby 设置步骤默认折起。
 */
const { message } = useFeedback()
const media = useFullSetting()
const monitor = useSetting('monitor', { enabled: false })

const cfg = ref<ScrapeConfig>({
  local_root: '',
  write_nfo: true,
  write_images: true,
  force: false,
  auto_after_organize: false,
  auto_after_sync: false,
  probe_streams: false,
  skip_shared_stills: true,
})
/** 读回来的那份，用来判断改过没有；先按默认值起步，否则读回来之前保存条会闪一下「有未保存的改动」 */
const savedJson = ref(JSON.stringify(cfg.value))
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
      auto_after_sync: !!c.auto_after_sync,
      probe_streams: !!c.probe_streams,
      // 后端缺省开启
      skip_shared_stills: c.skip_shared_stills !== false,
    }
  } catch {
    // 首次使用尚无配置
  } finally {
    savedJson.value = JSON.stringify(cfg.value)
  }
}

const dirty = () => JSON.stringify(cfg.value) !== savedJson.value

async function save() {
  saving.value = true
  try {
    cfg.value.local_root = media.model.value.local_path
    await organizeApi.saveScrapeConfig(cfg.value)
    savedJson.value = JSON.stringify(cfg.value)
    message.success('保存成功')
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}

const ON_OFF = [
  { label: '开启', value: true },
  { label: '关闭', value: false },
]
const MAKE_SKIP = [
  { label: '生成', value: true },
  { label: '跳过', value: false },
]

onMounted(load)
</script>

<template>
  <div class="stack">
    <div class="grid">
      <!-- ==== 左：刮什么 ==== -->
      <SectionCard title="刮削内容" hint="按 TMDB 生成标准 NFO 与海报，写进本地媒体库的片目目录">
        <FieldRow
          label="整理后自动刮削"
          tip="整理完成后只刮本轮新入库的片目（不扫全库）。刮削在单独的「刮削队列」里执行，不占任务锁：几百集的剧刮半小时，整理与同步照常进行。"
        >
          <HSegmented v-model="cfg.auto_after_organize" :options="ON_OFF" />
        </FieldRow>

        <FieldRow
          label="同步后自动刮削"
          tip="增量同步新生成 STRM 后（手机上传、网页端拖进媒体库等外部变更），刮削这些 STRM 所在的片目；整理入库的不经过这里。全量同步不触发，存量请用「扩展功能 → 媒体信息补全」。目录名里有 TMDB 编号就按编号刮；没有时按目录名搜 TMDB，只认标题或原名完全相等的条目，认不准就跳过（任务详情里列出来，可在「本地文件」手动指定）。只认当前分类目录下的片目，一轮最多 50 部。"
          hint="增量同步新增的片目；没有 TMDB 编号时只认片名完全相等"
        >
          <HSegmented v-model="cfg.auto_after_sync" :options="ON_OFF" />
        </FieldRow>

        <FieldRow
          label="NFO 元数据"
          tip="电影生成与视频同名的 NFO（口径与 Emby 自己刮削一致）；剧集生成 tvshow.nfo、season.nfo 与逐集同名 NFO。"
        >
          <HSegmented v-model="cfg.write_nfo" :options="MAKE_SKIP" />
        </FieldRow>

        <FieldRow label="图片海报" tip="海报、背景图、季海报与集剧照。">
          <HSegmented v-model="cfg.write_images" :options="MAKE_SKIP" />
        </FieldRow>

        <FieldRow label="覆盖模式" tip="只补缺失 = 已有的文件不动；强制覆盖 = 按 TMDB 重新生成一遍。">
          <HSegmented v-model="cfg.force" :options="[{ label: '只补缺失', value: false }, { label: '强制覆盖', value: true }]" />
        </FieldRow>

        <FieldRow
          label="占位剧照"
          tip="综艺常见同一季几十集挂同一张剧照。同一季里 3 集以上共用一张（或内容完全相同）时判为占位图，这些集不写集剧照，Emby 会改用剧的背景图。只在同一季内比较，别的季用过同一张图不算。"
          hint="同一季 3 集以上共用的剧照不写"
        >
          <HSegmented v-model="cfg.skip_shared_stills" :options="[{ label: '不写', value: true }, { label: '照写', value: false }]" />
        </FieldRow>

        <FieldRow
          label="轨道探测"
          tip="入库后让 Emby 提前探测媒体信息（分辨率、音轨、内嵌字幕），第一次播放就不用现场探测，起播和第二次一样快。整理、同步入库确认后自动进行；在「本地文件」手动刮削时也可以给所选片目补上（所选视频超过 100 个要确认两次）。Emby 已有媒体信息的条目不碰。后台一次探一个、间隔 3 秒，每个条目会产生一次 115 直链请求。需要先在「系统配置 → Emby」配好服务器地址与 API 密钥。"
          hint="入库后让 Emby 提前探测音视频轨道；每个条目一次 115 直链请求"
        >
          <HSegmented v-model="cfg.probe_streams" :options="[{ label: '关闭', value: false }, { label: '开启', value: true }]" />
        </FieldRow>
      </SectionCard>

      <!-- ==== 右：写到哪、要注意什么 ==== -->
      <SectionCard title="去向与注意事项" hint="刮削结果先写本地，按需回传 115">
        <div class="facts">
          <div class="fact">
            <span class="fact-icon"><HardDrive :size="15" /></span>
            <div class="fact-body">
              <div class="fact-label">写到本地</div>
              <div class="fact-value mono" :class="{ 'is-empty': !media.model.value.local_path }">
                {{ media.model.value.local_path || '未配置' }}
              </div>
            </div>
            <RouterLink :to="{ name: 'accounts' }" class="fact-link">修改<ChevronRight :size="13" /></RouterLink>
          </div>
          <div class="fact">
            <span class="fact-icon"><Upload :size="15" /></span>
            <div class="fact-body">
              <div class="fact-label">上传到 115</div>
              <div class="fact-value" :class="{ 'is-on': monitor.model.value.enabled }">
                {{ monitor.model.value.enabled ? '已允许（由「监控上传」回传）' : '不上传（默认）' }}
              </div>
            </div>
            <RouterLink :to="{ name: 'upload-download' }" class="fact-link">上传开关<ChevronRight :size="13" /></RouterLink>
          </div>
        </div>

        <p class="note">
          <Info :size="14" />
          <span>
            手动刮削在「<RouterLink :to="{ name: 'local' }">本地文件</RouterLink>」页勾选片目后点「刮削」，只处理已入库的片目。
            未识别、整理失败的文件还在网盘里、本地没有 STRM，要先到「<RouterLink :to="{ name: 'tasks', query: { tab: 'records', status: 'problem' } }">整理记录</RouterLink>」重新整理，入库时会自动刮削。
          </span>
        </p>

        <!-- Emby 默认开着联网刮削：不关的话它照样去 TMDB 拉一遍、下一遍图，本站写的 NFO / 海报会被它的结果盖掉，流量白花。
             一次性设置，默认折起；标题用警告色，免得被当成可有可无的说明 -->
        <details class="emby">
          <summary>
            <TriangleAlert :size="14" />
            用本站刮削时，Emby 媒体库要这样设
            <ChevronRight :size="14" class="emby-chev" />
          </summary>
          <div class="emby-body">
            <p>在 Emby「媒体库 → 编辑媒体库」里（每个媒体库都要改）：</p>
            <ol>
              <li>元数据读取器：只勾 <b>Nfo</b></li>
              <li>元数据下载器：<b>全部取消勾选</b></li>
              <li>元数据存储方式：选 <b>Nfo</b></li>
              <li>图像获取器：<b>全部取消勾选</b></li>
              <li>关闭「保存媒体图片到媒体文件夹中」「在服务器的元数据文件夹中保留图像的缓存副本」「预先下载图像」</li>
            </ol>
            <p>不改的话 Emby 会自己再联网刮一遍、再下一遍图，最终以 Emby 的结果为准，本站刮的白刮，还多耗流量。</p>
            <p>
              <b>想切回 Emby 自己刮削</b>：先关掉左边的「整理后自动刮削」与「同步后自动刮削」，再在 Emby 里勾回元数据下载器与图像获取器（TheMovieDb 等）。
              本站已写好的 NFO 与海报不会浪费：Nfo 读取器保持勾选时 Emby 先读现成的，只联网补缺；
              媒体目录里的海报 Emby 本来就会读，不需要为此打开「保存媒体图片到媒体文件夹中」。
              那个开关管的是 Emby <b>新下载</b>的图片写到哪 —— 要让「监控上传」把 Emby 的刮削结果回传 115 才需要打开。
              想让 Emby 完全按自己的重刮，对媒体库「刷新元数据」并选「替换所有元数据 / 图像」。
            </p>
          </div>
        </details>
      </SectionCard>
    </div>

    <div class="save-bar" :class="{ 'is-dirty': dirty() }">
      <span class="save-note">
        {{ dirty() ? '有未保存的改动' : '进度见顶栏任务队列；逐个文件的去向见实时日志（搜「[影视刮削]」）' }}
      </span>
      <HButton variant="primary" size="sm" :loading="saving" @click="save">保存配置</HButton>
    </div>
  </div>
</template>

<style scoped>
.stack {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
  align-items: start;
}

/* ---- 去向：两行事实 ---- */
.facts {
  display: flex;
  flex-direction: column;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.fact {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
}
.fact + .fact {
  border-top: 1px solid var(--separator);
}
.fact-icon {
  width: 30px;
  height: 30px;
  flex-shrink: 0;
  border-radius: 9px;
  display: grid;
  place-items: center;
  background: var(--accent-soft);
  color: var(--accent);
}
.fact-body {
  flex: 1;
  min-width: 0;
}
.fact-label {
  font-size: 12px;
  color: var(--muted);
}
.fact-value {
  font-size: 13px;
  color: var(--foreground);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.fact-value.mono {
  font-family: var(--font-mono);
  font-size: 12.5px;
}
.fact-value.is-empty {
  font-family: inherit;
  color: var(--warning);
}
.fact-value.is-on {
  color: var(--success);
}
.fact-link {
  display: inline-flex;
  align-items: center;
  flex-shrink: 0;
  font-size: 12.5px;
  color: var(--muted);
  text-decoration: none;
}
.fact-link:hover {
  color: var(--accent);
}

.note {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: 14px 0 0;
  font-size: 12.5px;
  line-height: 1.7;
  color: var(--muted);
}
.note :deep(svg) {
  flex-shrink: 0;
  margin-top: 4px;
}
.note a,
.emby a {
  color: var(--accent);
  text-decoration: none;
}
.note a:hover {
  text-decoration: underline;
}

/* ---- Emby 设置步骤（默认折起） ---- */
.emby {
  margin-top: 14px;
  border-radius: var(--r-lg);
  box-shadow: inset 0 0 0 1px color-mix(in oklab, var(--warning) 40%, transparent);
  background: color-mix(in oklab, var(--warning) 6%, transparent);
}
.emby summary {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 10px 12px;
  list-style: none;
  cursor: pointer;
  font-size: 13px;
  font-weight: 500;
  color: var(--warning-soft-foreground);
}
.emby summary::-webkit-details-marker {
  display: none;
}
.emby-chev {
  margin-left: auto;
  transition: transform 150ms ease;
}
.emby[open] .emby-chev {
  transform: rotate(90deg);
}
.emby-body {
  padding: 0 14px 12px;
  font-size: 12.5px;
  line-height: 1.75;
  color: var(--foreground);
}
.emby-body p {
  margin: 6px 0;
}
.emby-body ol {
  margin: 4px 0;
  padding-left: 20px;
}

/* ---- 保存条 ---- */
.save-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 8px 8px 16px;
  border-radius: var(--r-lg);
  box-shadow: inset 0 0 0 1px var(--border);
  transition:
    background-color 150ms ease,
    box-shadow 150ms ease;
}
.save-bar.is-dirty {
  background: color-mix(in oklab, var(--accent) 8%, transparent);
  box-shadow: inset 0 0 0 1px color-mix(in oklab, var(--accent) 40%, transparent);
}
.save-note {
  flex: 1;
  min-width: 0;
  font-size: 12.5px;
  color: var(--muted);
}
.is-dirty .save-note {
  color: var(--accent);
  font-weight: 500;
}

@media (max-width: 1280px) {
  .grid {
    grid-template-columns: 1fr;
  }
}
</style>
