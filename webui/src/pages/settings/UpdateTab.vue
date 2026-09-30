<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ExternalLink } from '@lucide/vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import CopyBox from '@/components/ui/CopyBox.vue'
import { useSetting } from '@/composables/useSetting'
import { toastError } from '@/composables/useFeedback'
import { checkUpdateNow, displayVersion, refreshUpdateStatus, updateStatus } from '@/stores/update'
import { fullTime, relTime } from '@/utils/time'

/**
 * 版本更新：只提示，不自更新（AGENTS.md §6.7）。
 * 后端每 12 小时查一次 GitHub Releases，这里读缓存；「检查更新」立刻查一次。
 */
const { model, saving, save } = useSetting('update', { check: true, notify: true })

const UPDATE_CMD = 'docker compose pull && docker compose up -d'

const st = updateStatus
const checking = ref(false)

onMounted(refreshUpdateStatus)

async function check() {
  checking.value = true
  try {
    await checkUpdateNow()
  } catch (e) {
    toastError(e, '检查更新失败')
  } finally {
    checking.value = false
  }
}

/** 顶部结论：有新版本 / 已是最新 / 比不了 / 查不到 */
const verdict = computed(() => {
  const s = st.value
  if (!s) return null
  if (s.has_update) return { status: 'accent' as const, title: `有新版本 ${s.latest}`, text: '照下面的命令在 docker-compose.yml 所在目录执行，配置与数据都在挂载卷里，不受影响。' }
  if (s.error && !s.latest) return { status: 'warning' as const, title: '检查失败', text: s.error }
  if (!s.checked_at) return { status: 'default' as const, title: '还没检查过', text: '启动一分钟后会自动检查，也可以现在点「检查更新」。' }
  if (!s.latest) return { status: 'default' as const, title: '还没有发布过正式版', text: '' }
  if (!s.comparable) return { status: 'default' as const, title: `最新正式版 ${s.latest}`, text: '当前是本地 / 开发构建，认不出版本号，不做新旧比较。' }
  return { status: 'success' as const, title: '已是最新版本', text: '' }
})
</script>

<template>
  <SectionCard title="版本更新" hint="有新版本时提示，更新需要自己执行 docker compose 命令">
    <template #extra>
      <HButton variant="tertiary" :loading="checking" @click="check">检查更新</HButton>
    </template>

    <FieldRow label="当前版本">
      <span class="mono">{{ displayVersion(st?.current) || '—' }}</span>
      <!-- 版本号里已经带着提交号（还没打过 tag / 两版之间的构建）就不再重复一遍 -->
      <span v-if="st?.sha && st.sha !== 'dev' && !st.current.includes(st.sha.slice(0, 7))" class="muted mono"> · {{ st.sha.slice(0, 7) }}</span>
    </FieldRow>
    <FieldRow label="最新正式版">
      <template v-if="st?.latest">
        <span class="mono">{{ st.latest }}</span>
        <span v-if="st.published_at" class="muted" :title="fullTime(st.published_at)"> · 发布于 {{ relTime(st.published_at) }}</span>
      </template>
      <span v-else class="muted">—</span>
    </FieldRow>
    <FieldRow label="上次检查">
      <span v-if="st?.checked_at" :title="fullTime(st.checked_at)">{{ relTime(st.checked_at) }}</span>
      <span v-else class="muted">—</span>
      <span v-if="st?.error && st.latest" class="err"> · 最近一次失败：{{ st.error }}</span>
    </FieldRow>

    <HAlert v-if="verdict" :status="verdict.status" :title="verdict.title">
      <template v-if="verdict.text">{{ verdict.text }}</template>
    </HAlert>

    <template v-if="st?.has_update">
      <CopyBox :value="UPDATE_CMD" tone="primary" />
      <div v-if="st.notes" class="notes">{{ st.notes }}</div>
      <a v-if="st.url" class="release-link" :href="st.url" target="_blank" rel="noopener">
        在 GitHub 上查看 {{ st.latest }} 的更新说明 <ExternalLink :size="13" />
      </a>
    </template>
  </SectionCard>

  <SectionCard title="检测设置">
    <FieldRow label="定时检查" tip="每 12 小时请求一次 GitHub（api.github.com），走「代理配置」里的代理。关掉后只在你点「检查更新」时才查。">
      <HSwitch v-model="model.check" />
    </FieldRow>
    <FieldRow label="新版本通知" tip="发现新版本时往已启用的通知渠道（企微 / TG 等）推一条，同一个版本只推一次。">
      <HSwitch v-model="model.notify" :disabled="!model.check" />
    </FieldRow>
    <FormActions>
      <HButton variant="primary" :loading="saving" @click="save()">保存配置</HButton>
    </FormActions>
  </SectionCard>
</template>

<style scoped>
.mono {
  font-family: var(--font-mono, ui-monospace, monospace);
}
.muted {
  color: var(--muted);
}
.err {
  color: var(--danger);
}
.notes {
  max-height: 320px;
  overflow: auto;
  padding: 12px 14px;
  border-radius: var(--r-sm);
  background: var(--surface-secondary, var(--default));
  font-size: 13px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-word;
}
.release-link {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--accent);
  font-size: 13px;
  text-decoration: none;
}
.release-link:hover {
  text-decoration: underline;
}
</style>
