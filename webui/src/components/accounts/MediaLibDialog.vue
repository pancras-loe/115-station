<script setup lang="ts">
/**
 * 新建 / 修改一个媒体库（MULTI-ACCOUNT-V2-PLAN.md §10.1）：
 * 名称 → 115 网盘目录（连同账号）→ 本地目录（媒体根下）→ 转存/待整理文件夹 → 分类方案 → 刮削 → 上传到 115。
 * 存一次只动这一个库，校验在后端（libapiv2.go，整组互不包含）。
 */
import { computed, ref, watch } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import HModal from '@/components/hero/HModal.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSelect from '@/components/hero/HSelect.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import Cid115Input, { type CidValue } from '@/components/Cid115Input.vue'
import { libraryApi } from '@/api'
import type { MediaLib, MediaLibReq, MediaLibsView } from '@/api/library'
import type { LibScrape } from '@/api/organize'
import { toastError, useFeedback } from '@/composables/useFeedback'

const props = defineProps<{ view: MediaLibsView; lib: MediaLib | null }>()
const show = defineModel<boolean>('show', { default: false })
const emit = defineEmits<{ saved: [MediaLibsView] }>()
const { message } = useFeedback()

const form = ref<MediaLibReq>(blank())
const dir = ref<CidValue>({ cid: '', path: '' })
const staging = ref<CidValue>({ cid: '', path: '' })
const saving = ref(false)
const dirInput = ref<InstanceType<typeof Cid115Input> | null>(null)
const stagingInput = ref<InstanceType<typeof Cid115Input> | null>(null)

function blank(): MediaLibReq {
  return { name: '', account: 0, cid: '', path: '', local: '', staging_cid: '', staging_path: '', scheme_id: 0, depth: 1, scrape: 'manual', upload: false }
}

watch(show, (v) => {
  if (!v) return
  const l = props.lib
  form.value = l
    ? {
        name: l.name,
        account: l.account,
        cid: l.cid,
        path: l.path,
        local: l.local,
        staging_cid: l.staging_cid,
        staging_path: l.staging_path,
        scheme_id: l.scheme_id,
        depth: l.depth || 1,
        scrape: l.scrape || 'manual',
        upload: !!l.upload,
      }
    : { ...blank(), scheme_id: props.view.schemes[0]?.id ?? 0 }
  dir.value = { cid: form.value.cid, path: form.value.path || form.value.cid }
  staging.value = { cid: form.value.staging_cid, path: form.value.staging_path || form.value.staging_cid }
})

const isNew = computed(() => !props.lib)
const localLocked = computed(() => !!props.lib?.local_locked)
const accountOpts = computed(() => props.view.accounts.map((a) => ({ label: a.main ? `${a.name}（主账号）` : a.name, value: a.id })))
/** 只有一个账号时不显示；已经是别的账号的库照常显示 */
const showAccount = computed(() => props.view.accounts.length > 1 || form.value.account !== 0)
const schemeOpts = computed(() => [
  { label: '不分类（片目直接放在库目录下）', value: 0 },
  ...props.view.schemes.map((s) => ({ label: `${s.name}（电影 ${s.movie} / 剧集 ${s.tv} 条）`, value: s.id })),
])
const SCRAPE_OPTS = [
  { label: '自动刮新片', value: 'auto' },
  { label: '只手动刮', value: 'manual' },
  { label: '不刮', value: 'off' },
]
const UPLOAD_OPTS = [
  { label: '回传', value: true },
  { label: '不传（推荐）', value: false },
]
const DEPTH_OPTS = [
  { label: '库 / 片目', value: 1 },
  { label: '库 / 分类 / 片目', value: 2 },
  { label: '库 / 分类 / 子分类 / 片目', value: 3 },
]

/** 选了目录、名称还空着：拿目录名当默认名称与本地目录 */
watch(
  () => dir.value.path,
  (p) => {
    const base = (p || '').split('/').filter(Boolean).pop() || ''
    if (!base || /^\d+$/.test(base)) return
    if (!form.value.name.trim()) form.value.name = base
    if (isNew.value && !form.value.local.trim()) form.value.local = base
  },
)

function setAccount(v: number) {
  if (v === form.value.account) return
  form.value.account = v
  // 换了账号：目录都是那个账号里的，原来选的作废
  dir.value = { cid: '', path: '' }
  staging.value = { cid: '', path: '' }
}

const localPreview = computed(() => {
  const l = form.value.local.trim().replace(/\\/g, '/').replace(/^\/+|\/+$/g, '')
  return l ? `${props.view.media_root}/${l}` : ''
})

