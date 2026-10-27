<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ExternalLink } from '@lucide/vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import CopyBox from '@/components/ui/CopyBox.vue'
import { useSetting } from '@/composables/useSetting'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { systemApi } from '@/api'
import { checkUpdateNow, displayVersion, refreshUpdateStatus, updateStatus } from '@/stores/update'
import { fullTime, relTime } from '@/utils/time'

/**
 * 版本更新（AGENTS.md §6.7）。
 * 后端每 12 小时查一次 GitHub Releases，这里读缓存；「检查更新」立刻查一次。
 * 「立即更新」：后端从 ghcr 镜像层取新程序、校验后重启换上（selfupdate.go）；
 * 不在本站镜像里跑、或新版改了运行环境时，照旧给 docker compose 命令。
 */
const { dialog } = useFeedback()
const { model, saving, save } = useSetting('update', { check: true, notify: true })

const UPDATE_CMD = 'docker compose pull && docker compose up -d'

const st = updateStatus
const checking = ref(false)

onMounted(async () => {
  await refreshUpdateStatus()
  // 刷新页面时更新正在进行：接着看进度
  const a = st.value?.apply
  if (a && a.phase !== 'failed') follow(a.target)
})

const applying = ref(false)
let alive = true
onBeforeUnmount(() => {
  alive = false
})

const applyText = computed(() => {
  const a = st.value?.apply
  if (!a) return ''
  if (a.phase === 'restarting') return `${a.target} 已下载并校验，正在重启，回来后自动刷新页面…`
  if (a.phase === 'downloading') {
    const mb = (n?: number) => ((n || 0) / 1048576).toFixed(1)
    return a.total ? `正在下载 ${a.target}：${mb(a.done)} / ${mb(a.total)} MB` : `正在下载 ${a.target}…`
  }
  return ''
})

async function apply() {
  const target = st.value?.latest
  if (!target) return
  const ok = await dialog.confirm({
    title: `更新到 ${target}`,
    content:
      '会从 ghcr.io 下载新版本的程序（约几十 MB），校验后重启容器换上；正在跑的任务会被中断，重启后标为中断。' +
      '靠容器的重启策略拉起来（docker-compose.yml 默认 restart: unless-stopped），没配的话请手动 docker compose up -d。',
    actions: [
      { label: '取消', value: false, variant: 'tertiary' },
      { label: '更新并重启', value: true, variant: 'primary' },
    ],
  })
  if (!ok) return
  applying.value = true
  try {
    updateStatus.value = await systemApi.applyUpdate(target)
  } catch (e) {
    applying.value = false
    toastError(e, '更新失败')
    return
  }
  follow(target)
}

/** 盯着进度：下载失败就停；重启后版本号变成 target 就刷新页面。最多等 10 分钟 */
async function follow(target: string) {
  applying.value = true
  const start = Date.now()
  while (alive && Date.now() - start < 600_000) {
    await new Promise((r) => setTimeout(r, 2000))
    try {
      const s = await systemApi.updateStatus()
      updateStatus.value = s
      if (s.current === target) {
        location.reload()
        return
      }
      if (!s.apply || s.apply.phase === 'failed') break
    } catch {
      // 重启中连不上，接着等
    }
  }
  applying.value = false
}

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
  if (s.has_update && s.can_apply) return { status: 'accent' as const, title: `有新版本 ${s.latest}`, text: '点「立即更新」：下载新程序、校验后重启换上，配置与数据不受影响。也可以照下面的命令自己更新。' }
  if (s.has_update) return { status: 'accent' as const, title: `有新版本 ${s.latest}`, text: `${s.apply_block || ''}。照下面的命令在 docker-compose.yml 所在目录执行，配置与数据都在挂载卷里，不受影响。` }
  if (s.error && !s.latest) return { status: 'warning' as const, title: '检查失败', text: s.error }
  if (!s.checked_at) return { status: 'default' as const, title: '还没检查过', text: '启动一分钟后会自动检查，也可以现在点「检查更新」。' }
  if (!s.latest) return { status: 'default' as const, title: '还没有发布过正式版', text: '' }
  if (!s.comparable) return { status: 'default' as const, title: `最新正式版 ${s.latest}`, text: '当前是本地 / 开发构建，认不出版本号，不做新旧比较。' }
  return { status: 'success' as const, title: '已是最新版本', text: '' }
})
</script>

<template>
  <SectionCard title="版本更新" hint="有新版本时提示，可以一键更新，也可以自己执行 docker compose 命令">
    <template #extra>
      <HButton variant="tertiary" :loading="checking" @click="check">检查更新</HButton>
      <HButton v-if="st?.has_update && st.can_apply" variant="primary" :loading="applying" @click="apply">立即更新</HButton>
    </template>

    <FieldRow label="当前版本">
      <span class="mono">{{ displayVersion(st?.current) || '—' }}</span>
      <!-- 版本号里已经带着提交号（还没打过 tag / 两版之间的构建）就不再重复一遍 -->
      <span v-if="st?.sha && st.sha !== 'dev' && !st.current.includes(st.sha.slice(0, 7))" class="muted mono"> · {{ st.sha.slice(0, 7) }}</span>
      <span v-if="st?.image_version" class="muted"> · 一键更新换上的，镜像里是 <span class="mono">{{ st.image_version }}</span></span>
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

    <HAlert v-if="st?.last_failed" status="danger" title="上一次一键更新没换上">{{ st.last_failed }}</HAlert>
    <HAlert v-if="applyText" status="accent" title="正在更新">{{ applyText }}</HAlert>
    <HAlert v-else-if="st?.apply?.phase === 'failed'" status="danger" :title="`更新到 ${st.apply.target} 失败`">{{ st.apply.error }}</HAlert>
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
