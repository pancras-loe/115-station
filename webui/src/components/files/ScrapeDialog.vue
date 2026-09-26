<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HModal from '@/components/hero/HModal.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import TmdbPicker from './TmdbPicker.vue'
import { configApi, filesApi, organizeApi } from '@/api'
import type { FileJobBody, ScrapeOptions } from '@/api/files'
import type { TmdbCandidate } from '@/api/resources'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { useQueueStore } from '@/stores/queue'

/**
 * 刮削所选条目。选项默认取「自动整理 → 影视刮削」里保存的配置、「上传到网盘」默认取监控上传总开关，
 * 在这里改的只对这一次生效，不回写配置。
 *
 * inLibrary=false（待整理 / 冗余 / 任意目录）时本地没有对应片目，产物只能直接写进网盘，
 * 所以上传锁定为开。
 */
const props = defineProps<{ body: FileJobBody | null; inLibrary: boolean }>()
const show = defineModel<boolean>('show', { required: true })

const { message } = useFeedback()
const queue = useQueueStore()

const opts = ref<ScrapeOptions>({ write_nfo: true, write_images: true, force: false, upload: false })
/** 已保存的配置（对照显示「与已保存不同」） */
const saved = ref<ScrapeOptions>({ ...opts.value })
const loading = ref(false)
const submitting = ref(false)
const mode = ref<'auto' | 'pick'>('auto')
const picked = ref<TmdbCandidate | null>(null)

const single = computed(() => (props.body?.items.length ?? 0) === 1)
const firstName = computed(() => props.body?.items[0]?.name ?? '')

watch(show, async (v) => {
  if (!v) return
  mode.value = 'auto'
  picked.value = null
  loading.value = true
  try {
    const [sc, mon] = await Promise.all([
      organizeApi.getScrapeConfig(),
      configApi.getSetting<{ enabled: boolean }>('monitor', { enabled: false }),
    ])
    const c = sc.cfg ?? {}
    saved.value = {
      // 这两项后端缺省视为开启
      write_nfo: c.write_nfo !== false,
      write_images: c.write_images !== false,
      force: !!c.force,
      upload: !!mon.enabled,
    }
  } catch {
    saved.value = { write_nfo: true, write_images: true, force: false, upload: false }
  } finally {
    opts.value = { ...saved.value, upload: saved.value.upload || !props.inLibrary }
    loading.value = false
  }
})

const changed = computed(() => (Object.keys(saved.value) as (keyof ScrapeOptions)[]).some((k) => saved.value[k] !== opts.value[k]))

const canSubmit = computed(
  () =>
    !!props.body &&
    (opts.value.write_nfo || opts.value.write_images) &&
    (mode.value === 'auto' || !!picked.value) &&
    !loading.value,
)

async function submit() {
  if (!props.body || !canSubmit.value) return
  submitting.value = true
  try {
    const pick = mode.value === 'pick' ? picked.value : null
    const d = await filesApi.scrape({
      ...props.body,
      scrape: opts.value,
      ...(pick ? { tmdb_id: pick.id, media_type: pick.media_type, label: `${pick.title} (${pick.year ?? ''})` } : {}),
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
        所选：<b>{{ firstName }}</b>
        <span v-if="!single"> 等 {{ body?.items.length }} 项</span>
      </p>

      <FieldRow label="识别方式">
        <HSegmented
          v-model="mode"
          :options="[
            { label: '自动识别', value: 'auto' },
            { label: '指定 TMDB 条目', value: 'pick', disabled: !single },
          ]"
        />
      </FieldRow>
      <p v-if="mode === 'auto'" class="note">
        媒体库里的片目按目录名里的 <code>[tmdb=编号]</code> 取条目，没有编号的按片名识别；
        不在媒体库里的文件夹按一部影片识别。<template v-if="!single">指定条目只能单选一项。</template>
      </p>
      <TmdbPicker v-else v-model="picked" :initial="firstName" />

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
        <FieldRow
          label="上传到网盘"
          :hint="
            inLibrary
              ? '写入本地媒体库后，把这一次生成的文件直接传进网盘对应目录；不勾则只写本地，监控上传也不会再传它们。'
              : '所选不在媒体库里，本地没有对应片目：产物只能直接写进网盘里的这个目录。'
          "
        >
          <HSwitch v-model="opts.upload" :disabled="!inLibrary" aria-label="上传到网盘" />
        </FieldRow>
      </div>

      <HAlert v-if="!opts.write_nfo && !opts.write_images" status="warning">NFO 与图片至少要生成一项。</HAlert>
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
