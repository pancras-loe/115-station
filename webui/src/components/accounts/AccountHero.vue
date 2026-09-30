<script setup lang="ts">
import { computed } from 'vue'
import { ChevronDown, Crown, QrCode, ScanLine, ShieldCheck, Smartphone } from '@lucide/vue'
import HButton from '@/components/hero/HButton.vue'
import HPopover from '@/components/hero/HPopover.vue'
import HSkeleton from '@/components/hero/HSkeleton.vue'
import MeterBar from '@/components/ui/MeterBar.vue'
import type { StorageCheck } from '@/types/storage'
import { bytes } from '@/utils/format'

/**
 * 「账号与媒体库」顶部的账号条。刻意压成一行（约 76px 高）：这一页真正要改的是下面的表单，
 * 账号信息只需要扫一眼确认「绑着、是谁、还剩多少空间」，不该占掉半屏把表单挤到折叠线以下。
 * 登录设备列表放进点击弹出的浮层，不在条里展开。
 *
 * 三种样子：加载中（骨架）/ 没绑定（同一行里给扫码主按钮）/ 已绑定。
 */
const props = defineProps<{ account: StorageCheck | null; loading?: boolean; checking?: boolean }>()
const emit = defineEmits<{ check: []; scan: [] }>()

const isForever = computed(() => props.account?.vip_forever === 1 || props.account?.vip_forever === true)
const isVip = computed(() => isForever.value || (props.account?.vip ?? 0) > 0)

const vipText = computed(() => {
  const a = props.account
  if (!a) return ''
  if (isForever.value) return '终身会员'
  if (!isVip.value) return '非会员'
  if (a.vip_expire && a.vip_expire > 0) {
    const days = Math.ceil((a.vip_expire * 1000 - Date.now()) / 86_400_000)
    const date = new Date(a.vip_expire * 1000).toLocaleDateString('zh-CN')
    return days > 0 ? `会员 ${date} 到期（剩 ${days} 天）` : `会员 ${date} 已到期`
  }
  return '会员'
})
/** 会员 30 天内到期给个醒 */
const vipSoon = computed(() => {
  const e = props.account?.vip_expire
  if (!e || isForever.value) return false
  const days = (e * 1000 - Date.now()) / 86_400_000
  return days > 0 && days <= 30
})

const usedPct = computed(() => {
  const a = props.account
  if (!a?.total_size) return 0
  return Math.min(100, ((a.used_size ?? 0) / a.total_size) * 100)
})

const initial = computed(() => (props.account?.username || '1').slice(0, 1).toUpperCase())

function when(t?: number) {
  if (!t || t <= 0) return ''
  const m = Math.round((Date.now() - t * 1000) / 60000)
  if (m < 60) return m <= 1 ? '刚刚' : `${m} 分钟前`
  const h = Math.round(m / 60)
  if (h < 24) return `${h} 小时前`
  const d = Math.round(h / 24)
  return d < 30 ? `${d} 天前` : new Date(t * 1000).toLocaleDateString('zh-CN')
}
</script>

