<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import HNumberInput from '@/components/hero/HNumberInput.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import HSelect from '@/components/hero/HSelect.vue'
import HTabs from '@/components/hero/HTabs.vue'
import { ChevronRight, QrCode } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import AccountHero from '@/components/accounts/AccountHero.vue'
import StagingLegacyAlert from '@/components/accounts/StagingLegacyAlert.vue'
import MediaLibs from '@/components/accounts/MediaLibs.vue'
import QrLoginModal from '@/components/QrLoginModal.vue'
import ExtraAccounts from '@/components/accounts/ExtraAccounts.vue'
import { libraryApi, storageApi } from '@/api'
import type { LibLeftover, MediaLibsView } from '@/api/library'
import { useQueueStore } from '@/stores/queue'
import { plainProps } from '@/utils/autofill'
import { DEFAULT_DEVICE, DEVICE_OPTIONS } from '@/types/storage'
import type { StorageCheck } from '@/types/storage'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { useTabQuery } from '@/composables/useTabQuery'

const { message, dialog } = useFeedback()

/**
 * 两个页签：account（115 账号：主账号、连接设置、附加账号）/ library（共用目录 + 媒体库卡片）。
 * 别处跳「改媒体库目录」带 ?tab=library；页签值别改，收藏和站内链接都认它。
 */
const tab = useTabQuery('account')
const TAB_ITEMS = computed(() => [
  { value: 'account', label: '115 账号' },
  { value: 'library', label: '媒体库', dot: libUnset.value },
])

/**
 * 媒体库表由页面读一次、交给 MediaLibs 显示：账号页签也要用（没库时引导、每个账号挂了几个库）。
 * 原来只在 MediaLibs 挂载时读，没点开「媒体库」页签之前，账号页签上的「还没有设置媒体库」永远不出。
 */
const libView = ref<MediaLibsView | null>(null)
const libLoading = ref(true)
async function loadLibs() {
  try {
    libView.value = await libraryApi.getMediaLibs()
  } catch (e) {
    toastError(e, '读取媒体库失败')
  } finally {
    libLoading.value = false
  }
}
function onLibsUpdate(v: MediaLibsView) {
  libView.value = v
  void loadLeftovers()
}
const libUnset = computed(() => !!libView.value && !libView.value.libs.length)
/** 账号 id → 挂了几个库（主账号是 0） */
const libCount = computed(() => {
  if (!libView.value) return undefined
  const out: Record<number, number> = {}
  for (const l of libView.value.libs) out[l.account] = (out[l.account] ?? 0) + 1
  return out
})
/** 账号还没绑定：媒体库页签里选不了 115 目录，提示先回账号页签扫码 */
const accountUnbound = computed(() => !accountLoading.value && !account.value)

/**
 * 移出配置的媒体库在本地留下的 STRM 与台账（后端 libleft.go）。删掉一个库、或 115 上给库改了名之后出现；
 * 只列出来，用户点了才清，清理也只动本地
 */
const queue = useQueueStore()
const leftovers = ref<LibLeftover[]>([])
async function loadLeftovers() {
  try {
    leftovers.value = (await libraryApi.getLeftovers()).data ?? []
  } catch {
    leftovers.value = []
  }
}
const offFinished = queue.onFinished((j) => {
  if (j.kind === 'libclean') void loadLeftovers()
})
onUnmounted(offFinished)

async function cleanLeftover(l: LibLeftover) {
  const ok = await dialog.confirm({
    title: `清理「${l.name}」的本地残留`,
    content: `将删除本地媒体库根目录下的「${l.name}」整个目录（STRM、字幕、NFO、图片），以及台账里它的 ${l.files} 行。网盘上的文件不动；以后想要回来，把这个库加回媒体库再跑一次全量同步即可。Emby 里对应的媒体库请自己在 Emby 里删除或刷新。`,
    actions: [
      { label: '取消', value: false, variant: 'tertiary' },
      { label: '清理本地', value: true, variant: 'danger' },
    ],
  })
  if (!ok) return
  try {
    const d = await libraryApi.cleanLeftover(l.name)
    message.success(d.message)
    await queue.submitted(d.job_id)
  } catch (e) {
    toastError(e, '提交清理失败')
  }
}

const DEFAULT_INTERVAL = 3.0

const form = ref({
  device: DEFAULT_DEVICE,
  interval: DEFAULT_INTERVAL,
  openapi_enabled: false,
  app_id: '',
})

