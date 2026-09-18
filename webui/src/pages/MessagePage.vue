<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import {
  NAlert,
  NButton,
  NInput,
  NRadioButton,
  NRadioGroup,
  NTabPane,
  NTabs,
  NTag,
} from 'naive-ui'
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
</script>

<template>
  <NTabs v-model:value="tab" type="line" animated>
    <NTabPane name="wecom">
      <template #tab>
        <span class="tab-label">企业微信<i v-if="channels.wecom" class="dot" /></span>
      </template>
      <SectionCard title="企业微信" hint="通知推送 + 双向机器人">
        <FieldRow label="企业 ID" tip="企业微信「我的企业」页的 CorpID（ww 开头）。">
          <NInput
            v-model:value="model.wecom.corp_id"
            placeholder="corpid"
            :input-props="plainProps('wecom-corp-id')"
          />
        </FieldRow>
        <FieldRow label="应用 Secret" tip="自建应用的凭证，泄露后请在企微后台重置。">
          <SecretInput v-model="model.wecom.secret" name="wecom-app-secret" placeholder="secret" />
        </FieldRow>
        <FieldRow label="Agent ID" tip="自建应用的数字 ID。">
          <NInput
            v-model:value="model.wecom.agent_id"
            placeholder="agentid"
            :input-props="plainProps('wecom-agent-id')"
          />
        </FieldRow>
        <FieldRow
          label="API 地址"
          tip="默认官方 https://qyapi.weixin.qq.com；海外部署访问官方 API 慢或不通时可改为自建反代。一般保持默认。"
        >
          <NInput v-model:value="model.wecom.api_url" />
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

        <NAlert class="note" type="info" :bordered="false">
          机器人互动：企业微信后台「接收消息」填写 URL
          <code>http://&lt;公网IP&gt;:6086/wecom/callback</code> 及上面的 Token/AESKey。
          聊天底栏菜单默认自动生成，直接给应用发消息即可：<b>磁力 / ed2k / HTTP 链接</b>提交离线下载，
          <b>115 分享链接</b>（链接与提取码发在一起）自动转存并整理入库；另支持指令：下载 / 状态 / 搜索 / 整理 / 同步 / 补全 / 帮助。
        </NAlert>

        <FieldRow label="状态" tip="启用后任务完成推送企业微信通知；配置 Token/AESKey 后可用机器人远程控制。">
          <NRadioGroup v-model:value="model.wecom.enabled">
            <NRadioButton :value="true">启用</NRadioButton>
            <NRadioButton :value="false">禁用</NRadioButton>
          </NRadioGroup>
        </FieldRow>

        <FormActions>
          <NButton type="primary" :loading="saving" @click="save()">保存配置</NButton>
          <NButton :loading="testing" @click="test">测试通知</NButton>
          <NButton @click="reset">重置全部通道</NButton>
        </FormActions>
        <TestBanner :state="banner" />
      </SectionCard>
    </NTabPane>

    <NTabPane name="tg">
      <template #tab>
        <span class="tab-label">TG 机器人<i v-if="channels.tg" class="dot" /></span>
      </template>
      <SectionCard title="Telegram 机器人">
        <FieldRow label="Bot Token" tip="Telegram BotFather 创建机器人后的 Token。">
          <SecretInput v-model="model.tg.token" name="tg-bot-token" placeholder="123456:ABC..." />
        </FieldRow>
        <FieldRow label="Chat ID" tip="Telegram 目标会话 ID，向 @userinfobot 发消息可查到。">
          <NInput v-model:value="model.tg.chat_id" placeholder="接收消息的 chat id" />
        </FieldRow>
        <FieldRow label="状态" tip="启用后任务完成推送 Telegram 通知。">
          <NRadioGroup v-model:value="model.tg.enabled">
            <NRadioButton :value="true">启用</NRadioButton>
            <NRadioButton :value="false">禁用</NRadioButton>
          </NRadioGroup>
        </FieldRow>
        <FormActions>
          <NButton type="primary" :loading="saving" @click="save()">保存配置</NButton>
          <NButton :loading="testing" @click="test">测试通知</NButton>
        </FormActions>
        <TestBanner :state="banner" />
      </SectionCard>
    </NTabPane>

    <NTabPane name="feishu">
      <template #tab>
        <span class="tab-label">飞书<i v-if="channels.feishu" class="dot" /></span>
      </template>
      <SectionCard title="飞书">
        <NAlert class="note-top" type="info" :bordered="false">
          飞书群「自定义机器人」：群设置 → 群机器人 → 添加自定义机器人，拿到 Webhook
          地址（可加签名校验）。仅支持接收通知，不支持指令。
        </NAlert>
        <FieldRow label="Webhook 地址" tip="形如 https://open.feishu.cn/open-apis/bot/v2/hook/xxxx。">
          <NInput v-model:value="model.feishu.webhook" placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/xxxx" />
        </FieldRow>
        <FieldRow label="签名密钥" tip="添加机器人时若开启了「签名校验」才需要填，否则留空。">
          <SecretInput v-model="model.feishu.secret" name="feishu-sign-secret" placeholder="未开启签名校验则留空" />
        </FieldRow>
        <FieldRow label="状态">
          <NRadioGroup v-model:value="model.feishu.enabled">
            <NRadioButton :value="true">启用</NRadioButton>
            <NRadioButton :value="false">禁用</NRadioButton>
          </NRadioGroup>
        </FieldRow>
        <FormActions>
          <NButton type="primary" :loading="saving" @click="save()">保存配置</NButton>
          <NButton :loading="testing" @click="test">测试通知</NButton>
        </FormActions>
        <TestBanner :state="banner" />
      </SectionCard>
    </NTabPane>

    <NTabPane name="onebot">
      <template #tab>
        <span class="tab-label">QQ · OneBot<i v-if="channels.onebot" class="dot" /></span>
      </template>
      <SectionCard title="QQ · OneBot">
        <NAlert class="note-top" type="info" :bordered="false">
          对接 NapCat / Lagrange / LLOneBot 等 OneBot 实现：需要你自己运行一个 QQ
          客户端容器并登录。支持通知推送 + 私聊指令（整理 / 同步 / 状态等，与管理后台互斥）。
        </NAlert>
        <FieldRow
          label="HTTP 地址"
          tip="OneBot 实现的 HTTP 服务地址，如 http://127.0.0.1:3000（与本服务同一 compose 网络时用容器名）。"
        >
          <NInput
            v-model:value="model.qq_onebot.url"
            placeholder="http://127.0.0.1:3000"
            :input-props="plainProps('onebot-url')"
          />
        </FieldRow>
        <FieldRow label="Access Token" tip="OneBot 服务端配置的 access_token，未设置则留空。">
          <SecretInput
            v-model="model.qq_onebot.token"
            name="onebot-access-token"
            placeholder="与服务端 access_token 一致，可留空"
          />
        </FieldRow>
        <FieldRow label="消息目标类型">
          <NRadioGroup v-model:value="model.qq_onebot.target_type">
            <NRadioButton value="group">群聊</NRadioButton>
            <NRadioButton value="private">私聊</NRadioButton>
          </NRadioGroup>
        </FieldRow>
        <FieldRow label="目标群号 / QQ 号">
          <NInput v-model:value="model.qq_onebot.target" placeholder="接收通知的群号或 QQ 号" />
        </FieldRow>
        <FieldRow
          label="管理 QQ（指令用）"
          tip="填你的 QQ 号。该 QQ 私聊机器人发送 整理/同步/状态 等指令会触发任务；留空则不启用双向指令。"
        >
          <NInput v-model:value="model.qq_onebot.admin" placeholder="你的 QQ 号（私聊指令）" />
        </FieldRow>
        <FieldRow
          label="事件回调 Token"
          wide
          tip="OneBot「HTTP POST 上报」推事件到本服务时携带的鉴权 token。把下方回调地址填到 OneBot 的 HTTP POST 配置里。"
        >
          <NInput v-model:value="model.qq_onebot.event_token" />
          <CopyBox class="cb" :value="onebotCallback" />
        </FieldRow>
        <FieldRow label="状态">
          <NRadioGroup v-model:value="model.qq_onebot.enabled">
            <NRadioButton :value="true">启用</NRadioButton>
            <NRadioButton :value="false">禁用</NRadioButton>
          </NRadioGroup>
        </FieldRow>
        <FormActions>
          <NButton type="primary" :loading="saving" @click="save()">保存配置</NButton>
          <NButton :loading="testing" @click="test">测试通知</NButton>
        </FormActions>
        <TestBanner :state="banner" />
      </SectionCard>
    </NTabPane>

    <NTabPane name="qqoff">
      <template #tab>
        <span class="tab-label">QQ 官方机器人<i v-if="channels.qqoff" class="dot" /></span>
      </template>
      <SectionCard title="QQ 官方机器人">
        <NAlert class="note-top" type="warning" :bordered="false">
          q.qq.com 注册的官方机器人：创建沙箱/正式群并获取群 ID（group_openid，可从官方回调事件里查看）。
          注意官方群消息有审核与 @ 限制，适合通知推送，发送可能被平台拒绝。
        </NAlert>
        <FieldRow label="AppID" tip="q.qq.com 机器人管理页的 AppID。">
          <NInput
            v-model:value="model.qq_official.app_id"
            placeholder="机器人 AppID"
            :input-props="plainProps('qqoff-app-id')"
          />
        </FieldRow>
        <FieldRow label="AppSecret" tip="机器人管理页的密钥，用于换取 access_token。">
          <SecretInput v-model="model.qq_official.secret" name="qqoff-app-secret" placeholder="机器人密钥" />
        </FieldRow>
        <FieldRow label="群 ID" tip="接收通知的群 ID（group_openid）。把机器人拉进群后，从官方事件推送里可以查到。">
          <NInput v-model:value="model.qq_official.group_id" placeholder="group_openid" />
        </FieldRow>
        <FieldRow label="状态">
          <NRadioGroup v-model:value="model.qq_official.enabled">
            <NRadioButton :value="true">启用</NRadioButton>
            <NRadioButton :value="false">禁用</NRadioButton>
          </NRadioGroup>
        </FieldRow>
        <FormActions>
          <NButton type="primary" :loading="saving" @click="save()">保存配置</NButton>
          <NButton :loading="testing" @click="test">测试通知</NButton>
        </FormActions>
        <TestBanner :state="banner" />
      </SectionCard>
    </NTabPane>

    <template #suffix>
      <NTag size="small" :bordered="false">
        已启用 {{ Object.values(channels).filter(Boolean).length }} / 5
      </NTag>
    </template>
  </NTabs>
</template>

<style scoped>
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
