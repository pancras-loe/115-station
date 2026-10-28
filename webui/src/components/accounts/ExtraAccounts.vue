<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Plus, QrCode, ShieldCheck, Trash2 } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import QrLoginModal from '@/components/QrLoginModal.vue'
import { storageApi } from '@/api'
import type { Account115 } from '@/api/storage'
import { DEFAULT_DEVICE } from '@/types/storage'
import { toastError, useFeedback } from '@/composables/useFeedback'

/**
 * 附加 115 账号（后端 account115.go，MULTI-ACCOUNT-V2-PLAN.md §10）。
 *
 * 只管登录、检测、删除。名字就是 115 用户名（后端按它显示），不另起昵称。
 * 登录好的账号在「媒体库」里建库时可选，整理与 STRM 和主账号一样；
 * 订阅还只进主账号的库 —— 文案里要说清楚。
 */

/** 每个账号挂了几个库（页面按媒体库表算好；读不到时不显示） */
const props = defineProps<{ libCount?: Record<number, number> }>()
const { message, dialog } = useFeedback()

const enabled = ref(false)
const list = ref<Account115[]>([])
const extras = computed(() => list.value.filter((a) => !a.main))
const busy = ref(0)

async function load() {
  try {
    const r = await storageApi.accounts()
    enabled.value = true
    list.value = r.data ?? []
  } catch {
    enabled.value = false // 老后端没有这个接口：不显示
  }
}
onMounted(load)

function statusOf(a: Account115): { text: string; color: 'success' | 'warning' | 'danger' | 'default' } {
  if (!a.logged_in) return { text: '未登录', color: 'default' }
  if (a.status === 'invalid') return { text: 'Cookie 失效', color: 'danger' }
  if (a.status === 'online') return { text: '已登录', color: 'success' }
  return { text: '未检测', color: 'warning' }
}

// ---- 添加 ----

const adding = ref(false)
/** 建一行就去扫码，名字等登录后用 115 用户名 */
async function add() {
  adding.value = true
  try {
    const r = await storageApi.createAccount()
    await load()
    scan(r.data.id)
  } catch (e) {
    toastError(e, '添加失败')
  } finally {
    adding.value = false
  }
}

// ---- 登录 ----

const qr = ref<{ show: boolean; id: number }>({ show: false, id: 0 })
function scan(id: number) {
  qr.value = { show: true, id }
}
async function onScanned() {
  message.success('附加账号已登录')
  await load()
}

async function check(a: Account115) {
  busy.value = a.id
  try {
    const r = await storageApi.checkAccount(a.id)
    if (r.valid) message.success(`「${a.name}」有效：${r.username}，${r.capacity}`)
    else message.error(`「${a.name}」${r.message || 'Cookie 无效'}`)
    await load()
  } catch (e) {
    toastError(e, '检测失败')
  } finally {
    busy.value = 0
  }
}

async function remove(a: Account115) {
  const ok = await dialog.confirm({
    title: `删除附加账号「${a.name}」`,
    content: props.libCount?.[a.id]
      ? `这个账号上还有 ${props.libCount[a.id]} 个媒体库，删了账号它们就同步不了。只删本站记下的登录信息，115 网盘上的东西不动。`
      : '只删本站记下的登录信息，115 网盘上的东西不动。',
    actions: [
      { label: '取消', value: false, variant: 'tertiary' },
      { label: '删除', value: true, variant: 'danger' },
    ],
  })
  if (!ok) return
  try {
    await storageApi.deleteAccount(a.id)
    message.success('已删除')
    await load()
  } catch (e) {
    toastError(e, '删除失败')
  }
}
</script>

<template>
  <SectionCard v-if="enabled" title="附加账号" hint="建库时可选；整理、STRM、播放与主账号一样，订阅只进主账号的库">
    <template #extra>
      <HButton size="sm" variant="secondary" :loading="adding" @click="add"><Plus :size="14" />添加账号</HButton>
    </template>
    <ul v-if="extras.length" class="accts">
      <li v-for="a in extras" :key="a.id" class="acct">
        <span class="acct-avatar" :class="{ 'is-on': a.logged_in && a.status !== 'invalid' }">{{ a.name.slice(0, 1).toUpperCase() }}</span>
        <div class="acct-main">
          <div class="acct-line">
            <span class="acct-name">{{ a.name }}</span>
            <HChip :color="statusOf(a).color">{{ statusOf(a).text }}</HChip>
          </div>
          <div class="acct-sub">
            <span v-if="libCount">{{ libCount[a.id] ? `${libCount[a.id]} 个媒体库` : '还没有媒体库' }}</span>
          </div>
        </div>
        <div class="acct-ops">
          <HButton size="sm" variant="secondary" @click="scan(a.id)"><QrCode :size="14" />{{ a.logged_in ? '重新登录' : '扫码登录' }}</HButton>
          <HButton v-if="a.logged_in" size="sm" variant="ghost" :loading="busy === a.id" @click="check(a)"><ShieldCheck :size="14" />检测</HButton>
          <HButton size="sm" variant="ghost" :aria-label="`删除 ${a.name}`" @click="remove(a)"><Trash2 :size="14" /></HButton>
        </div>
      </li>
    </ul>
    <p v-else class="muted small empty">还没有附加账号。别的 115 账号登录进来后，在「媒体库」里建库时就能选它。</p>

    <QrLoginModal v-model:show="qr.show" channel="cookie" :device="DEFAULT_DEVICE" app-id="" :account="qr.id" @success="onScanned" />
  </SectionCard>
</template>

<style scoped>
.accts {
  list-style: none;
  margin: 0;
  padding: 0;
}
.acct {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 12px;
  padding: 12px 0;
  border-top: 1px solid var(--separator);
}
.acct:first-child {
  border-top: 0;
  padding-top: 0;
}
.acct:last-child {
  padding-bottom: 0;
}
.acct-avatar {
  width: 36px;
  height: 36px;
  flex-shrink: 0;
  border-radius: 999px;
  display: grid;
  place-items: center;
  background: var(--default);
  color: var(--muted);
  font-size: 14px;
  font-weight: 700;
}
.acct-avatar.is-on {
  background: var(--accent-soft);
  color: var(--accent);
}
.acct-main {
  flex: 1 1 200px;
  min-width: 0;
}
.acct-line {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.acct-name {
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.acct-sub {
  display: flex;
  flex-wrap: wrap;
  gap: 0 12px;
  margin-top: 2px;
  font-size: 12.5px;
  color: var(--muted);
}
.acct-ops {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}
.empty {
  margin: 0;
}
.muted {
  color: var(--muted);
}
.small {
  font-size: 12.5px;
}
</style>
