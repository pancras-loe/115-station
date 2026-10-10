<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HModal from '@/components/hero/HModal.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSpinner from '@/components/hero/HSpinner.vue'
import { localApi } from '@/api'
import type { ReconBucket, ReconcileResult, ReconSide } from '@/api/local'
import { num } from '@/utils/format'

/**
 * Emby 与海报墙对账。
 *
 * 总览的电影 / 剧集数读 Emby（全服务器），海报墙数台账片目（只认当前分类规则下的标题目录），
 * 两个口径天然可能不一致。这里把差值拆成几类原因、各自列出条目，让用户知道该去哪儿改，
 * 而不是对着两个数字干猜。只读：后端只打 Emby，零 115 请求。
 * 路径映射对不上时分类没有意义，只给映射建议，跳去 Emby 设置由用户确认保存。
 */
const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ 'update:show': [boolean] }>()
const router = useRouter()

const loading = ref(false)
const failed = ref('')
const data = ref<ReconcileResult | null>(null)
const side = ref<'tv' | 'movie'>('tv')

async function load() {
  loading.value = true
  failed.value = ''
  try {
    const d = await localApi.reconcile()
    data.value = d
    const r = d.result
    // 默认看差得多的那一类
    if (r) side.value = Math.abs(r.movie.emby - r.movie.ledger) > Math.abs(r.tv.emby - r.tv.ledger) ? 'movie' : 'tv'
  } catch (e) {
    failed.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

watch(
  () => props.show,
  (v) => {
    if (v) load()
  },
)

const result = computed(() => data.value?.result)
const cur = computed<ReconSide | undefined>(() => result.value?.[side.value])
const sideOptions = computed(() => {
  const r = result.value
  const label = (name: string, s?: ReconSide) => (s && s.emby !== s.ledger ? `${name} · 差 ${s.emby - s.ledger}` : name)
  return [
    { label: label('剧集', r?.tv), value: 'tv' as const },
    { label: label('电影', r?.movie), value: 'movie' as const },
  ]
})

interface Reason {
  id: string
  /** 对差值的贡献：Emby 多出来的为正，Emby 少的为负 */
  sign: 1 | -1
  title: string
  advice: string
  bucket: ReconBucket
  /** 计数与 bucket.count 不同时（拆分条目算的是多出来的个数） */
  amount?: number
}

const reasons = computed<Reason[]>(() => {
  const s = cur.value
  if (!s) return []
  const tv = side.value === 'tv'
  const list: Reason[] = [
    {
      id: 'uncategorized',
      sign: 1,
      title: '不在当前分类规则里',
      advice:
        '这些片子的目录在本地媒体库里，但上一层目录不是「自动整理 → 二级分类策略」里的任何一个分类，海报墙不把它们当片目。把下面这些目录名加进分类策略（或把片子挪进已有分类）就能对上。',
      bucket: s.uncategorized,
    },
    {
      id: 'stale',
      sign: 1,
      title: 'Emby 残留',
      advice: '本地已经没有这个目录了，Emby 还留着条目。到 Emby 里对所在媒体库「扫描媒体库文件」即可清掉。',
      bucket: s.stale,
    },
    {
      id: 'outside',
      sign: 1,
      title: '不在本站媒体库目录下',
      advice: 'Emby 另外挂的媒体库（别的硬盘 / 目录），不归本站管，海报墙本来就不显示，不用处理。',
      bucket: s.outside,
    },
    {
      id: 'unledgered',
      sign: 1,
      title: '本地有、同步台账里没有',
      advice: '目录在分类下、本地也在，但不是本站同步 / 整理生成的 STRM（或台账丢了）。跑一次全量同步可以把本站管的那部分补进台账。',
      bucket: s.unledgered,
    },
    {
      id: 'type_mismatch',
      sign: 1,
      title: tv ? '台账算电影、Emby 认成剧集' : '台账算剧集、Emby 认成电影',
      advice: '多半是分类目录放进了另一种类型的 Emby 媒体库，或同名分类电影和剧集都有。检查 Emby 媒体库的内容类型与分类策略。',
      bucket: s.type_mismatch,
    },
    {
      id: 'split',
      sign: 1,
      title: tv ? '一部剧被 Emby 拆成多个条目' : '一部电影在 Emby 里有多个条目',
      advice: tv
        ? 'Emby 没认出季目录（如「第一季 1080p」「S1」），把每个子目录当成一部剧。把季目录改成「Season 1」这类标准名后重新扫描。'
        : '同一目录下的多个版本，Emby 记成几个 Movie 条目、界面上合成一部切换版本，属于正常现象。',
      bucket: s.split,
      amount: s.split_extra,
    },
    {
      id: 'missing',
      sign: -1,
      title: '海报墙有、Emby 里没有',
      advice: 'Emby 还没扫到，或者所在分类目录没加进 Emby 媒体库。到 Emby 里扫描对应媒体库，或在 Emby 新建 / 补上媒体库路径。',
      bucket: s.missing,
    },
  ]
  return list.filter((r) => (r.amount ?? r.bucket.count) > 0)
})

const mapping = computed(() => data.value?.mapping)

function goSettings() {
  emit('update:show', false)
  router.push({ name: 'settings', query: { tab: 'emby', emby_root: mapping.value?.suggest || undefined } })
}
</script>

<template>
  <HModal :show="show" title="Emby 与海报墙对账" width="720px" @update:show="emit('update:show', $event)">
    <div class="body">
      <p class="intro">
        总览的数量读的是 Emby（全服务器），海报墙数的是本站台账里、当前分类规则下的片目。这里把 Emby 的每个电影 / 剧集换算回本地路径，
        逐个说明两边为什么对不上。只读，不改任何东西。
      </p>

      <div v-if="loading" class="center"><HSpinner size="sm" /><span>正在读取 Emby 条目…</span></div>

      <HAlert v-else-if="failed" status="danger">{{ failed }}</HAlert>

      <HAlert v-else-if="data && !data.configured" status="warning">还没有配置 Emby 服务器，或本地媒体库目录为空。</HAlert>

      <template v-else-if="result && !result.mapping_ok">
        <HAlert status="warning" title="路径映射对不上">
          Emby 里读到 {{ num(result.scanned) }} 个电影 / 剧集，换算后一个都不在本地片目里。先把「系统配置 → Emby」里的路径映射填对，对账才有意义。
        </HAlert>
        <div v-if="mapping" class="map">
          <div class="map-row"><span class="k">本地媒体库目录</span><code>{{ mapping.local_root || '未配置' }}</code></div>
          <div class="map-row"><span class="k">当前 Emby 媒体库目录</span><code>{{ mapping.current || '未填' }}</code></div>
          <div v-if="mapping.suggest" class="map-row">
            <span class="k">推算应填</span><code class="hl">{{ mapping.suggest }}</code>
          </div>
          <ul v-if="mapping.evidence?.length" class="evidence">
            <li v-for="e in mapping.evidence" :key="e.location">
              Emby <code>{{ e.location }}</code> ⇄ 本地 <code>{{ e.local }}</code>
            </li>
          </ul>
          <p v-if="mapping.error" class="note err">{{ mapping.error }}</p>
          <p v-else-if="!mapping.suggest" class="note">
            推算不出来：Emby 媒体库的目录在本地媒体库根下找不到同名目录。请对照 Emby 媒体库的路径手动填写。
          </p>
          <div v-if="mapping.libraries?.length" class="libs">
            <div class="k">Emby 媒体库</div>
            <div v-for="l in mapping.libraries" :key="l.name" class="lib">
              <b>{{ l.name }}</b>
              <code v-for="loc in l.locations" :key="loc">{{ loc }}</code>
            </div>
          </div>
        </div>
      </template>

      <template v-else-if="result && cur">
        <HSegmented v-model="side" :options="sideOptions" size="sm" aria-label="类型" />

        <div class="stats">
          <div class="cell"><span class="k">Emby</span><span class="v">{{ num(cur.emby) }}</span></div>
          <div class="cell"><span class="k">海报墙</span><span class="v">{{ num(cur.ledger) }}</span></div>
          <div class="cell"><span class="k">两边对上</span><span class="v ok">{{ num(cur.matched) }}</span></div>
          <div class="cell">
            <span class="k">相差</span>
            <span class="v" :class="{ warn: cur.emby !== cur.ledger }">{{ cur.emby - cur.ledger > 0 ? '+' : '' }}{{ num(cur.emby - cur.ledger) }}</span>
          </div>
        </div>

        <HAlert v-if="!reasons.length" status="success">两边完全一致。</HAlert>

        <details v-for="r in reasons" :key="r.id" class="reason" :open="reasons.length <= 2">
          <summary>
            <span class="delta" :class="r.sign > 0 ? 'plus' : 'minus'">{{ r.sign > 0 ? '+' : '−' }}{{ num(r.amount ?? r.bucket.count) }}</span>
            <span class="r-title">{{ r.title }}</span>
          </summary>
          <p class="advice">{{ r.advice }}</p>

          <ul v-if="r.id === 'uncategorized' && cur.uncategorized_dirs?.length" class="dirs">
            <li v-for="d in cur.uncategorized_dirs" :key="d.dir">
              <code>{{ d.dir || '（媒体库根）' }}</code>
              <span class="cnt">{{ num(d.count) }} 部</span>
              <span class="samples">{{ d.samples.join('、') }}{{ d.count > d.samples.length ? ' …' : '' }}</span>
            </li>
          </ul>
          <ul v-else class="items">
            <li v-for="(e, i) in r.bucket.items" :key="i">
              <span class="name">{{ e.name }}<span v-if="e.year" class="year"> ({{ e.year }})</span></span>
              <span v-if="e.count" class="cnt">{{ e.count }} 个条目</span>
              <span class="path">{{ e.key || e.local || e.path }}</span>
            </li>
            <li v-if="r.bucket.count > r.bucket.items.length" class="more">
              还有 {{ num(r.bucket.count - r.bucket.items.length) }} 条未列出
            </li>
          </ul>
        </details>
      </template>
    </div>

    <template #footer>
      <div class="footer">
        <HButton variant="ghost" @click="emit('update:show', false)">关闭</HButton>
        <HButton v-if="result && !result.mapping_ok" variant="primary" @click="goSettings">
          {{ mapping?.suggest ? '去设置（已带上推算值）' : '去 Emby 设置' }}
        </HButton>
        <HButton v-else-if="result" variant="tertiary" :loading="loading" @click="load">重新对账</HButton>
      </div>
    </template>
  </HModal>
</template>

<style scoped>
.body {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.intro {
  margin: 0;
  font-size: 12.5px;
  line-height: 1.7;
  color: var(--muted);
}
.center {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 26px 0;
  font-size: 12.5px;
  color: var(--muted);
}
.k {
  font-size: 11.5px;
  color: var(--muted);
}
code {
  font-size: 12px;
  word-break: break-all;
  color: var(--foreground);
}
code.hl {
  color: var(--accent);
  font-weight: 600;
}

.map {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px 14px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.map-row {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 8px;
}
.map-row .k {
  min-width: 8em;
}
.evidence,
.dirs,
.items {
  margin: 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 6px;
  font-size: 12px;
}
.evidence {
  color: var(--muted);
}
.note {
  margin: 0;
  font-size: 12px;
  color: var(--muted);
}
.note.err {
  color: var(--danger);
}
.libs {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 12px;
}
.lib {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 10px;
}

.stats {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 10px;
}
.cell {
  display: flex;
  flex-direction: column;
  gap: 3px;
  padding: 10px 12px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.v {
  font-size: 18px;
  font-weight: 600;
  color: var(--foreground);
  font-variant-numeric: tabular-nums;
}
.v.ok {
  color: var(--success);
}
.v.warn {
  color: var(--warning);
}

.reason {
  padding: 10px 14px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
}
.reason summary {
  display: flex;
  align-items: center;
  gap: 10px;
  cursor: pointer;
  font-size: 13px;
  color: var(--foreground);
}
.delta {
  min-width: 3.2em;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}
.delta.plus {
  color: var(--warning);
}
.delta.minus {
  color: var(--danger);
}
.advice {
  margin: 8px 0;
  font-size: 12px;
  line-height: 1.7;
  color: var(--muted);
}
.items li,
.dirs li {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 4px 10px;
}
.name {
  color: var(--foreground);
}
.year,
.cnt {
  color: var(--muted);
}
.path,
.samples {
  flex-basis: 100%;
  color: var(--muted);
  word-break: break-all;
}
.more {
  color: var(--muted);
}

.footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

@media (max-width: 720px) {
  .stats {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>
