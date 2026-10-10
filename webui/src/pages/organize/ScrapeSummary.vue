<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import { ArrowRight, ChevronRight } from '@lucide/vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import { organizeApi, pluginsApi } from '@/api'
import type { MetaFillInfo, PersonFillInfo } from '@/api/plugins'
import { useFullSetting } from '@/pages/strm/fullSetting'
import { SCRAPE_DEFAULTS, normalizeScrapeConfig } from '@/pages/scrape/scrapeConfig'
import { useScrapeProvider } from '@/composables/scrapeProvider'

/**
 * 「自动整理 → 影视刮削」只读概览。刮削配置 2026-10-04 起搬到独立的「影视刮削」页，
 * 这里只显示当前状态与跳转，**不放能改的开关**：两页各存一份整存整取会互相覆盖。
 */
const router = useRouter()
const media = useFullSetting()
const cfg = ref({ ...SCRAPE_DEFAULTS })
const metaFill = ref<MetaFillInfo | null>(null)
const personFill = ref<PersonFillInfo | null>(null)
const loading = ref(true)
const { embyScrapes, load: loadProvider } = useScrapeProvider()

onMounted(async () => {
  // 三份配置互不依赖，哪份读失败就按默认值显示，别让一份拖住整页
  void loadProvider(true)
  const [s, m, p] = await Promise.allSettled([
    organizeApi.getScrapeConfig(),
    pluginsApi.metaFillConfig(),
    pluginsApi.personFillConfig(),
  ])
  if (s.status === 'fulfilled') cfg.value = normalizeScrapeConfig(s.value.cfg)
  if (m.status === 'fulfilled') metaFill.value = m.value.data ?? null
  if (p.status === 'fulfilled') personFill.value = p.value.data ?? null
  loading.value = false
})

interface Item {
  label: string
  on: boolean
  text: string
  hint?: string
}
interface Group {
  title: string
  tab: string
  items: Item[]
}

const onOff = (on: boolean): Pick<Item, 'on' | 'text'> => ({ on, text: on ? '开启' : '关闭' })
const cronHint = (enabled?: boolean, cron?: string, next?: string) =>
  enabled ? [cron, next ? `下次 ${next}` : ''].filter(Boolean).join(' · ') : undefined

const groups = computed<Group[]>(() => {
  const c = cfg.value
  const mf = metaFill.value
  const pf = personFill.value
  // 刮削方式为 Emby：本站一样都不刮，刮削那两组只写一句「不生效」，别让人以为整理后还会刮
  const scrapeGroups: Group[] = embyScrapes.value
    ? [
        {
          title: '刮削方式',
          tab: 'scrape',
          items: [{ label: 'Emby 刮削', on: true, text: '由 Emby 刮削', hint: '本站不写 NFO 与图片，刮削设置不生效' }],
        },
      ]
    : [
        {
          title: '自动触发',
          tab: 'scrape',
          items: [
            { label: '整理后自动刮削', ...onOff(c.auto_after_organize), hint: '只刮本轮新入库的片目' },
            { label: '同步后自动刮削', ...onOff(c.auto_after_sync), hint: '增量同步新增的片目' },
          ],
        },
        {
          title: '刮削内容',
          tab: 'scrape',
          items: [
            { label: 'NFO 元数据', on: c.write_nfo, text: c.write_nfo ? '生成' : '跳过' },
            { label: '图片海报', on: c.write_images, text: c.write_images ? '生成' : '跳过' },
            { label: '覆盖模式', on: c.force, text: c.force ? '强制覆盖' : '只补缺失' },
            { label: '占位剧照', on: c.skip_shared_stills, text: c.skip_shared_stills ? '不写' : '照写' },
          ],
        },
      ]
  return [
    ...scrapeGroups,
    {
      title: '媒体信息与补全',
      tab: 'media',
      items: [
        { label: '轨道探测', ...onOff(c.probe_streams), hint: '入库后让 Emby 提前探测' },
        {
          label: '媒体信息补全',
          ...onOff(!!mf?.config.enabled),
          hint: cronHint(mf?.config.enabled, mf?.config.cron, mf?.next_run),
        },
      ],
    },
    {
      title: '演职人员',
      tab: 'people',
      items: [
        {
          label: '演职人员补全',
          ...onOff(!!pf?.config.enabled),
          hint: cronHint(pf?.config.enabled, pf?.config.cron, pf?.next_run),
        },
      ],
    },
  ]
})
</script>

<template>
  <SectionCard title="影视刮削" hint="刮削配置已移到独立的「影视刮削」页，这里只显示当前状态">
    <template #extra>
      <HButton variant="primary" size="sm" @click="router.push({ name: 'scrape' })">
        去修改
        <ArrowRight :size="14" />
      </HButton>
    </template>

    <div class="groups" :class="{ 'is-loading': loading }">
      <div v-for="g in groups" :key="g.title" class="group">
        <div class="group-head">
          <span class="group-title">{{ g.title }}</span>
          <RouterLink :to="{ name: 'scrape', query: g.tab === 'scrape' ? {} : { tab: g.tab } }" class="group-link">
            修改<ChevronRight :size="13" />
          </RouterLink>
        </div>
        <div v-for="it in g.items" :key="it.label" class="item">
          <div class="item-body">
            <div class="item-label">{{ it.label }}</div>
            <div v-if="it.hint" class="item-hint">{{ it.hint }}</div>
          </div>
          <HChip :color="it.on ? 'success' : 'default'">{{ it.text }}</HChip>
        </div>
      </div>
    </div>

    <p class="foot">
      写到本地：<span class="mono" :class="{ warn: !media.model.value.local_path }">{{ media.model.value.local_path || '未配置' }}</span>
    </p>
  </SectionCard>
</template>

<style scoped>
.groups {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 12px;
  transition: opacity 150ms ease;
}
.groups.is-loading {
  opacity: 0.5;
}
.group {
  padding: 4px 0;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.group-head {
  display: flex;
  align-items: center;
  padding: 8px 12px 4px;
}
.group-title {
  flex: 1;
  font-size: 12px;
  font-weight: 500;
  color: var(--muted);
}
.group-link {
  display: inline-flex;
  align-items: center;
  font-size: 12px;
  color: var(--muted);
  text-decoration: none;
}
.group-link:hover {
  color: var(--accent);
}
.item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 12px;
}
.item + .item {
  border-top: 1px solid var(--separator);
}
.item-body {
  flex: 1;
  min-width: 0;
}
.item-label {
  font-size: 13px;
  color: var(--foreground);
}
.item-hint {
  font-size: 12px;
  color: var(--muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.foot {
  margin: 14px 0 0;
  font-size: 12.5px;
  color: var(--muted);
}
.mono {
  font-family: var(--font-mono);
  color: var(--foreground);
}
.mono.warn {
  font-family: inherit;
  color: var(--warning);
}
</style>
