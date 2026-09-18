/** 五个通道共用 Setting 表的同一个 key（'message'），所以整页是一份配置对象 */
export interface MessageConfig {
  wecom: {
    corp_id: string
    secret: string
    agent_id: string
    api_url: string
    token: string
    encoding_aes_key: string
    enabled: boolean
  }
  tg: { token: string; chat_id: string; enabled: boolean }
  feishu: { webhook: string; secret: string; enabled: boolean }
  qq_onebot: {
    url: string
    token: string
    target_type: 'group' | 'private'
    target: string
    admin: string
    event_token: string
    enabled: boolean
  }
  qq_official: { app_id: string; secret: string; group_id: string; enabled: boolean }
}

export const MESSAGE_DEFAULTS: MessageConfig = {
  wecom: {
    corp_id: '',
    secret: '',
    agent_id: '',
    api_url: 'https://qyapi.weixin.qq.com',
    token: '',
    encoding_aes_key: '',
    enabled: false,
  },
  tg: { token: '', chat_id: '', enabled: false },
  feishu: { webhook: '', secret: '', enabled: false },
  qq_onebot: {
    url: '',
    token: '',
    target_type: 'group',
    target: '',
    admin: '',
    event_token: '',
    enabled: false,
  },
  qq_official: { app_id: '', secret: '', group_id: '', enabled: false },
}
