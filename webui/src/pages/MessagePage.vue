<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import HTabs from '@/components/hero/HTabs.vue'
import HChip from '@/components/hero/HChip.vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import SecretInput from '@/components/ui/SecretInput.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import TestBanner, { type BannerState } from '@/components/ui/TestBanner.vue'
import CopyBox from '@/components/ui/CopyBox.vue'
import { http } from '@/api'
import { plainProps } from '@/utils/autofill'
import { useSetting } from '@/composables/useSetting'
import { useTabQuery } from '@/composables/useTabQuery'
import { MESSAGE_DEFAULTS, type MessageConfig } from '@/types/message'

const tab = useTabQuery('wecom')

// 五个通道共用一个 Setting key，所以整页一份 model，任一页签保存的都是全量配置
const { model, saving, save, reset } = useSetting<MessageConfig>('message', MESSAGE_DEFAULTS)

const banner = ref<BannerState | null>(null)
const testing = ref(false)
const tgStatus = ref('正在读取连接状态…')
let tgStatusTimer: ReturnType<typeof setInterval> | undefined
let refreshingTgStatus = false
async function refreshTgStatus() {
  if (refreshingTgStatus) return
  refreshingTgStatus = true
  try {
    const result = await http.get<{ state: string; detail: string }>('/message/tg-status')
    tgStatus.value = result.detail || '接收器尚未启动'
  } catch {
    tgStatus.value = '连接状态读取失败'
  } finally {
    refreshingTgStatus = false
  }
}
onMounted(() => {
  void refreshTgStatus()
  tgStatusTimer = setInterval(() => { if (tab.value === 'tg') void refreshTgStatus() }, 5000)
})
onUnmounted(() => { if (tgStatusTimer) clearInterval(tgStatusTimer) })

async function test() {
  testing.value = true
  banner.value = { status: 'pending', title: '正在发送测试消息…' }
  try {
    const d = await http.post<{ success: boolean; error?: string }>('/message/test')
    banner.value = d.success
      ? { status: 'ok', title: '测试消息已发送', detail: '请到对应的机器人会话里查收' }
      : { status: 'err', title: '发送失败', detail: d.error || '请检查消息配置' }
  } catch (e) {
    banner.value = { status: 'err', title: '发送失败', detail: e instanceof Error ? e.message : '' }
  } finally {
    testing.value = false
  }
}

// OneBot 的事件回调 token：没有就地生成一个，保存后随整份配置落库
function genToken() {
  const cs = 'abcdef0123456789'
  return Array.from({ length: 16 }, () => cs[Math.floor(Math.random() * cs.length)]).join('')
}
watch(
  () => model.value.qq_onebot.event_token,
  (v) => {
    if (!v) model.value.qq_onebot.event_token = genToken()
  },
  { immediate: true },
)

const onebotCallback = computed(
  () => `${location.origin}/onebot/event?token=${model.value.qq_onebot.event_token}`,
)

/** 页签标题带启用状态点，一眼看出开了哪几个通道 */
const channels = computed(() => ({
  wecom: model.value.wecom.enabled,
  tg: model.value.tg.enabled,
  feishu: model.value.feishu.enabled,
  onebot: model.value.qq_onebot.enabled,
  qqoff: model.value.qq_official.enabled,
}))

const TABS = computed(() => [
  { value: 'wecom', label: '企业微信', dot: channels.value.wecom },
  { value: 'tg', label: 'TG 机器人', dot: channels.value.tg },
  { value: 'feishu', label: '飞书', dot: channels.value.feishu },
  { value: 'onebot', label: 'QQ · OneBot', dot: channels.value.onebot },
  { value: 'qqoff', label: 'QQ 官方机器人', dot: channels.value.qqoff },
])
</script>

<template>
  <div class="h-tabs-page">
<div class="msg-tabs">
      <HTabs v-model="tab" :items="TABS" />
      <HChip class="msg-count">已启用 {{ Object.values(channels).filter(Boolean).length }} / 5</HChip>
    </div>