<template>
  <section class="card card--default acct" :class="{ 'is-empty': !loading && !account }">
    <!-- ==== 加载中 ==== -->
    <template v-if="loading && !account">
      <HSkeleton width="44px" height="44px" radius="999px" />
      <div class="id">
        <HSkeleton width="140px" height="16px" radius="6px" />
        <HSkeleton width="220px" height="11px" radius="999px" style="margin-top: 8px" />
      </div>
    </template>

    <!-- ==== 没绑定 ==== -->
    <template v-else-if="!account">
      <span class="empty-icon"><QrCode :size="22" :stroke-width="1.8" /></span>
      <div class="id">
        <div class="name-row"><span class="name">尚未绑定 115 账号</span></div>
        <div class="sub">用 115 App 扫码即可完成绑定；已有 Cookie 文件的话，确认下方路径后点「检测可用性」</div>
      </div>
      <div class="actions">
        <HButton variant="tertiary" size="sm" :loading="checking" @click="emit('check')">
          <template #icon><ShieldCheck :size="15" /></template>
          检测可用性
        </HButton>
        <HButton variant="primary" size="sm" @click="emit('scan')">
          <template #icon><ScanLine :size="15" /></template>
          扫码绑定
        </HButton>
      </div>
    </template>

    <!-- ==== 已绑定 ==== -->
    <template v-else>
      <div class="avatar" :class="{ 'is-vip': isVip }">
        <img v-if="account.avatar" :src="account.avatar" alt="" />
        <span v-else>{{ initial }}</span>
        <span class="avatar-ok" title="账号可用" />
      </div>

      <div class="id">
        <div class="name-row">
          <span class="name">{{ account.username || '115 用户' }}</span>
          <span v-if="isVip" class="pill pill-vip">
            <Crown :size="11" :stroke-width="2.2" />{{ isForever ? '终身 VIP' : 'VIP' }}
          </span>
          <span class="pill">{{ account.channel || 'Cookie' }}</span>
        </div>
        <div class="sub">
          <span v-if="account.user_id">UID {{ account.user_id }}</span>
          <span :class="{ 'is-warn': vipSoon }">{{ vipText }}</span>
        </div>
      </div>

      <div class="sep" />

      <div class="cap" :title="`已用 ${usedPct.toFixed(1)}%`">
        <div class="cap-head">
          <span class="cap-label">网盘容量</span>
          <span v-if="account.total_size" class="cap-value">
            {{ bytes(account.used_size) }}<small> / {{ bytes(account.total_size) }}</small>
          </span>
          <span v-else class="cap-value">{{ account.capacity || '—' }}</span>
        </div>
        <MeterBar v-if="account.total_size" :percent="usedPct" />
      </div>

      <template v-if="account.devices?.length">
        <div class="sep" />
        <HPopover align="end" content-class="acct-dev-pop">
          <button type="button" class="dev-btn" :aria-label="`登录设备 ${account.devices.length} 台`">
            <Smartphone :size="15" />
            <span><b>{{ account.devices.length }}</b> 台设备</span>
            <ChevronDown :size="14" class="dev-chev" />
          </button>
          <template #content>
            <div class="acct-dev-title">登录设备</div>
            <div v-for="(d, i) in account.devices" :key="i" class="acct-dev">
              <span class="acct-dev-dot" :class="{ current: d.is_current }" />
              <div class="acct-dev-body">
                <div class="acct-dev-name">
                  {{ d.name || d.device || '未知设备' }}
                  <span v-if="d.is_current" class="acct-dev-tag">当前</span>
                </div>
                <div class="acct-dev-meta">{{ d.ip }}{{ d.city ? ` · ${d.city}` : '' }}</div>
              </div>
              <span class="acct-dev-time">{{ when(d.utime) }}</span>
            </div>
          </template>
        </HPopover>
      </template>

      <div class="actions">
        <HButton variant="tertiary" size="sm" :loading="checking" @click="emit('check')">
          <template #icon><ShieldCheck :size="15" /></template>
          检测
        </HButton>
        <HButton variant="tertiary" size="sm" @click="emit('scan')">
          <template #icon><ScanLine :size="15" /></template>
          重新扫码
        </HButton>
      </div>
    </template>
  </section>
</template>

<style scoped>
.acct {
  flex-direction: row;
  align-items: center;
  gap: 14px;
  padding: 14px 18px;
  min-height: 76px;
  box-sizing: border-box;
}
/* 没绑定：左缘一道强调色，同一行里就把「这里要先做」说清楚 */
.acct.is-empty {
  box-shadow:
    inset 3px 0 0 var(--accent),
    var(--surface-shadow);
}

.avatar {
  position: relative;
  width: 44px;
  height: 44px;
  flex-shrink: 0;
  border-radius: 999px;
  display: grid;
  place-items: center;
  background: var(--accent-soft);
  color: var(--accent);
  font-size: 17px;
  font-weight: 700;
}
.avatar.is-vip {
  box-shadow:
    0 0 0 2px var(--surface),
    0 0 0 3.5px var(--accent);
}
.avatar img {
  width: 100%;
  height: 100%;
  border-radius: inherit;
  object-fit: cover;
}
.avatar-ok {
  position: absolute;
  right: 0;
  bottom: 0;
  width: 11px;
  height: 11px;
  border-radius: 999px;
  background: var(--success);
  box-shadow: 0 0 0 2.5px var(--surface);
}
.empty-icon {
  width: 44px;
  height: 44px;
  flex-shrink: 0;
  border-radius: 13px;
  display: grid;
  place-items: center;
  background: var(--accent-soft);
  color: var(--accent);
}

