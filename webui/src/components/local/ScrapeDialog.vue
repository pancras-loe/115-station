<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HModal from '@/components/hero/HModal.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import TmdbPicker from '@/components/files/TmdbPicker.vue'
import { configApi, localApi, organizeApi } from '@/api'
import type { LocalTitle, ScrapeOptions } from '@/api/local'
import type { TmdbCandidate } from '@/api/resources'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { useQueueStore } from '@/stores/queue'

/**
 * 刮削本地媒体库里所选的片目。选项默认取「自动整理 → 影视刮削」里保存的配置，
 * 在这里改的只对这一次生效，不回写配置。
 *
 * 「上传到网盘」默认关：不勾时只写本地，回不回传网盘照常由「监控上传」决定；
 * 勾了就这一次当场传进网盘对应目录（不看监控上传开关）。
 *
 * preset：卡片菜单的「改指定 TMDB」直接把弹窗开在对应的选项上（连带打开强制覆盖）。
 */
const props = defineProps<{ targets: LocalTitle[]; preset?: 'auto' | 'pick' }>()
const show = defineModel<boolean>('show', { required: true })

const { message, dialog } = useFeedback()

/** 开着轨道探测时，所选视频超过这个数要确认两次（与后端 localProbeConfirmVideos 一致） */
const PROBE_CONFIRM_VIDEOS = 100
/** 手动探测的防抖（后端 embyextract.go：embyExtractDebounce） */
const PROBE_DEBOUNCE_MIN = 5

const queue = useQueueStore()

const DEFAULT_OPTS: ScrapeOptions = {
  write_nfo: true,
  write_images: true,
  force: false,
  upload: false,
  probe: false,
  skip_shared_stills: true,
}
const opts = ref<ScrapeOptions>({ ...DEFAULT_OPTS })
/** 已保存的配置（对照显示「与已保存不同」） */
const saved = ref<ScrapeOptions>({ ...opts.value })
const monitorOn = ref(false)
const loading = ref(false)
const submitting = ref(false)
const mode = ref<'auto' | 'pick'>('auto')
const picked = ref<TmdbCandidate | null>(null)

const single = computed(() => props.targets.length === 1)
const first = computed(() => props.targets[0])
const pickInitial = computed(() => (first.value ? `${first.value.title} ${first.value.year ?? ''}`.trim() : ''))

watch(show, async (v) => {
  if (!v) return
  mode.value = props.preset === 'pick' && single.value ? 'pick' : 'auto'
  picked.value = null
  loading.value = true
  try {
    const [sc, mon] = await Promise.all([
      organizeApi.getScrapeConfig(),
      configApi.getSetting<{ enabled: boolean }>('monitor', { enabled: false }),
    ])
    const c = sc.cfg ?? {}
    // 这两项后端缺省视为开启
    saved.value = {
      write_nfo: c.write_nfo !== false,
      write_images: c.write_images !== false,
      force: !!c.force,
      upload: false,
      probe: !!c.probe_streams,
      skip_shared_stills: c.skip_shared_stills !== false,
    }
    monitorOn.value = !!mon.enabled
  } catch {
    saved.value = { ...DEFAULT_OPTS }
  } finally {
    opts.value = { ...saved.value, force: saved.value.force || props.preset === 'pick' }
    loading.value = false
  }
})

const changed = computed(() => (Object.keys(saved.value) as (keyof ScrapeOptions)[]).some((k) => saved.value[k] !== opts.value[k]))

const canSubmit = computed(
  () =>
    props.targets.length > 0 &&
    (opts.value.write_nfo || opts.value.write_images) &&
    (mode.value === 'auto' || !!picked.value) &&
    !loading.value,
)

const uploadHint = computed(() =>
  opts.value.upload
    ? '这一次生成的文件写入本地后，直接传进网盘里对应的片目目录（不看监控上传开关）。'
    : `只写本地媒体库；回不回传网盘由「监控上传」决定（当前${monitorOn.value ? '已开启，会随后自动上传' : '未开启，不会上传'}）。`,
)

const totalVideos = computed(() => props.targets.reduce((n, t) => n + (t.videos || 0), 0))

/**
 * 轨道探测 = 让 Emby 逐个探测还没有媒体信息的视频，每个都是一次 115 直链请求。
 * 量大时确认两次：第一次说清数量与代价，第二次再问一遍，免得顺手点过去
 */
async function confirmProbe(): Promise<boolean> {
  const n = totalVideos.value
  const first = await dialog.confirm({
    title: '确认开启轨道探测？',
    content: `所选 ${props.targets.length} 部共 ${n} 个视频。刮完后会让 Emby 逐个探测其中还没有媒体信息的视频，每个都会产生一次 115 直链请求，后台一次一个、间隔 3 秒，最多要 ${Math.ceil((n * 3) / 60)} 分钟以上。请求过多有触发 115 风控的风险。`,
    actions: [
      { label: '取消', value: false, variant: 'tertiary' },
      { label: '继续', value: true, variant: 'primary' },
    ],
  })
  if (!first) return false
  const second = await dialog.confirm({
    title: '再次确认',
    content: `确定要对这 ${n} 个视频请求 Emby 提前探测吗？不需要的话可以取消，把「轨道探测」关掉再刮削。`,
    actions: [
      { label: '取消', value: false, variant: 'tertiary' },
      { label: '确定探测', value: true, variant: 'danger' },
    ],
  })
  return !!second
}

