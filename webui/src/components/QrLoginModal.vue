<script setup lang="ts">
import { ref, watch } from 'vue'
import HSpinner from '@/components/hero/HSpinner.vue'
import HModal from '@/components/hero/HModal.vue'
import { storageApi } from '@/api'
import type { QrChannel } from '@/api/storage'

const props = defineProps<{
  show: boolean
  channel: QrChannel
  device: string
  appId: string
}>()
const emit = defineEmits<{ 'update:show': [boolean]; success: [] }>()

const qrSrc = ref('')
const statusText = ref('')
const loading = ref(false)
const failed = ref(false)

/**
 * 轮询会话令牌。用对象身份而不是布尔量：关闭弹窗后立刻又打开会产生两条
 * 轮询链，只有「当前会话 === 我这条」才继续，旧链自然退出。
 */
let session: object | null = null
/** 二维码有效期约 2 分钟，过期自动换新码，最多 3 次（避免无人值守时无限拉码） */
let autoRefresh = 0

function stop() {
  session = null
}

async function open(isAuto = false) {
  if (!isAuto) autoRefresh = 0
  stop()
  loading.value = true
  failed.value = false
  qrSrc.value = ''
  statusText.value = props.channel === 'openapi' ? '正在获取开放平台授权二维码...' : '正在获取登录二维码...'

  try {
    const body =
      props.channel === 'openapi'
        ? { type: '115', app_id: props.appId.trim() }
        : { type: '115', device: props.device }
    const data = await storageApi.createQrCode(props.channel, body)
    if (!data.qrcode) {
      failed.value = true
      statusText.value = data.error || '获取二维码失败，请稍后重试'
      return
    }
    qrSrc.value = data.qrcode
    statusText.value =
      props.channel === 'openapi' ? '请使用 115 手机 App 扫码（开放平台授权）' : '请使用 115 手机 App 扫描二维码'
    poll(data.uid, data.time, data.sign)
  } catch (e) {
    failed.value = true
    statusText.value = e instanceof Error ? e.message : '二维码获取失败'
  } finally {
    loading.value = false
  }
}

function poll(uid?: string, time?: number | string, sign?: string) {
  const mine = {}
  session = mine
  let errCount = 0

  const tick = async () => {
    if (session !== mine) return
    try {
      const data = await storageApi.qrStatus(props.channel, { uid, time, sign })
      if (session !== mine) return
      errCount = 0

      if (data.status === 'scanned') {
        statusText.value = '已扫码，请在手机上确认登录...'
        tick()
      } else if (data.status === 'success') {
        statusText.value = '登录成功！'
        stop()
        setTimeout(() => {
          emit('update:show', false)
          emit('success')
        }, 1000)
      } else if (data.status === 'expired' && autoRefresh < 3) {
        autoRefresh++
        statusText.value = `二维码已过期，正在自动刷新（第 ${autoRefresh}/3 次）...`
        open(true)
      } else if (data.status === 'expired' || data.status === 'cancelled') {
        stop()
        statusText.value = data.status === 'expired' ? '二维码已过期，请关闭后重新获取' : '已取消登录'
      } else {
        tick() // waiting：服务端是长轮询，直接续上
      }
    } catch (e) {
      if (session !== mine) return
      // 只有网络层故障（TypeError = Failed to fetch）才退避重试。
      // 服务端返回的错误（115 拒绝登录 / IP 风控）是确定性失败，
      // 重试只会加重风控，直接展示并停止。
      if (e instanceof TypeError) {
        errCount++
        const delay = Math.min(30_000, 1000 * 2 ** (errCount - 1))
        statusText.value = `网络波动，${delay / 1000} 秒后重试...`
        setTimeout(tick, delay)
      } else {
        stop()
        statusText.value = e instanceof Error ? e.message : '登录失败'
      }
    }
  }
  tick()
}

watch(
  () => props.show,
  (v) => (v ? open() : stop()),
)
</script>

<template>
  <HModal :show="show" title="扫码登录 115" width="340px" @update:show="emit('update:show', $event)">
    <div class="qr">
      <div class="qr-frame">
        <HSpinner v-if="loading" size="sm" />
        <img v-else-if="qrSrc" :src="qrSrc" alt="115 登录二维码" />
        <span v-else class="qr-fail">二维码获取失败</span>
      </div>
      <p class="qr-status" :class="{ fail: failed }">{{ statusText }}</p>
    </div>
  </HModal>
</template>

<style scoped>
.qr {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 14px;
}
.qr-frame {
  width: 186px;
  height: 186px;
  display: grid;
  place-items: center;
  padding: 8px;
  border-radius: var(--radius);
  /* 二维码本身是黑白图，暗色模式下必须保持白底，否则扫不出来 */
  background: #fff;
  border: 1px solid var(--c-border);
}
.qr-frame img {
  width: 100%;
  height: 100%;
  display: block;
}
.qr-fail {
  font-size: 12.5px;
  color: #86909c;
}
.qr-status {
  margin: 0;
  text-align: center;
  font-size: 12.5px;
  line-height: 1.6;
  color: var(--c-text-2);
}
.qr-status.fail {
  color: var(--c-danger);
}
</style>
