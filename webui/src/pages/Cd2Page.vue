<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { NAlert, NButton, NDivider, NInput, NPopconfirm, NRadioButton, NRadioGroup } from 'naive-ui'
import SectionCard from '@/components/ui/SectionCard.vue'
import SecretInput from '@/components/ui/SecretInput.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import TestBanner, { type BannerState } from '@/components/ui/TestBanner.vue'
import { cd2Api } from '@/api'
import { plainProps } from '@/utils/autofill'
import type { Cd2Config, Cd2OrgStatus } from '@/api/cd2'
import { toastError, useFeedback } from '@/composables/useFeedback'

const { message } = useFeedback()

const form = ref({ endpoint: '', username: '', password: '', org_enabled: false })
const derived = ref<Pick<Cd2Config, 'root_path' | 'org_pending' | 'org_existing'>>({})
const status = ref<Cd2OrgStatus | null>(null)
const statusError = ref(false)
const banner = ref<BannerState | null>(null)
const saving = ref(false)
const testing = ref(false)

async function loadConfig() {
  try {
    const d = await cd2Api.getConfig()
    const c: Partial<Cd2Config> = d.data ?? {}
    form.value = {
      endpoint: c.endpoint ?? '',
      username: c.username ?? '',
      password: c.password ?? '',
      org_enabled: !!c.org_enabled,
    }
    derived.value = {
      root_path: c.root_path,
      org_pending: c.org_pending,
      org_existing: c.org_existing,
    }
  } catch {
    // 首次使用尚无配置
  }
}

async function loadStatus() {
  try {
    status.value = (await cd2Api.orgStatus()).data ?? null
    statusError.value = false
  } catch {
    statusError.value = true
  }
}