async function submit() {
  if (!canSubmit.value) return
  const needConfirm = opts.value.probe && totalVideos.value > PROBE_CONFIRM_VIDEOS
  if (needConfirm && !(await confirmProbe())) return
  submitting.value = true
  try {
    const pick = mode.value === 'pick' ? picked.value : null
    const d = await localApi.scrape({
      keys: props.targets.map((t) => t.key),
      scrape: opts.value,
      ...(pick ? { tmdb_id: pick.id, media_type: pick.media_type, label: `${pick.title} (${pick.year ?? ''})` } : {}),
      ...(needConfirm ? { confirm_probe: true } : {}),
    })
    message.success(d.message || '刮削已加入任务队列')
    await queue.submitted(d.job_id)
    show.value = false
  } catch (e) {
    toastError(e, '提交失败')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <HModal v-model:show="show" title="刮削" width="620px">
    <div class="body">
      <p class="src">
        所选：<b>{{ first?.title }}<template v-if="first?.year"> ({{ first.year }})</template></b>
        <span v-if="!single"> 等 {{ targets.length }} 部</span>
      </p>

      <FieldRow label="识别方式">
        <HSegmented
          v-model="mode"
          :options="[
            { label: '按目录名', value: 'auto' },
            { label: '指定 TMDB 条目', value: 'pick', disabled: !single },
          ]"
        />
      </FieldRow>
      <p v-if="mode === 'auto'" class="note">
        按片目目录名里的 TMDB 编号（如 <code>{tmdbid=编号}</code>）取条目，没有编号的按片名识别。
        <template v-if="!single">指定条目只能单选一部。</template>
      </p>
      <template v-else>
        <TmdbPicker v-model="picked" :initial="pickInitial" />
        <p class="note">只换这一次刮削用的条目，不改目录名；目录名里的编号对不上时，建议到整理记录里「重新整理」。</p>
      </template>

      <div class="opts" :aria-busy="loading">
        <FieldRow label="NFO 元数据">
          <HSegmented v-model="opts.write_nfo" :options="[{ label: '生成', value: true }, { label: '跳过', value: false }]" />
        </FieldRow>
        <FieldRow label="图片海报">
          <HSegmented v-model="opts.write_images" :options="[{ label: '生成', value: true }, { label: '跳过', value: false }]" />
        </FieldRow>
        <FieldRow label="覆盖模式">
          <HSegmented v-model="opts.force" :options="[{ label: '只补缺失', value: false }, { label: '强制覆盖', value: true }]" />
        </FieldRow>
        <FieldRow label="占位剧照" hint="同一季里 3 集以上共用一张剧照时判为占位图，这些集不写集剧照。">
          <HSegmented
            v-model="opts.skip_shared_stills"
            :options="[{ label: '不写', value: true }, { label: '照写', value: false }]"
          />
        </FieldRow>
        <FieldRow
          label="轨道探测"
          :hint="`刮完让 Emby 给这些片目里还没有媒体信息的条目提前探测，第一次播放更快。后台逐个进行，每个条目一次 115 直链请求。所选共 ${totalVideos} 个视频${totalVideos > PROBE_CONFIRM_VIDEOS ? '，提交时需要确认两次' : ''}。`"
        >
          <HSegmented v-model="opts.probe" :options="[{ label: '关闭', value: false }, { label: '开启', value: true }]" />
        </FieldRow>
        <HAlert v-if="opts.probe" status="accent" class="probe-note">
          刮削结束后会另建一个「Emby 探测」任务，进度和每集结果在任务中心，失败的可以重试。
          已有媒体信息的不会再探；这是手动探测，不受入库后自动探测的次数限制，只是同一视频 {{ PROBE_DEBOUNCE_MIN }} 分钟内不重复请求（免得重复取 115 直链）。
        </HAlert>
        <FieldRow label="上传到网盘" :hint="uploadHint">
          <HSwitch v-model="opts.upload" aria-label="上传到网盘" />
        </FieldRow>
      </div>

      <HAlert v-if="!opts.write_nfo && !opts.write_images" status="warning">NFO 与图片至少要生成一项。</HAlert>
      <HAlert v-else-if="mode === 'pick' && !opts.force" status="warning">
        换了 TMDB 条目却只补缺失：已有的 NFO / 海报不会被替换。
      </HAlert>
      <HAlert v-else-if="opts.force && opts.upload" status="warning">
        强制覆盖会把网盘里同名的旧 NFO / 图片先送进回收站，再上传新的。
      </HAlert>
    </div>

    <template #footer>
      <span class="foot-note">{{ changed ? '已改动的选项只对这一次生效，不会保存到刮削配置。' : '默认按已保存的刮削配置。' }}</span>
      <div class="foot-btns">
        <HButton variant="tertiary" @click="show = false">取消</HButton>
        <HButton variant="primary" :disabled="!canSubmit" :loading="submitting" @click="submit">加入队列刮削</HButton>
      </div>
    </template>
  </HModal>
</template>

<style scoped>
.body {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.src {
  margin: 0;
  font-size: 12.5px;
  color: var(--muted);
  word-break: break-all;
}
.src b {
  color: var(--foreground);
  font-weight: 500;
}
.note {
  margin: -4px 0 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--muted);
}
.opts {
  display: flex;
  flex-direction: column;
}
.probe-note {
  margin: 2px 0 6px;
  font-size: 12px;
  line-height: 1.6;
}
.foot-note {
  margin-right: auto;
  flex: 1 1 220px;
  font-size: 12px;
  color: var(--muted);
}
.foot-btns {
  display: flex;
  gap: 8px;
}
@media (max-width: 639px) {
  .foot-note {
    flex-basis: 100%;
  }
  .foot-btns {
    width: 100%;
  }
  .foot-btns > * {
    flex: 1;
  }
}
</style>