async function submit() {
  const cid = (await dirInput.value?.ensureCid()) ?? dir.value.cid
  if (!cid || cid === '0') {
    message.error('选一个具体的 115 目录（不能是网盘根）')
    return
  }
  let stagingCid = ''
  if (staging.value.path.trim()) {
    stagingCid = (await stagingInput.value?.ensureCid()) ?? staging.value.cid
    if (!stagingCid) {
      message.error('转存/待整理文件夹的路径认不出来：点「选择目录」重新选')
      return
    }
  }
  const req: MediaLibReq = {
    ...form.value,
    cid,
    path: /^\d+$/.test(dir.value.path) ? '' : dir.value.path,
    staging_cid: stagingCid,
    staging_path: stagingCid && !/^\d+$/.test(staging.value.path) ? staging.value.path : '',
    depth: form.value.scheme_id ? 0 : form.value.depth,
  }
  saving.value = true
  try {
    const v = props.lib ? await libraryApi.updateMediaLib(props.lib.id, req) : await libraryApi.createMediaLib(req)
    message.success(props.lib ? '已保存' : `已添加媒体库「${req.name}」：跑一次全量同步就能把它的 STRM 生成出来`)
    emit('saved', v)
    show.value = false
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <HModal v-model:show="show" :title="isNew ? '添加媒体库' : `修改「${lib?.name}」`" width="640px" persistent>
    <div class="form">
      <FieldRow label="名称" required tip="给人看的名字，各库不能重名。">
        <HInput v-model="form.name" placeholder="如 电影" :input-attrs="{ 'aria-label': '媒体库名称' }" />
      </FieldRow>

      <FieldRow v-if="showAccount" label="账号" tip="这个库在哪个 115 账号上。115 网盘目录和转存文件夹都是这个账号里的。">
        <HSelect
          :model-value="form.account"
          :options="accountOpts"
          :disabled="localLocked"
          aria-label="账号"
          @update:model-value="(v) => setAccount(Number(v))"
        />
      </FieldRow>

      <FieldRow label="115 网盘目录" required tip="同步这个目录生成 STRM；本站整理的片也放进这里。和别的库不能互相包含。">
        <Cid115Input :key="`dir-${form.account}`" ref="dirInput" v-model="dir" :account="form.account || undefined" placeholder="如 /影视/电影" />
      </FieldRow>

      <FieldRow
        label="本地目录"
        required
        :hint="localLocked ? '本地已经有同步的内容，不能再改' : localPreview ? `STRM 生成在 ${localPreview}` : ''"
        tip="在本地媒体根下的位置，可以多级。几个账号的库想进 Emby 的同一个库，就把它们放在同一个上级下（/media/电影/主号、/media/电影/副号，Emby 的「电影」库指向 /media/电影）。"
      >
        <div class="local-row">
          <span class="root mono">{{ view.media_root }}/</span>
          <HInput v-model="form.local" :disabled="localLocked" placeholder="如 电影 或 电影/主号" :input-attrs="{ 'aria-label': '本地目录' }" />
        </div>
      </FieldRow>

      <FieldRow
        label="转存/待整理文件夹"
        tip="放进这个文件夹的内容，整理进这个库。不设 = 这个库只同步 STRM，本站不往里整理。"
        :hint="staging.path ? '' : '不设：只同步 STRM，本站不往里整理'"
      >
        <Cid115Input
          :key="`staging-${form.account}`"
          ref="stagingInput"
          v-model="staging"
          :account="form.account || undefined"
          placeholder="如 /转存/电影（留空 = 只同步）"
        />
      </FieldRow>

      <FieldRow label="二级分类" tip="整理时按哪套分类方案分目录（方案在「自动整理 → 二级分类」里管）。不分类 = 片目直接放在库目录下。">
        <HSelect v-model="form.scheme_id" :options="schemeOpts" aria-label="分类方案" />
      </FieldRow>

      <FieldRow v-if="!form.scheme_id" label="片目在" tip="海报墙、刮削按这一层认「一部片」。本站整理进来的片都在第一层；只同步的库按你自己的目录结构选。">
        <HSelect v-model="form.depth" :options="DEPTH_OPTS" aria-label="片目在第几层" />
      </FieldRow>

      <FieldRow label="刮削" tip="自动刮新片：整理进来、同步进来的新片都刮；只手动刮：在海报墙勾选后刮；不刮：交给 Emby。">
        <HSegmented :model-value="form.scrape" :options="SCRAPE_OPTS" size="sm" @update:model-value="(v) => (form.scrape = v as LibScrape)" />
      </FieldRow>

      <FieldRow
        label="上传到 115"
        tip="监控这个库的本地目录，本站或 Emby 新写的 NFO 与标准命名的图片（poster / fanart / seasonXX-poster 等）按相对路径传进这个库的 115 目录，用的是这个库所在账号。上传是 115 风控敏感操作，默认不传。"
        :hint="form.upload ? '每分钟扫一次本地目录，最近 24 小时新产生的文件才传' : '只写本地，不往网盘写'"
      >
        <HSegmented :model-value="form.upload" :options="UPLOAD_OPTS" size="sm" @update:model-value="(v) => (form.upload = v as boolean)" />
      </FieldRow>
    </div>
    <template #footer>
      <HButton variant="tertiary" @click="show = false">取消</HButton>
      <HButton variant="primary" :loading="saving" @click="submit">{{ isNew ? '添加' : '保存' }}</HButton>
    </template>
  </HModal>
</template>

<style scoped>
.form {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.local-row {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
}
.local-row .root {
  flex-shrink: 0;
  color: var(--muted);
  font-family: var(--font-mono);
  font-size: 13px;
}
</style>