.id {
  flex: 1 1 auto;
  min-width: 0;
}
.name-row {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.name {
  font-size: 15.5px;
  font-weight: 700;
  letter-spacing: -0.01em;
  color: var(--foreground);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.pill {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  flex-shrink: 0;
  height: 20px;
  padding: 0 8px;
  border-radius: 999px;
  background: var(--default);
  color: var(--muted);
  font-size: 11.5px;
  font-weight: 500;
}
.pill-vip {
  background: var(--accent-soft);
  color: var(--accent-soft-foreground);
}
.sub {
  display: flex;
  gap: 12px;
  margin-top: 3px;
  font-size: 12.5px;
  color: var(--muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  font-variant-numeric: tabular-nums;
}
.sub .is-warn {
  color: var(--warning);
  font-weight: 500;
}

.sep {
  width: 1px;
  height: 32px;
  flex-shrink: 0;
  background: var(--separator);
}

.cap {
  width: 220px;
  flex-shrink: 0;
}
.cap-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 6px;
}
.cap-label {
  font-size: 12px;
  color: var(--muted);
}
.cap-value {
  font-size: 14px;
  font-weight: 700;
  color: var(--foreground);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.cap-value small {
  font-size: 12px;
  font-weight: 500;
  color: var(--muted);
}

.dev-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
  height: 32px;
  padding: 0 10px;
  border: 0;
  border-radius: 10px;
  background: transparent;
  color: var(--muted);
  font: inherit;
  font-size: 12.5px;
  cursor: pointer;
  transition:
    background-color 150ms ease,
    color 150ms ease;
}
.dev-btn b {
  font-weight: 700;
  color: var(--foreground);
}
.dev-btn:hover,
.dev-btn[data-state='open'] {
  background: var(--default);
  color: var(--foreground);
}
.dev-chev {
  transition: transform 150ms ease;
}
.dev-btn[data-state='open'] .dev-chev {
  transform: rotate(180deg);
}

.actions {
  display: flex;
  gap: 6px;
  flex-shrink: 0;
}

/* 窄屏：身份占第一行，容量 / 设备 / 按钮换到第二行 */
@media (max-width: 1080px) {
  .acct {
    flex-wrap: wrap;
    row-gap: 12px;
  }
  .id {
    flex-basis: calc(100% - 60px);
  }
  .sep {
    display: none;
  }
  .cap {
    flex: 1 1 180px;
    width: auto;
  }
  .actions {
    margin-left: auto;
  }
  .is-empty .actions {
    margin-left: 58px;
  }
}
@media (max-width: 720px) {
  .sub {
    flex-wrap: wrap;
    white-space: normal;
    gap: 2px 12px;
  }
}
</style>

<!-- 设备浮层在 Portal 里，scoped 样式选不中，这一块不加 scoped，类名都带 acct- 前缀 -->
<style>
.acct-dev-pop {
  width: 300px;
  padding: 8px;
}
.acct-dev-title {
  padding: 4px 8px 8px;
  font-size: 12px;
  font-weight: 600;
  color: var(--muted);
}
.acct-dev {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px;
  border-radius: 10px;
}
.acct-dev:hover {
  background: var(--default);
}
.acct-dev-dot {
  width: 7px;
  height: 7px;
  flex-shrink: 0;
  border-radius: 50%;
  background: var(--c-text-4);
}
.acct-dev-dot.current {
  background: var(--success);
  box-shadow: 0 0 0 3px var(--success-soft);
}
.acct-dev-body {
  flex: 1;
  min-width: 0;
}
.acct-dev-name {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  font-weight: 500;
  color: var(--foreground);
}
.acct-dev-tag {
  padding: 0 6px;
  border-radius: 999px;
  background: var(--success-soft);
  color: var(--success-soft-foreground);
  font-size: 11px;
  font-weight: 400;
  line-height: 17px;
}
.acct-dev-meta {
  margin-top: 1px;
  font-size: 12px;
  color: var(--muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.acct-dev-time {
  flex-shrink: 0;
  font-size: 12px;
  color: var(--muted);
  font-variant-numeric: tabular-nums;
}
</style>