async function save() {
  saving.value = true
  try {
    const d = await cd2Api.saveConfig(form.value)
    message.success(d.message || '配置已保存')
    // 保存时后端会按 115 目录重新探测派生目录，重新读一次才能看到
    await loadConfig()
    await loadStatus()
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}

async function test() {
  testing.value = true
  banner.value = { status: 'pending', title: '正在连接 CD2…' }
  try {
    // 后端测试用的是已保存的凭据，所以必须先存再测
    await cd2Api.saveConfig(form.value)
    const d = await cd2Api.test()
    banner.value = { status: 'ok', title: d.message || '连接成功' }
    await loadConfig()
  } catch (e) {
    banner.value = { status: 'err', title: '连接失败', detail: e instanceof Error ? e.message : '' }
  } finally {
    testing.value = false
  }
}

async function runOrganize() {
  try {
    const d = await cd2Api.orgRun()
    message.success(d.message || '整理已开始')
    loadStatus()
  } catch (e) {
    toastError(e, '整理触发失败')
  }
}

const statusView = computed(() => {
  if (statusError.value) return { tone: 'err', text: '状态加载失败' }
  const s = status.value
  if (!s || !s.enabled) return { tone: 'idle', text: '未开启' }
  if (s.running) {
    return {
      tone: 'ok',
      text: `监控中（已整理 ${s.organized ?? 0} 个单元${s.last_event ? `，最近事件 ${s.last_event}` : ''}）`,
    }
  }
  return { tone: 'warn', text: `启动中…${s.last_err ? `（${s.last_err}）` : ''}` }
})

let timer: number | undefined
onMounted(() => {
  loadConfig()
  loadStatus()
  timer = window.setInterval(loadStatus, 15_000)
})
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <SectionCard title="CloudDrive2" hint="跨网盘整理引擎">
    <NAlert class="intro" type="info" :bordered="false">
      把 CD2 挂载的任意网盘当作「跨网盘整理引擎」的入口：监控目录出现新内容时自动识别、改名、分类并搬运进
      115 媒体库，再由本项目原生生成 STRM。链路为 监控目录新视频 → 识别（TMDB / 目录名兜底）→
      重命名模板改名 → 二级分类归位并跨网盘搬运 → 触发增量同步生成 STRM → 通知 Emby。
      <b>播放完全走项目原生直链，不经过 CD2</b>；识别失败的留在原地等重试。
    </NAlert>

    <FieldRow
      label="服务地址"
      tip="CD2 的服务地址（与网页管理端同地址同端口，默认 19798）。本服务必须能直接访问该地址——Docker 部署时填宿主机可达的 IP，不要填 127.0.0.1。"
    >
      <NInput
        v-model:value="form.endpoint"
        placeholder="如 192.168.1.10:19798"
        :input-props="plainProps('cd2-endpoint')"
      />
    </FieldRow>

    <FieldRow
      label="账号密码"
      tip="CD2 本地账号，即登录 CD2 网页管理端用的用户名和密码（不是网盘账号）。token 过期会自动重新登录；开启了两步验证的账号暂不支持。"
    >
      <div class="pair">
        <NInput
          v-model:value="form.username"
          placeholder="用户名"
          :input-props="plainProps('cd2-account')"
        />
        <SecretInput v-model="form.password" name="cd2-secret" placeholder="密码" />
      </div>
    </FieldRow>

    <FieldRow
      label="连接测试"
      tip="会先用当前表单内容保存配置，再实际登录 CD2 列一次根目录（根目录即各网盘挂载点）。"
    >
      <NButton :loading="testing" @click="test">测试连接</NButton>
      <TestBanner :state="banner" />
    </FieldRow>

    <NDivider />

    <FieldRow
      label="实时监控整理"
      tip="订阅 CD2 文件变更推送。开关保存后约 10 秒内生效；同目录文件静默 8 秒后才开始整理（等批量转存的文件陆续到位）。"
    >
      <div class="pair">
        <NRadioGroup v-model:value="form.org_enabled">
          <NRadioButton :value="true">开启</NRadioButton>
          <NRadioButton :value="false">关闭</NRadioButton>
        </NRadioGroup>
        <NPopconfirm @positive-click="runOrganize">
          <template #trigger><NButton>立即整理</NButton></template>
          立即整理监控目录下所有待处理内容？
        </NPopconfirm>
      </div>
    </FieldRow>

    <FieldRow
      label="目录（全自动）"
      wide
      tip="无需填写任何目录：整理目标根自动探测 CD2 里的 115 媒体库挂载；监控 / 已存在目录自动取「自动整理 → 基础配置」里 115 的对应目录映射到 CD2 侧。在 115 那边改目录，这里自动跟随。"
    >
      <div class="derived">
        <template v-if="derived.root_path">
          <div><span class="k">整理目标根</span><b>{{ derived.root_path }}</b></div>
          <div><span class="k">监控目录</span>{{ derived.org_pending || '（识别中…）' }}</div>
          <div><span class="k">已存在目录</span>{{ derived.org_existing || '（识别中…）' }}</div>
        </template>
        <span v-else class="muted">未识别（保存并开启后自动探测 CD2 的 115 媒体库挂载）</span>
      </div>
    </FieldRow>

    <FieldRow label="监控状态">
      <div class="status" :class="statusView.tone">
        <span class="dot" />{{ statusView.text }}
      </div>
    </FieldRow>

    <FormActions>
      <NButton type="primary" :loading="saving" @click="save">保存配置</NButton>
    </FormActions>
  </SectionCard>
</template>

<style scoped>
.intro {
  margin-bottom: 14px;
}
.pair {
  display: flex;
  gap: 8px;
  align-items: center;
}

.derived {
  padding: 9px 12px;
  border-radius: var(--radius);
  background: var(--c-bg-raised);
  border: 1px solid var(--c-border);
  font-size: 12.5px;
  line-height: 1.9;
  color: var(--c-text-2);
}
.derived .k {
  display: inline-block;
  width: 84px;
  color: var(--c-text-3);
}
.derived .muted {
  color: var(--c-text-3);
}

.status {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  font-size: 12.5px;
  color: var(--c-text-2);
}
.status .dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--c-text-4);
}
.status.ok .dot {
  background: var(--c-success);
}
.status.warn .dot {
  background: var(--c-warning);
}
.status.err {
  color: var(--c-danger);
}
.status.err .dot {
  background: var(--c-danger);
}
</style>
