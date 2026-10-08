<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HInput from '@/components/hero/HInput.vue'
import { CircleCheck, CircleX, LoaderCircle } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import { transferApi } from '@/api'
import { useTransferSources } from '@/composables/transferSources'
import { type ParsedLink, parseLinks, pendingLinkText } from '@/composables/transferLinks'
import { useFeedback } from '@/composables/useFeedback'

const router = useRouter()
const { message, dialog } = useFeedback()
const { state: srcState } = useTransferSources()
const folderPath = computed(() => srcState.value?.folder_path || srcState.value?.folder || '')

// ---- 提交链接 ----
const text = ref('')
const code = ref('')
const running = ref(false)

type Row = ParsedLink & { state: 'wait' | 'busy' | 'ok' | 'err'; msg: string }
const rows = ref<Row[]>([])

// 从「找资源」带过来的链接
onMounted(() => {
  if (pendingLinkText.value) {
    text.value = pendingLinkText.value
    pendingLinkText.value = ''
  }
})

const parsed = computed(() => parseLinks(text.value, code.value))
/** 只有一个分享、链接里和文本里都没写提取码时才要单独填 */
const needCode = computed(() => {
  // 不带输入框里的值再认一遍：否则填了一个字母框就消失了
  const shares = parseLinks(text.value).filter((l) => l.kind === 'share')
  return shares.length === 1 && !shares[0].code
})

const KIND_LABEL: Record<ParsedLink['kind'], string> = {
  share: '115 分享',
  magnet: '磁力',
  ed2k: 'ed2k',
  http: 'HTTP',
}
const summary = computed(() => {
  const n: Partial<Record<ParsedLink['kind'], number>> = {}
  for (const l of parsed.value) n[l.kind] = (n[l.kind] ?? 0) + 1
  return (Object.keys(n) as ParsedLink['kind'][]).map((k) => `${n[k]} 个${KIND_LABEL[k]}`).join('、')
})

async function submit() {
  const list = parsed.value
  if (!list.length) {
    message.warning(text.value.trim() ? '没认出能提交的链接：支持 115 分享、磁力、ed2k、HTTP' : '请粘贴链接')
    return
  }
  if (list.some((l) => l.kind === 'http')) {
    const ok = await dialog.confirm({
      title: '提交 HTTP 离线下载',
      content: '这是一个普通网页链接。115 会按链接下载文件；如果它是网页而不是文件，下载下来的就是那个网页。确定提交吗？',
      actions: [
        { label: '取消', value: false, variant: 'tertiary' },
        { label: '提交离线下载', value: true, variant: 'primary' },
      ],
    })
    if (!ok) return
  }
  running.value = true
  rows.value = list.map((l) => ({ ...l, state: 'wait', msg: '' }))
  // 一条一条来：分享转存与离线下载都是 115 写请求，后端本来就按节流排队，并发只会互相等
  for (const r of rows.value) {
    r.state = 'busy'
    try {
      const d = await transferApi.submit({ url: r.url, code: r.code })
      r.state = 'ok'
      r.msg = d.message
    } catch (e) {
      r.state = 'err'
      r.msg = e instanceof Error ? e.message : '提交失败'
    }
  }
  running.value = false
  const failed = rows.value.filter((r) => r.state === 'err').length
  if (!failed) {
    message.success(rows.value.length > 1 ? `${rows.value.length} 个链接都已提交` : rows.value[0].msg)
    text.value = ''
    code.value = ''
  } else {
    message.error(`${failed} 个链接提交失败，见下方列表`)
  }
}

</script>

<template>
  <div class="stack">
    <SectionCard title="提交链接" hint="115 分享转存，磁力 / ed2k / HTTP 提交 115 离线下载">
      <template #extra>
        <button type="button" class="folder" @click="router.push({ query: { tab: 'settings' } })">
          <template v-if="folderPath">存到 <b>{{ folderPath }}</b></template>
          <span v-else-if="srcState" class="warn">还没设置转存目录</span>
          <span class="link">{{ folderPath ? '更改' : '去设置' }}</span>
        </button>
      </template>
      <FieldRow
        label="链接"
        wide
        tip="可以一次粘贴多个：ed2k 一行一个，115 分享可以整段粘贴官方分享文案（带「访问码」那种）。提交后一个一个处理，完成后自动整理入库。"
      >
        <HInput
          v-model="text"
          :rows="5"
          mono
          placeholder="粘贴 115 分享 / 磁力 / ed2k 链接，可以一次多个"
          :input-attrs="{ 'aria-label': '链接', autocomplete: 'off', spellcheck: 'false' }"
        />
      </FieldRow>
      <FieldRow v-if="needCode" label="提取码" tip="链接里和文字里都没写提取码时填这里；无密码分享可以留空。">
        <HInput v-model="code" class="code" placeholder="提取码" :input-attrs="{ 'aria-label': '提取码', autocomplete: 'off' }" />
      </FieldRow>
      <FormActions>
        <HButton variant="primary" :loading="running" @click="submit">
          {{ parsed.length > 1 ? `提交 ${parsed.length} 个链接` : '提交' }}
        </HButton>
        <span v-if="summary" class="summary">认出 {{ summary }}</span>
      </FormActions>

      <ul v-if="rows.length" class="results">
        <li v-for="(r, i) in rows" :key="i" :class="r.state">
          <LoaderCircle v-if="r.state === 'busy'" :size="15" class="spin" />
          <CircleCheck v-else-if="r.state === 'ok'" :size="15" />
          <CircleX v-else-if="r.state === 'err'" :size="15" />
          <span v-else class="dot" />
          <HChip size="sm">{{ KIND_LABEL[r.kind] }}</HChip>
          <span class="url" :title="r.url">{{ r.url }}</span>
          <span v-if="r.msg" class="msg">{{ r.msg }}</span>
        </li>
      </ul>
    </SectionCard>

  </div>
</template>

<style scoped>
.stack {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.folder {
  all: unset;
  display: inline-flex;
  flex-wrap: wrap;
  gap: 4px 8px;
  font-size: 12.5px;
  color: var(--muted);
  cursor: pointer;
}
.folder b {
  font-family: var(--font-mono);
  font-weight: 500;
  color: var(--foreground);
  word-break: break-all;
}
.folder .warn {
  color: var(--warning);
}
.folder .link {
  color: var(--accent);
}
.folder:hover .link {
  text-decoration: underline;
}
.code {
  max-width: 160px;
}
.summary {
  align-self: center;
  font-size: 12.5px;
  color: var(--muted);
}
.results {
  list-style: none;
  margin: 4px 0 0;
  padding: 0;
  display: flex;
  flex-direction: column;
}
.results li {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px 8px;
  padding: 8px 2px;
  border-top: 1px solid var(--border);
  font-size: 12.5px;
  color: var(--muted);
}
.results li.ok {
  color: var(--success);
}
.results li.err {
  color: var(--danger);
}
.dot {
  width: 15px;
  text-align: center;
}
.dot::before {
  content: '·';
}
.url {
  flex: 1;
  min-width: 0;
  font-family: var(--font-mono);
  color: var(--foreground);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.msg {
  flex-basis: 100%;
  padding-left: 23px;
}
.spin {
  animation: spin 1s linear infinite;
}
@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}
@media (prefers-reduced-motion: reduce) {
  .spin {
    animation: none;
  }
}
</style>
