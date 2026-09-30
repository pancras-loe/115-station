<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HCheckbox from '@/components/hero/HCheckbox.vue'
import HModal from '@/components/hero/HModal.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import { filesApi } from '@/api'
import type { FileJobBody, MoveTarget, WorkspaceRole } from '@/api/files'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { useQueueStore } from '@/stores/queue'

/**
 * 移动到 冗余 / 已存在 / 待整理。
 * 从媒体库里移出片目会删掉本地 STRM、元数据与台账，Emby 里的条目随之消失 —— 要勾确认才能提交。
 */
const props = defineProps<{
  body: FileJobBody | null
  /** 当前目录所在的工作区：同一个工作区不作为目标 */
  zone: WorkspaceRole | ''
  /** 已配置的工作区（没配的不给选） */
  configured: WorkspaceRole[]
}>()
const show = defineModel<boolean>('show', { required: true })

const { message } = useFeedback()
const queue = useQueueStore()

const TARGETS: { value: MoveTarget; label: string; note: string }[] = [
  { value: 'redundant', label: '冗余', note: '放着不管：不会被自动整理，也不在媒体库里。' },
  { value: 'existing', label: '已存在', note: '当作重复版本收起来：不会被自动整理，也不在媒体库里。' },
  { value: 'pending', label: '待整理', note: '下一轮自动整理会重新识别并入库；开着「人工确认」时会先停在待确认。' },
]

const options = computed(() =>
  TARGETS.filter((t) => t.value !== props.zone).map((t) => ({
    label: t.label,
    value: t.value,
    disabled: !props.configured.includes(t.value),
  })),
)
const target = ref<MoveTarget>('redundant')
const acknowledged = ref(false)
const submitting = ref(false)

const count = computed(() => props.body?.items.length ?? 0)
const firstName = computed(() => props.body?.items[0]?.name ?? '')
const fromLibrary = computed(() => props.zone === 'library')
const note = computed(() => TARGETS.find((t) => t.value === target.value)?.note ?? '')
/** 没配置的目标只是灰掉的话，用户不知道为什么点不了 */
const unconfigured = computed(() => options.value.filter((o) => o.disabled).map((o) => o.label))

watch(show, (v) => {
  if (!v) return
  acknowledged.value = false
  const first = options.value.find((o) => !o.disabled)
  if (first) target.value = first.value
})

const canSubmit = computed(
  () =>
    !!props.body &&
    options.value.some((o) => o.value === target.value && !o.disabled) &&
    (!fromLibrary.value || acknowledged.value),
)

async function submit() {
  if (!props.body || !canSubmit.value) return
  submitting.value = true
  try {
    const d = await filesApi.move({ ...props.body, target: target.value })
    message.success(d.message || '移动已加入任务队列')
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
  <HModal v-model:show="show" :title="fromLibrary ? '移出媒体库' : '移动'" width="520px">
    <div class="body">
      <p class="src">
        所选：<b>{{ firstName }}</b>
        <span v-if="count > 1"> 等 {{ count }} 项</span>
      </p>

      <FieldRow label="移动到">
        <HSegmented v-model="target" :options="options" />
      </FieldRow>
      <p class="note">{{ note }}</p>
      <p v-if="unconfigured.length" class="note">
        {{ unconfigured.join('、') }}目录还没配置，不能选：到「自动整理 → 基础配置」里设置。
      </p>

      <template v-if="fromLibrary">
        <HAlert status="warning">
          将从媒体库移出 {{ count }} 部：本地的 STRM、NFO、海报与同步台账会一并删除，Emby 里的条目随之消失。
          网盘里的文件只是换了目录，不会删除。
        </HAlert>
        <HCheckbox v-model:checked="acknowledged" class="ack">
          <span>我知道这会把它们移出媒体库</span>
        </HCheckbox>
      </template>
      <p class="note">目标目录里已有同名的文件或文件夹、或正在等人工确认的条目会被跳过，原因见任务结果。</p>
    </div>

    <template #footer>
      <div class="foot-btns">
        <HButton variant="tertiary" @click="show = false">取消</HButton>
        <HButton variant="primary" :disabled="!canSubmit" :loading="submitting" @click="submit">加入队列移动</HButton>
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
  margin: 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--muted);
}
.ack {
  font-size: 13px;
}
.foot-btns {
  display: flex;
  gap: 8px;
  margin-left: auto;
}
@media (max-width: 639px) {
  .foot-btns {
    width: 100%;
  }
  .foot-btns > * {
    flex: 1;
  }
}
</style>