const account = ref<StorageCheck | null>(null)
/** 首次静默检测还没回来：账号卡显示骨架，而不是先闪一下「未绑定」 */
const accountLoading = ref(true)
const checking = ref(false)
const saving = ref(false)
const qrShow = ref(false)

/** OPENAPI 启用且填了 AppID 才走开放平台授权，否则回退 Cookie 扫码 */
const qrChannel = computed<'openapi' | 'cookie'>(() =>
  form.value.openapi_enabled && form.value.app_id.trim() ? 'openapi' : 'cookie',
)

async function load() {
  try {
    const res = await storageApi.list()
    const acc = (res.data ?? []).find((s) => s.type === '115')
    if (acc) {
      form.value = {
        // 保存过的设备值不在选项里时用默认设备
        device: DEVICE_OPTIONS.some((o) => o.value === acc.device) ? acc.device : DEFAULT_DEVICE,
        interval: acc.interval || DEFAULT_INTERVAL,
        openapi_enabled: !!acc.openapi_enabled,
        app_id: acc.app_id || '',
      }
    }
    // 静默刷新账号卡片（真实容量/会员/头像）；未绑定时失败是正常的，不提示
    const chk = await storageApi.check().catch(() => null)
    if (chk?.valid) account.value = chk
  } catch {
    // 列表拿不到就保持默认表单，不打断页面
  } finally {
    accountLoading.value = false
  }
}

async function check() {
  checking.value = true
  try {
    const data = await storageApi.check()
    if (!data.valid) {
      message.error('Cookie 无效：' + (data.message || '未知原因'))
      return
    }
    account.value = data
    message.success('Cookie 检测成功')
  } catch (e) {
    toastError(e, 'Cookie 检测失败')
  } finally {
    checking.value = false
  }
}

/** 启用 OpenAPI 但没填 AppID 时后端会当作未启用，静默回落 Cookie——先在这里拦住 */
function validate(): boolean {
  if (form.value.openapi_enabled && !form.value.app_id.trim()) {
    message.error('启用 OPENAPI 必须填写开放平台 AppID；没有 AppID 请选择「禁用」，Cookie 通道功能完整')
    return false
  }
  return true
}

async function save(): Promise<boolean> {
  if (!validate()) return false
  saving.value = true
  try {
    await storageApi.save({ name: '115主号', type: '115', ...form.value })
    message.success('保存成功')
    return true
  } catch (e) {
    toastError(e, '保存失败')
    return false
  } finally {
    saving.value = false
  }
}

async function saveAndScan() {
  if (await save()) qrShow.value = true
}

function reset() {
  form.value = {
    device: DEFAULT_DEVICE,
    interval: DEFAULT_INTERVAL,
    openapi_enabled: false,
    app_id: '',
  }
  message.info('配置已重置（尚未保存）')
}

onMounted(() => {
  load()
  void loadLibs()
  void loadLeftovers()
})
</script>

