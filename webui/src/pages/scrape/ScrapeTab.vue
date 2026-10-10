<script setup lang="ts">
import { onMounted } from 'vue'
import { RouterLink } from 'vue-router'
import { ChevronRight, HardDrive, Info, TriangleAlert, Upload } from '@lucide/vue'
import HAlert from '@/components/hero/HAlert.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import { useSetting } from '@/composables/useSetting'
import { useFullSetting } from '@/pages/strm/fullSetting'
import SaveBar from './SaveBar.vue'
import { useScrapeConfig } from './scrapeConfig'
import { useScrapeProvider } from '@/composables/scrapeProvider'

/**
 * 影视刮削配置。2026-10-04 从「自动整理」里拎出来成了独立页面的第一个页签；
 * 「轨道探测」管的是 Emby 提前探测、不是刮削，挪去了「媒体信息」页签。
 * 自动整理那边留一个只读概览（organize/ScrapeSummary.vue），要改跳到这里。
 */
const media = useFullSetting()
const monitor = useSetting('monitor', { enabled: false })
const { cfg, saving, dirty, load, save } = useScrapeConfig()
// 刮削方式为 Emby：整页置灰（inert：点不了、Tab 也进不去），后端同样不认这些设置
const { embyScrapes } = useScrapeProvider()

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
    <HAlert v-if="embyScrapes" status="accent">
      当前由 Emby 刮削，以下设置不生效：本站不写 NFO 与图片，整理、同步后也不会自动刮削。要改回来，在页面顶部选「本站刮削」。
    </HAlert>
    <div class="grid" :class="{ off: embyScrapes }" :inert="embyScrapes">
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
          tip="增量同步新生成 STRM 后（手机上传、网页端拖进媒体库等外部变更），刮削这些 STRM 所在的片目；整理入库的不经过这里。全量同步不触发，存量请用本页「媒体信息」页签里的媒体信息补全。目录名里有 TMDB 编号就按编号刮；没有时按目录名搜 TMDB，只认标题或原名完全相等的条目，认不准就跳过（任务详情里列出来，可在「海报墙」手动指定）。只认当前分类目录下的片目，一轮最多 50 部。"
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
            手动刮削在「<RouterLink :to="{ name: 'local' }">海报墙</RouterLink>」页勾选片目后点「刮削」，只处理已入库的片目；弹窗里的选项默认取左边这几项，可以只为那一次改。
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
              <b>想切回 Emby 自己刮削</b>：在页面顶部的「刮削方式」选「Emby 刮削」，再在 Emby 里勾回元数据下载器与图像获取器（TheMovieDb 等）。
              本站已写好的 NFO 与海报不会浪费：Nfo 读取器保持勾选时 Emby 先读现成的，只联网补缺；
              媒体目录里的海报 Emby 本来就会读，不需要为此打开「保存媒体图片到媒体文件夹中」。
              那个开关管的是 Emby <b>新下载</b>的图片写到哪 —— 要让「监控上传」把 Emby 的刮削结果回传 115 才需要打开。
              想让 Emby 完全按自己的重刮，对媒体库「刷新元数据」并选「替换所有元数据 / 图像」。
            </p>
          </div>
        </details>
      </SectionCard>
    </div>

    <SaveBar
      v-if="!embyScrapes"
      :dirty="dirty"
      :saving="saving"
      note="进度见顶栏任务队列；逐个文件的去向见实时日志（搜「[影视刮削]」）"
      @save="save(media.model.value.local_path)"
    />
  </div>
</template>

<style scoped>
.stack {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.grid.off {
  opacity: 0.5;
  filter: grayscale(0.6);
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

@media (max-width: 1280px) {
  .grid {
    grid-template-columns: 1fr;
  }
}
</style>
