<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import HButton from '@/components/hero/HButton.vue'
import HNumberInput from '@/components/hero/HNumberInput.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import { resourcesApi } from '@/api'
import type { ShareOutConfig, ShareOutSite, ShareOutSiteCfg } from '@/api/resources'
import { toastError, useFeedback } from '@/composables/useFeedback'

/**
 * 「转存后分享到本站」：RE0 与癫影两张卡片共用。设置单独存在 setting `shareout`，按站点分开保存，
 * 不跟着卡片的「保存配置」走（那个接口整份覆盖账号配置）
 */
const props = defineProps<{ site: ShareOutSite }>()
const { message } = useFeedback()

const all = ref<ShareOutConfig | null>(null)
const form = ref<ShareOutSiteCfg>({ enabled: false, unlock_points: 0, anonymous: false })
const saving = ref(false)
const dirty = ref(false)

const label = computed(() => (props.site === 'dian' ? '癫影' : 'RE0'))
// 发不了的原因：开着开关也只会记一笔「跳过」，提前说清楚
const blocked = computed(() => {
  if (!all.value) return ''
  if (props.site === 'dian') return all.value.dian_configured ? '' : '先在上面填好癫影账号'
  return all.value.re0_ready
})
const tip = computed(
  () =>
    `只管你自己贴的 115 分享和 ed2k / 磁力（链接转存页、机器人里贴链接；同一部前后脚贴的几条离线链接合成一条分享），从资源站选的、订阅转存的都不分享。` +
    `等整理认出是哪一部之后才发布到${label.value}上；一条链接整理出好几部（合集）不发，同一条分享只发一次。` +
    (props.site === 'dian' ? '癫影要求 115 分享带访问码、文件名里认得出分辨率，不满足的跳过。' : '') +
    `结果显示在整理记录的来源链接旁边。搬运别人的分享可能违反站规，请先看清站点规则。`,
)

async function load() {
  try {
    all.value = await resourcesApi.shareOutConfig()
    form.value = { ...all.value[props.site] }
    dirty.value = false
  } catch {
    all.value = null
  }
}

async function save(next?: Partial<ShareOutSiteCfg>) {
  const body = { ...form.value, ...next, unlock_points: Math.max(0, Math.round(form.value.unlock_points ?? 0)) }
  saving.value = true
  try {
    await resourcesApi.shareOutSave(props.site, body)
    form.value = body
    dirty.value = false
    if (next && 'enabled' in next) {
      message.success(next.enabled ? `已开启：贴链接转存的分享整理完会发到${label.value}` : `已关闭分享到${label.value}`)
    } else {
      message.success('已保存')
    }
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}

onMounted(load)
defineExpose({ reload: load })
</script>

<template>
  <FieldRow label="转存后分享" :tip="tip">
    <div class="app-row">
      <HSwitch
        :model-value="form.enabled"
        :disabled="!all || saving"
        :aria-label="`转存后分享到${label}`"
        @update:model-value="(on: boolean) => save({ enabled: on })"
      />
      <span v-if="blocked" class="hint warn">{{ blocked }}</span>
    </div>
    <div v-if="form.enabled" class="app-row opts">
      <span class="hint">解锁积分</span>
      <HNumberInput
        v-model="form.unlock_points"
        class="num"
        :min="0"
        :max="10000"
        aria-label="解锁积分"
        @update:model-value="dirty = true"
      />
      <span class="hint">{{ site === 'dian' ? '0 = 站点默认价' : '0 = 免费' }}</span>
      <label v-if="site === 're0'" class="anon">
        <HSwitch v-model="form.anonymous" size="sm" aria-label="匿名分享" @update:model-value="dirty = true" />
        <span class="hint">匿名</span>
      </label>
      <HButton v-if="dirty" size="sm" variant="tertiary" :loading="saving" @click="save()">保存</HButton>
    </div>
  </FieldRow>
</template>

<style scoped>
.app-row {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}
.opts {
  margin-top: 8px;
}
.num {
  width: 130px;
}
.anon {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  margin-left: 8px;
}
.hint {
  color: var(--muted);
  font-size: 12px;
}
.warn {
  color: var(--warning);
}
</style>