<template>
  <div class="page">
    <HTabs v-model="tab" :items="TAB_ITEMS" />

    <Transition name="tab" mode="out-in">
      <div v-if="tab !== 'library'" key="account" class="pane">
        <AccountHero
          :account="account"
          :loading="accountLoading"
          :checking="checking"
          @check="check"
          @scan="qrShow = true"
        />

        <!-- 没设媒体库时同步、整理、刮削都跑不起来，在账号页签就引导过去 -->
        <HAlert v-if="libUnset" status="warning" title="还没有设置媒体库">
          <template v-if="accountUnbound">先扫码登录 115 账号，再到「媒体库」页签添加媒体库。</template>
          <template v-else>同步、整理、刮削都要先有媒体库（115 目录 → 本地目录），请到「媒体库」页签添加。</template>
          <template #actions>
            <HButton v-if="accountUnbound" variant="primary" size="sm" @click="qrShow = true">
              <template #icon><QrCode :size="15" /></template>
              扫码登录
            </HButton>
            <HButton :variant="accountUnbound ? 'secondary' : 'primary'" size="sm" @click="tab = 'library'">
              去设置媒体库<ChevronRight :size="15" />
            </HButton>
          </template>
        </HAlert>

        <!-- 宽屏左右两栏：主账号的连接设置 / 附加账号，都不长，并排放不用往下翻 -->
        <div class="cols" :class="{ 'is-single': accountUnbound }">
          <SectionCard title="主账号连接" hint="登录设备与开放平台；改完先保存，再扫码才按新设置登录">
            <FieldRow
              label="Cookie 设备"
              tip="115 每种设备同时只能登录一处，扫码登录会顶掉这种设备上已经登录的会话。推荐支付宝小程序：平时几乎用不到，不会把你自己挤下线。网页端会顶掉电脑浏览器里的 115，也最容易触发「IP 登录异常」。已经登录的不用重新扫码。"
            >
              <div class="h-field-row">
                <HSelect v-model="form.device" :options="DEVICE_OPTIONS" />
                <HButton variant="secondary" @click="qrShow = true">
                  <template #icon><QrCode :size="15" /></template>
                  扫码登录
                </HButton>
              </div>
            </FieldRow>

            <FieldRow
              label="启用 OPENAPI"
              tip="需先在 115 开放平台（open.115.com）申请应用拿到 AppID。没有 AppID 请保持「禁用」——Cookie 通道功能完整，且全量同步可用「快速模式」。"
            >
              <HSegmented v-model="form.openapi_enabled" :options="[{ label: '启用', value: true }, { label: '禁用（默认）', value: false }]" />
            </FieldRow>

            <FieldRow
              v-if="form.openapi_enabled"
              label="开放平台 AppID"
              required
              tip="在 115 开放平台（open.115.com）申请应用后获取。填入后点击“保存并扫码授权”，用 115 App 扫码完成 OAuth 授权。"
            >
              <div class="h-field-row">
                <HInput v-model="form.app_id" placeholder="请输入 115 开放平台 AppID" :input-attrs="plainProps('acc-open-app-id')" />
                <HButton variant="secondary" @click="saveAndScan">保存并扫码授权</HButton>
              </div>
            </FieldRow>

            <FieldRow label="API 请求间隔" tip="设置 API 请求间隔可以减少风控概率。读接口默认 1s，写接口建议 ≥3s。">
              <HNumberInput v-model="form.interval" :min="0.5" :step="0.5" style="width: 150px">
                <template #suffix>秒</template>
              </HNumberInput>
            </FieldRow>

            <FormActions>
              <HButton variant="primary" :loading="saving" @click="save">保存配置</HButton>
              <HButton variant="tertiary" @click="reset">重置配置</HButton>
            </FormActions>
          </SectionCard>

          <!-- 附加 115 账号（account115.go） -->
          <ExtraAccounts v-if="!accountUnbound" :lib-count="libCount" />
        </div>
      </div>

      <div v-else key="library" class="pane">
        <HAlert v-if="accountUnbound" status="accent" title="115 账号还没绑定">
          选择 115 目录要先登录账号。
          <template #actions>
            <HButton variant="secondary" size="sm" @click="tab = 'account'">去绑定账号<ChevronRight :size="15" /></HButton>
          </template>
        </HAlert>

        <HAlert v-if="leftovers.length" status="warning" title="本地还有不在配置里的媒体库">
          这些目录是删掉的媒体库留下的，同步不再管它们，海报墙和 Emby 里还看得到。
          <ul class="leftover-list">
            <li v-for="l in leftovers" :key="l.name">
              <span>
                <b>{{ l.name }}</b>：台账 {{ l.files }} 行（视频 {{ l.videos }} 个）<template v-if="!l.local">，本地目录已不在</template>
              </span>
              <HButton variant="tertiary" size="sm" @click="cleanLeftover(l)">清理本地</HButton>
            </li>
          </ul>
        </HAlert>

        <StagingLegacyAlert />

        <MediaLibs :view="libView" :loading="libLoading" @update="onLibsUpdate" />
      </div>
    </Transition>

    <QrLoginModal
      v-model:show="qrShow"
      :channel="qrChannel"
      :device="form.device"
      :app-id="form.app_id"
      @success="(message.success('115 账号绑定成功'), check())"
    />
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.pane {
  display: flex;
  flex-direction: column;
  gap: 16px;
  min-width: 0;
}
.tab-enter-active,
.tab-leave-active {
  transition:
    opacity 0.16s ease,
    transform 0.16s ease;
}
.tab-enter-from {
  opacity: 0;
  transform: translateY(5px);
}
.tab-leave-to {
  opacity: 0;
  transform: translateY(-3px);
}

.cols {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 16px;
  align-items: start;
}
.cols.is-single {
  grid-template-columns: minmax(0, 1fr);
}
.leftover-list {
  margin: 8px 0 0;
  padding: 0;
  list-style: none;
}
.leftover-list li {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 2px 0;
}

@media (max-width: 1280px) {
  .cols {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
