<script setup lang="ts">
import { computed, ref } from 'vue'
import { FolderOpen, Plus, RefreshCw, Trash2, KeyRound } from '@lucide/vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HInput from '@/components/hero/HInput.vue'
import HModal from '@/components/hero/HModal.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import SourceFiles from './SourceFiles.vue'
import { shareStrmApi } from '@/api'
import type { ShareSource, ShareSourceState } from '@/api/sharestrm'
import { useShareStrm } from '@/composables/shareStrm'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { useQueueStore } from '@/stores/queue'
import { relTime, fullTime } from '@/utils/time'

const props = defineProps<{ list: ShareSource[]; loading: boolean }>()
const emit = defineEmits<{ changed: []; settings: [] }>()
const { message, dialog } = useFeedback()
const queue = useQueueStore()
const ss = useShareStrm()

// ---- 添加 ----
const url = ref('')
const code = ref('')
const adding = ref(false)

async function add() {
  const u = url.value.trim()
  if (!u) return
  adding.value = true
  try {
    const d = await shareStrmApi.add(u, code.value.trim())
    message.success(d.message)
    void queue.submitted(d.job_id)
    url.value = ''
    code.value = ''
    emit('changed')
  } catch (e) {
    toastError(e, '添加失败')
  } finally {
    adding.value = false
  }
}

// ---- 状态 ----
const STATE: Record<ShareSourceState, { text: string; color: 'success' | 'warning' | 'danger' | 'default' | 'accent' }> = {
  ok: { text: '正常', color: 'success' },
  pending: { text: '生成中', color: 'accent' },
  gone: { text: '分享已失效', color: 'danger' },
  badcode: { text: '提取码错误', color: 'warning' },
  error: { text: '出错', color: 'warning' },
}
function stateOf(s: ShareSource) {
  if (s.busy) return { text: '生成中', color: 'accent' as const }
  return STATE[s.state] ?? { text: s.state, color: 'default' as const }
}
function goneWhy(s: ShareSource) {
  if (s.share_state === '4') return '分享已取消'
  if (s.share_state === '7') return '分享已过期'
  return ''
}

// ---- 操作 ----
const busyId = ref(0)

async function check(s: ShareSource) {
  busyId.value = s.id
  try {
    const d = await shareStrmApi.check(s.id)
    message.success(d.message)
    void queue.submitted(d.job_id)
    emit('changed')
  } catch (e) {
    toastError(e, '排不上队')
  } finally {
    busyId.value = 0
  }
}

async function del(s: ShareSource) {
  const ok = await dialog.confirm({
    title: `删除「${s.title || s.share_code}」`,
    content: `会删掉本地「${s.local_dir}」下本站写的 ${s.files} 个 STRM 和 ${s.assets} 个附属文件，并通知 Emby。115 上什么都不动（分享本来就不是你的）。删了以后想要回来，重新贴一次链接就行。`,
    tone: 'danger',
    actions: [
      { label: '取消', value: false, variant: 'tertiary' },
      { label: '删除', value: true, variant: 'danger' },
    ],
  })
  if (!ok) return
  busyId.value = s.id
  try {
    const d = await shareStrmApi.remove(s.id)
    message.success(d.message)
    emit('changed')
  } catch (e) {
    toastError(e, '删除失败')
  } finally {
    busyId.value = 0
  }
}

// 改提取码
const codeShow = ref(false)
const codeTarget = ref<ShareSource | null>(null)
const codeInput = ref('')
const codeSaving = ref(false)
function editCode(s: ShareSource) {
  codeTarget.value = s
  codeInput.value = s.receive_code
  codeShow.value = true
}
async function saveCode() {
  if (!codeTarget.value) return
  codeSaving.value = true
  try {
    const d = await shareStrmApi.setReceiveCode(codeTarget.value.id, codeInput.value.trim())
    message.success(d.message)
    codeShow.value = false
    emit('changed')
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    codeSaving.value = false
  }
}

// 文件清单
const filesShow = ref(false)
const filesOf = ref<ShareSource | null>(null)
function openFiles(s: ShareSource) {
  filesOf.value = s
  filesShow.value = true
}

const sorted = computed(() => props.list)
</script>