<template v-if="tab === 'wecom'">
      
      <SectionCard title="企业微信" hint="通知推送 + 双向机器人">
        <FieldRow label="企业 ID" tip="企业微信「我的企业」页的 CorpID（ww 开头）。">
          <HInput v-model="model.wecom.corp_id" placeholder="corpid" :input-attrs="plainProps('wecom-corp-id')" />
        </FieldRow>
        <FieldRow label="应用 Secret" tip="自建应用的凭证，泄露后请在企微后台重置。">
          <SecretInput v-model="model.wecom.secret" name="wecom-app-secret" placeholder="secret" />
        </FieldRow>
        <FieldRow label="Agent ID" tip="自建应用的数字 ID。">
          <HInput v-model="model.wecom.agent_id" placeholder="agentid" :input-attrs="plainProps('wecom-agent-id')" />
        </FieldRow>
        <FieldRow
          label="API 地址"
          tip="默认官方 https://qyapi.weixin.qq.com；海外部署访问官方 API 慢或不通时可改为自建反代。一般保持默认。"
        >
          <HInput v-model="model.wecom.api_url" />
        </FieldRow>
        <FieldRow label="Token" tip="企微「接收消息服务器配置」的 Token——机器人互动必填，仅发通知可不填。">
          <SecretInput v-model="model.wecom.token" name="wecom-callback-token" placeholder="机器人互动用，可留空" />
        </FieldRow>
        <FieldRow label="EncodingAESKey" tip="企微「接收消息服务器配置」的 43 位密钥——机器人互动必填。">
          <SecretInput
            v-model="model.wecom.encoding_aes_key"
            name="wecom-aes-key"
            placeholder="43 位，机器人互动用"
          />
        </FieldRow>

        <HAlert status="accent" class="note">
          机器人互动：企业微信后台「接收消息」填写 URL
          <code>http://&lt;公网IP&gt;:6086/wecom/callback</code> 及上面的 Token/AESKey。
          聊天底栏菜单默认自动生成，直接给应用发消息即可：<b>磁力 / ed2k / HTTP 链接</b>提交离线下载，
          <b>115 分享链接</b>（链接与提取码发在一起）自动转存并整理入库；另支持指令：下载 / 状态 / 搜索 / 整理 / 同步 / 补全 / 帮助。
        </HAlert>

        <FieldRow label="状态" tip="启用后任务完成推送企业微信通知；配置 Token/AESKey 后可用机器人远程控制。">
          <HSegmented v-model="model.wecom.enabled" :options="[{ label: '启用', value: true }, { label: '禁用', value: false }]" />
        </FieldRow>

        <FormActions>
          <HButton variant="primary" :loading="saving" @click="save()">保存配置</HButton>
          <HButton variant="tertiary" :loading="testing" @click="test">测试通知</HButton>
          <HButton variant="tertiary" @click="reset">重置全部通道</HButton>
        </FormActions>
        <TestBanner :state="banner" />
      </SectionCard>
</template>
<template v-if="tab === 'tg'">
      
      <SectionCard title="Telegram 机器人" hint="通知推送 + 私聊指令">
        <HAlert status="accent" class="note-top">
          {{ tgStatus }}。私聊发送 /help 查看指令，搜索结果可点击按钮或回复序号。
          服务重启或重新启用后不补执行离线期间的指令，请重新发送。
        </HAlert>
        <FieldRow label="Bot Token" tip="Telegram BotFather 创建机器人后的 Token。">
          <SecretInput v-model="model.tg.token" name="tg-bot-token" placeholder="123456:ABC..." />
        </FieldRow>
        <FieldRow label="Chat ID" tip="通知接收目标。填写个人私聊 ID 时，该会话同时可以操作机器人；群和频道只接收通知。启用后可私聊机器人发送 /id 查询。">
          <HInput v-model="model.tg.chat_id" placeholder="接收消息的 chat id" />
        </FieldRow>
        <FieldRow label="状态" tip="启用后同时提供通知推送和私聊指令，仅配置的个人 Chat ID 可以操作。">
          <HSegmented v-model="model.tg.enabled" :options="[{ label: '启用', value: true }, { label: '禁用', value: false }]" />
        </FieldRow>
        <FormActions>
          <HButton variant="primary" :loading="saving" @click="save()">保存配置</HButton>
          <HButton variant="tertiary" :loading="testing" @click="test">测试通知</HButton>
        </FormActions>
        <TestBanner :state="banner" />
      </SectionCard>
</template>
<template v-if="tab === 'feishu'">
      
      <SectionCard title="飞书">
        <HAlert status="accent" class="note-top">
          飞书群「自定义机器人」：群设置 → 群机器人 → 添加自定义机器人，拿到 Webhook
          地址（可加签名校验）。仅支持接收通知，不支持指令。
        </HAlert>
        <FieldRow label="Webhook 地址" tip="形如 https://open.feishu.cn/open-apis/bot/v2/hook/xxxx。">
          <HInput v-model="model.feishu.webhook" placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/xxxx" />
        </FieldRow>
        <FieldRow label="签名密钥" tip="添加机器人时若开启了「签名校验」才需要填，否则留空。">
          <SecretInput v-model="model.feishu.secret" name="feishu-sign-secret" placeholder="未开启签名校验则留空" />
        </FieldRow>
        <FieldRow label="状态">
          <HSegmented v-model="model.feishu.enabled" :options="[{ label: '启用', value: true }, { label: '禁用', value: false }]" />
        </FieldRow>
        <FormActions>
          <HButton variant="primary" :loading="saving" @click="save()">保存配置</HButton>
          <HButton variant="tertiary" :loading="testing" @click="test">测试通知</HButton>
        </FormActions>
        <TestBanner :state="banner" />
      </SectionCard>
