<script setup lang="ts">
import { onMounted } from 'vue'
import { RouterLink } from 'vue-router'
import { ChevronRight } from '@lucide/vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import MetaFillPanel from '@/components/scrape/MetaFillPanel.vue'
import { useFullSetting } from '@/pages/strm/fullSetting'
import SaveBar from './SaveBar.vue'
import { useScrapeConfig } from './scrapeConfig'
import { useMediaServer } from '@/composables/mediaServer'

/**
 * 媒体信息：入库后让 Emby 提前探测（`scrape.probe_streams`，存在刮削配置里，但不是刮削）
 * 与定时的媒体信息补全（原在扩展功能）。两者都会经 302 取 115 直链，放在一起看得清代价。
 */
const media = useFullSetting()
const { cfg, saving, dirty, load, save } = useScrapeConfig()
const ms = useMediaServer()

onMounted(load)
</script>

<template>
  <!-- 左：入库后自动探测（全局开关）；右：定时补全。两张卡片各自保存，配置不是同一份 -->
  <div class="grid">
    <SectionCard title="入库后提前探测" hint="让 Emby 在第一次播放之前就拿到分辨率、音轨、内嵌字幕">
      <template #extra>
        <RouterLink :to="{ name: 'tasks', query: { tab: 'probe' } }" class="jump">
          探测失败清单<ChevronRight :size="13" />
        </RouterLink>
      </template>
      <FieldRow
        label="轨道探测"
        tip="入库后让 Emby 提前探测媒体信息（分辨率、音轨、内嵌字幕），第一次播放就不用现场探测，起播和第二次一样快。整理、同步入库确认后自动进行；在「海报墙」手动刮削时也可以给所选片目补上（所选视频超过 100 个要确认两次）。Emby 已有媒体信息的条目不碰。后台一次探一个、间隔 3 秒，每个条目会产生一次 115 直链请求。需要先在「系统配置 → Emby」配好服务器地址与 API 密钥。"
      >
        <HSegmented
          v-model="cfg.probe_streams"
          :disabled="!!ms.blocked('pre_probe')"
          :options="[{ label: '关闭', value: false }, { label: '开启', value: true }]"
        />
      </FieldRow>
      <p v-if="ms.blocked('pre_probe')" class="ms-block">{{ ms.name.value }} 下不可用：{{ ms.blocked('pre_probe') }}</p>

      <ul class="rules">
        <li>整理、同步入库确认后自动进行，只探本轮新入库的集</li>
        <li>Emby 已有媒体信息的条目不碰</li>
        <li>后台一次探一个、间隔 3 秒，每个条目一次 115 直链请求</li>
      </ul>

      <SaveBar
        class="bar"
        :dirty="dirty"
        :saving="saving"
        note="同一条目自动最多探 2 次、间隔 24 小时；失败的到任务中心手动重试"
        @save="save(media.model.value.local_path)"
      />
    </SectionCard>

    <MetaFillPanel />
  </div>
</template>

<style scoped>
.grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
  align-items: start;
}
.ms-block {
  margin: 6px 0 0;
  font-size: 12.5px;
  color: var(--warning);
}
.rules {
  margin: 6px 0 0;
  padding: 10px 14px 10px 30px;
  list-style: disc;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  font-size: 12.5px;
  line-height: 1.8;
  color: var(--muted);
}
.bar {
  margin-top: 14px;
}
.jump {
  display: inline-flex;
  align-items: center;
  font-size: 12.5px;
  color: var(--muted);
  text-decoration: none;
}
.jump:hover {
  color: var(--accent);
}

@media (max-width: 1280px) {
  .grid {
    grid-template-columns: 1fr;
  }
}
</style>