<template>
  <div class="tab">
    <SectionCard title="添加分享" hint="贴 115 分享链接（连同提取码），不转存，直接在本地生成 STRM。分享失效后就播不了，想长期留着请转存。">
      <p v-if="ss.blocked.value" class="blocked">
        {{ ss.blocked.value }}
        <a v-if="!ss.enabled.value" href="#" @click.prevent="emit('settings')">去设置</a>
      </p>
      <div class="add-row">
        <HInput v-model="url" class="add-url" placeholder="https://115cdn.com/s/xxxx?password=abcd" :disabled="!!ss.blocked.value" @enter="add" />
        <HInput v-model="code" class="add-code" placeholder="提取码（链接里带了可不填）" :disabled="!!ss.blocked.value" @enter="add" />
        <HButton variant="primary" :loading="adding" :disabled="!!ss.blocked.value || !url.trim()" @click="add">
          <template #icon><Plus :size="15" /></template>
          生成 STRM
        </HButton>
      </div>
    </SectionCard>

    <SectionCard title="已添加的分享" :hint="loading ? '读取中…' : `${list.length} 个`">
      <EmptyState v-if="!sorted.length" text="还没有分享。贴一条 115 分享链接试试，或者在「发现与订阅 → 链接转存」、机器人里贴链接时选「生成 STRM」。" />
      <div v-else class="rows">
        <div v-for="s in sorted" :key="s.id" class="row">
          <div class="main">
            <div class="title-line">
              <span class="title" :title="s.title">{{ s.title || s.share_code }}</span>
              <HChip :color="stateOf(s).color">{{ stateOf(s).text }}</HChip>
            </div>
            <div class="meta">
              <span>{{ s.files }} 个 STRM<template v-if="s.assets"> · {{ s.assets }} 个附属文件</template></span>
              <span class="mono">本地：{{ s.local_dir }}</span>
              <span class="mono">分享码 {{ s.share_code }}<template v-if="s.receive_code"> · 提取码 {{ s.receive_code }}</template></span>
              <span :title="fullTime(s.last_check)">检查：{{ s.last_check ? relTime(s.last_check) : '还没跑完' }}</span>
            </div>
            <p v-if="s.state !== 'ok' && (s.last_error || goneWhy(s))" class="err">{{ goneWhy(s) || s.last_error }}</p>
          </div>
          <div class="acts">
            <HButton size="sm" variant="tertiary" @click="openFiles(s)">
              <template #icon><FolderOpen :size="14" /></template>
              文件
            </HButton>
            <HButton size="sm" variant="tertiary" :loading="busyId === s.id && !s.busy" :disabled="s.busy" @click="check(s)">
              <template #icon><RefreshCw :size="14" /></template>
              立即检查
            </HButton>
            <HButton size="sm" variant="tertiary" @click="editCode(s)">
              <template #icon><KeyRound :size="14" /></template>
              提取码
            </HButton>
            <HButton size="sm" variant="danger-soft" :disabled="s.busy" @click="del(s)">
              <template #icon><Trash2 :size="14" /></template>
              删除
            </HButton>
          </div>
        </div>
      </div>
    </SectionCard>

    <HModal v-model:show="codeShow" :title="`改提取码：${codeTarget?.title || codeTarget?.share_code || ''}`" width="440px">
      <p class="modal-note">分享者换了提取码时改这里，保存后马上重新检查一次。STRM 里不带提取码，不用重新生成。</p>
      <HInput v-model="codeInput" placeholder="提取码" mono @enter="saveCode" />
      <template #footer>
        <div class="foot-right">
          <HButton variant="tertiary" @click="codeShow = false">取消</HButton>
          <HButton variant="primary" :loading="codeSaving" @click="saveCode">保存</HButton>
        </div>
      </template>
    </HModal>

    <SourceFiles v-model:show="filesShow" :source="filesOf" />
  </div>
</template>

<style scoped>
.tab {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.blocked {
  margin: 0 0 10px;
  font-size: 13px;
  color: var(--warning);
}
.blocked a {
  margin-left: 6px;
  color: var(--accent);
}
.add-row {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}
.add-url {
  flex: 3 1 320px;
}
.add-code {
  flex: 1 1 160px;
}
.rows {
  display: flex;
  flex-direction: column;
}
.row {
  display: flex;
  gap: 12px;
  align-items: flex-start;
  padding: 12px 0;
  border-top: 1px solid var(--border);
}
.row:first-child {
  border-top: 0;
  padding-top: 0;
}
.main {
  flex: 1;
  min-width: 0;
}
.title-line {
  display: flex;
  align-items: center;
  gap: 8px;
}
.title {
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.meta {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 14px;
  margin-top: 4px;
  font-size: 12.5px;
  color: var(--muted);
}
.mono {
  font-family: var(--font-mono, ui-monospace, monospace);
}
.err {
  margin: 6px 0 0;
  font-size: 12.5px;
  color: var(--danger);
}
.acts {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
  justify-content: flex-end;
}
.modal-note {
  margin: 0 0 10px;
  font-size: 13px;
  color: var(--muted);
}
.foot-right {
  display: flex;
  gap: 8px;
  justify-content: flex-end;
}
@media (max-width: 720px) {
  .row {
    flex-direction: column;
  }
  .acts {
    justify-content: flex-start;
  }
}
</style>