</template>
<template v-if="tab === 'onebot'">
      
      <SectionCard title="QQ · OneBot">
        <HAlert status="accent" class="note-top">
          对接 NapCat / Lagrange / LLOneBot 等 OneBot 实现：需要你自己运行一个 QQ
          客户端容器并登录。支持通知推送 + 私聊指令（整理 / 同步 / 状态等，与管理后台互斥）。
        </HAlert>
        <FieldRow
          label="HTTP 地址"
          tip="OneBot 实现的 HTTP 服务地址，如 http://127.0.0.1:3000（与本服务同一 compose 网络时用容器名）。"
        >
          <HInput v-model="model.qq_onebot.url" placeholder="http://127.0.0.1:3000" :input-attrs="plainProps('onebot-url')" />
        </FieldRow>
        <FieldRow label="Access Token" tip="OneBot 服务端配置的 access_token，未设置则留空。">
          <SecretInput
            v-model="model.qq_onebot.token"
            name="onebot-access-token"
            placeholder="与服务端 access_token 一致，可留空"
          />
        </FieldRow>
        <FieldRow label="消息目标类型">
          <HSegmented v-model="model.qq_onebot.target_type" :options="[{ label: '群聊', value: 'group' }, { label: '私聊', value: 'private' }]" />
        </FieldRow>
        <FieldRow label="目标群号 / QQ 号">
          <HInput v-model="model.qq_onebot.target" placeholder="接收通知的群号或 QQ 号" />
        </FieldRow>
        <FieldRow
          label="管理 QQ（指令用）"
          tip="填你的 QQ 号。该 QQ 私聊机器人发送 整理/同步/状态 等指令会触发任务；留空则不启用双向指令。"
        >
          <HInput v-model="model.qq_onebot.admin" placeholder="你的 QQ 号（私聊指令）" />
        </FieldRow>
        <FieldRow
          label="事件回调 Token"
          wide
          tip="OneBot「HTTP POST 上报」推事件到本服务时携带的鉴权 token。把下方回调地址填到 OneBot 的 HTTP POST 配置里。"
        >
          <HInput v-model="model.qq_onebot.event_token" />
          <CopyBox class="cb" :value="onebotCallback" />
        </FieldRow>
        <FieldRow label="状态">
          <HSegmented v-model="model.qq_onebot.enabled" :options="[{ label: '启用', value: true }, { label: '禁用', value: false }]" />
        </FieldRow>
        <FormActions>
          <HButton variant="primary" :loading="saving" @click="save()">保存配置</HButton>
          <HButton variant="tertiary" :loading="testing" @click="test">测试通知</HButton>
        </FormActions>
        <TestBanner :state="banner" />
      </SectionCard>
</template>
<template v-if="tab === 'qqoff'">
      
      <SectionCard title="QQ 官方机器人">
        <HAlert status="warning" class="note-top">
          q.qq.com 注册的官方机器人：创建沙箱/正式群并获取群 ID（group_openid，可从官方回调事件里查看）。
          注意官方群消息有审核与 @ 限制，适合通知推送，发送可能被平台拒绝。
        </HAlert>
        <FieldRow label="AppID" tip="q.qq.com 机器人管理页的 AppID。">
          <HInput v-model="model.qq_official.app_id" placeholder="机器人 AppID" :input-attrs="plainProps('qqoff-app-id')" />
        </FieldRow>
        <FieldRow label="AppSecret" tip="机器人管理页的密钥，用于换取 access_token。">
          <SecretInput v-model="model.qq_official.secret" name="qqoff-app-secret" placeholder="机器人密钥" />
        </FieldRow>
        <FieldRow label="群 ID" tip="接收通知的群 ID（group_openid）。把机器人拉进群后，从官方事件推送里可以查到。">
          <HInput v-model="model.qq_official.group_id" placeholder="group_openid" />
        </FieldRow>
        <FieldRow label="状态">
          <HSegmented v-model="model.qq_official.enabled" :options="[{ label: '启用', value: true }, { label: '禁用', value: false }]" />
        </FieldRow>
        <FormActions>
          <HButton variant="primary" :loading="saving" @click="save()">保存配置</HButton>
          <HButton variant="tertiary" :loading="testing" @click="test">测试通知</HButton>
        </FormActions>
        <TestBanner :state="banner" />
      </SectionCard>
</template>
</div>
</template>

<style scoped>
.msg-tabs {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
}
.msg-tabs > :first-child {
  min-width: 0;
}
.msg-count {
  flex: none;
  margin-left: auto;
}
/* 手机：五个通道页签自己就要横滑，计数挪到页签上方一行，别再跟它抢宽度 */
@media (max-width: 720px) {
  .msg-tabs {
    flex-direction: column-reverse;
    align-items: stretch;
    gap: 8px;
  }
  .msg-count {
    align-self: flex-end;
  }
}

.tab-label {
  display: inline-flex;
  align-items: center;
  gap: 5px;
}
.dot {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: var(--c-success);
}
.note {
  margin: 10px 0;
}
.note-top {
  margin-bottom: 10px;
}
.cb {
  margin-top: 8px;
}
</style>
