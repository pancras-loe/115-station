<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import {
  NButton,
  NCheckbox,
  NInput,
  NInputNumber,
  NModal,
  NPopconfirm,
  NRadioButton,
  NRadioGroup,
  NSelect,
  NSwitch,
  NTag,
} from 'naive-ui'
import { Pencil, Play, Plus, Trash2 } from '@lucide/vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import { tgsubApi } from '@/api'
import type { TgItem, TgSource, TgSubConfig } from '@/api/tgsub'
import { toastError, useFeedback } from '@/composables/useFeedback'

const { message } = useFeedback()

type Kind = 'source' | 'item'
interface Row {
  kind: Kind
  id: number
  enabled: boolean
  main: string
  sub: string[]
}

const cfg = ref<TgSubConfig>({ sources: [], items: [], interval_min: 30 })
const loading = ref(true)
const loadError = ref('')
const running = ref(false)

const filterType = ref<'' | Kind>('')
const filterStatus = ref<'' | 'on' | 'off'>('')

async function load() {
  loading.value = true
  try {
    cfg.value = await tgsubApi.getConfig()
    loadError.value = ''
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

/** 整份配置写回：后端没有单条增删改接口 */
async function persist(done: string) {
  try {
    await tgsubApi.saveConfig(cfg.value)
    message.success(done)
    await load()
  } catch (e) {
    toastError(e, '保存失败')
    await load() // 失败时回到服务端的真实状态，避免界面停在乐观值上
  }
}

const rows = computed<Row[]>(() => {
  const srcs: Row[] = cfg.value.sources.map((x) => ({
    kind: 'source',
    id: Number(x.id ?? 0),
    enabled: x.enabled !== false,
    main: x.name || x.url,
    sub: [x.url, x.note, `优先级 ${x.priority || 10}`].filter(Boolean) as string[],
  }))
  const items: Row[] = cfg.value.items.map((x) => ({
    kind: 'item',
    id: Number(x.id ?? 0),
    enabled: x.enabled !== false,
    main: x.keyword,
    sub: [
      x.channels ? String(x.channels).split('\n').join(' ') : '全部 TG 群',
      x.auto ? '自动转存' : '',
      x.last_hit ? `最近命中 ${x.last_hit}` : '',
    ].filter(Boolean) as string[],
  }))
  return [...srcs, ...items].filter(
    (r) =>
      (!filterType.value || r.kind === filterType.value) &&
      (!filterStatus.value || (filterStatus.value === 'on' ? r.enabled : !r.enabled)),
  )
})

function listOf(kind: Kind) {
  return kind === 'source' ? cfg.value.sources : cfg.value.items
}

async function toggle(r: Row, on: boolean) {
  const target = (listOf(r.kind) as { id?: number; enabled: boolean }[]).find(
    (x) => String(x.id) === String(r.id),
  )
  if (!target) return
  target.enabled = on
  await persist(on ? '已启用' : '已停用')
}

async function remove(r: Row) {
  if (r.kind === 'source') {
    cfg.value.sources = cfg.value.sources.filter((x) => String(x.id) !== String(r.id))
  } else {
    cfg.value.items = cfg.value.items.filter((x) => String(x.id) !== String(r.id))
  }
  await persist('已删除')
}

async function runNow() {
  running.value = true
  try {
    const d = await tgsubApi.run()
    message.success(d.message || '抓取任务已触发')
  } catch (e) {
    toastError(e, '触发失败')
  } finally {
    running.value = false
  }
}

// ---- 新增 / 编辑弹窗 ----
const editShow = ref(false)
const editId = ref(0) // 0 = 新增
const editKind = ref<Kind>('item')
const editItem = ref<Pick<TgItem, 'keyword' | 'channels' | 'auto'>>({
  keyword: '',
  channels: '',
  auto: false,
})
const editSource = ref<Pick<TgSource, 'name' | 'url' | 'priority' | 'note'>>({
  name: '',
  url: '',
  priority: 10,
  note: '',
})

function openAdd() {
  editId.value = 0
  editKind.value = 'item'
  editItem.value = { keyword: '', channels: '', auto: false }
  editSource.value = { name: '', url: '', priority: 10, note: '' }
  editShow.value = true
}

function openEdit(r: Row) {
  editId.value = r.id
  editKind.value = r.kind
  if (r.kind === 'source') {
    const s = cfg.value.sources.find((x) => String(x.id) === String(r.id))
    if (s) editSource.value = { name: s.name, url: s.url, priority: s.priority || 10, note: s.note || '' }
  } else {
    const it = cfg.value.items.find((x) => String(x.id) === String(r.id))
    if (it) editItem.value = { keyword: it.keyword, channels: it.channels || '', auto: !!it.auto }
  }
  editShow.value = true
}

async function saveEdit() {
  if (editKind.value === 'source') {
    const f = editSource.value
    if (!f.name.trim() || !f.url.trim()) {
      message.warning('订阅名称和地址必填')
      return
    }
    const fields = { type: 'tg', name: f.name.trim(), url: f.url.trim(), priority: f.priority || 10, note: f.note.trim() }
    if (editId.value > 0) {
      const t = cfg.value.sources.find((x) => String(x.id) === String(editId.value))
      if (t) Object.assign(t, fields)
    } else {
      cfg.value.sources.push({ enabled: true, last_id: 0, ...fields })
    }
  } else {
    const f = editItem.value
    if (!f.keyword.trim()) {
      message.warning('请填写关键词')
      return
    }
    const fields = { keyword: f.keyword.trim(), channels: f.channels.trim(), auto: f.auto }
    if (editId.value > 0) {
      const t = cfg.value.items.find((x) => String(x.id) === String(editId.value))
      if (t) Object.assign(t, fields)
    } else {
      cfg.value.items.push({ enabled: true, last_id: 0, ...fields })
    }
  }
  editShow.value = false
  await persist(editId.value > 0 ? '已更新' : '已添加')
}

onMounted(load)
</script>

<template>
  <SectionCard title="订阅管理" hint="TG 频道关键词订阅 / 命中通知 / 自动转存">
    <template #extra>
      <div class="tools">
        <NSelect
          v-model:value="filterType"
          size="small"
          class="sel"
          :options="[
            { label: '全部类型', value: '' },
            { label: 'TG 群', value: 'source' },
            { label: '关键词订阅', value: 'item' },
          ]"
        />
        <NSelect
          v-model:value="filterStatus"
          size="small"
          class="sel"
          :options="[
            { label: '全部状态', value: '' },
            { label: '启用', value: 'on' },
            { label: '停用', value: 'off' },
          ]"
        />
        <NButton size="small" :loading="running" @click="runNow">
          <template #icon><Play :size="14" /></template>
          立即抓取
        </NButton>
        <NButton size="small" type="primary" @click="openAdd">
          <template #icon><Plus :size="14" /></template>
          新增
        </NButton>
      </div>
    </template>

    <p v-if="loadError" class="err">{{ loadError }}</p>
    <EmptyState
      v-else-if="!rows.length && !loading"
      :text="filterType || filterStatus ? '没有符合筛选条件的条目' : '还没有订阅，点右上角「新增」添加'"
    />

    <div v-else class="list">
      <div v-for="r in rows" :key="`${r.kind}-${r.id}`" class="row" :class="{ off: !r.enabled }">
        <NTag size="small" :bordered="false" :type="r.kind === 'source' ? 'info' : 'warning'">
          {{ r.kind === 'source' ? 'TG 群' : '关键词' }}
        </NTag>

        <div class="body">
          <div class="name">{{ r.main }}</div>
          <div class="meta">
            <span v-for="(s, i) in r.sub" :key="i" class="meta-item">{{ s }}</span>
          </div>
        </div>

        <NSwitch size="small" :value="r.enabled" @update:value="toggle(r, $event)" />

        <NButton size="tiny" quaternary @click="openEdit(r)">
          <template #icon><Pencil :size="13" /></template>
        </NButton>

        <NPopconfirm @positive-click="remove(r)">
          <template #trigger>
            <NButton size="tiny" quaternary type="error">
              <template #icon><Trash2 :size="13" /></template>
            </NButton>
          </template>
          确定删除「{{ r.main }}」？
        </NPopconfirm>
      </div>
    </div>

    <NModal
      v-model:show="editShow"
      preset="card"
      :title="(editId > 0 ? '编辑' : '新增') + (editKind === 'source' ? ' TG 群' : '关键词订阅')"
      style="width: 520px"
    >
      <FieldRow label="类型" required>
        <!-- 编辑态锁类型：改类型等于换实体 -->
        <NRadioGroup v-model:value="editKind" :disabled="editId > 0">
          <NRadioButton value="item">关键词订阅</NRadioButton>
          <NRadioButton value="source">TG 群</NRadioButton>
        </NRadioGroup>
      </FieldRow>

      <template v-if="editKind === 'item'">
        <FieldRow label="关键词" required>
          <NInput v-model:value="editItem.keyword" placeholder="片名" />
        </FieldRow>
        <FieldRow label="指定频道" hint="留空使用全部 TG 群；多个频道每行一个">
          <NInput
            v-model:value="editItem.channels"
            type="textarea"
            :rows="2"
            placeholder="@channel1&#10;@channel2"
          />
        </FieldRow>
        <FieldRow label="自动转存">
          <NCheckbox v-model:checked="editItem.auto">命中后自动转存 / 离线</NCheckbox>
        </FieldRow>
      </template>

      <template v-else>
        <FieldRow label="订阅名称" required>
          <NInput v-model:value="editSource.name" placeholder="请输入订阅名称" />
        </FieldRow>
        <FieldRow label="订阅地址" required>
          <NInput v-model:value="editSource.url" placeholder="频道链接 https://t.me/xxx" />
        </FieldRow>
        <FieldRow label="优先级" hint="越大越优先，默认 10">
          <NInputNumber v-model:value="editSource.priority" :min="0" style="width: 140px" />
        </FieldRow>
        <FieldRow label="备注">
          <NInput v-model:value="editSource.note" />
        </FieldRow>
      </template>

      <template #footer>
        <div class="modal-foot">
          <NButton @click="editShow = false">取消</NButton>
          <NButton type="primary" @click="saveEdit">保存</NButton>
        </div>
      </template>
    </NModal>
  </SectionCard>
</template>

<style scoped>
.tools {
  display: flex;
  align-items: center;
  gap: 8px;
}
.sel {
  width: 128px;
}

.list {
  display: flex;
  flex-direction: column;
}
.row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 11px 0;
  border-bottom: 1px solid var(--c-border);
  transition: opacity 0.15s;
}
.row:last-child {
  border-bottom: none;
}
.row.off {
  opacity: 0.55;
}

.body {
  flex: 1;
  min-width: 0;
}
.name {
  font-size: 13.5px;
  font-weight: 500;
  color: var(--c-text-1);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.meta {
  margin-top: 2px;
  font-size: 11.5px;
  color: var(--c-text-3);
}
.meta-item + .meta-item::before {
  content: '·';
  margin: 0 6px;
  color: var(--c-text-4);
}

.err {
  color: var(--c-danger);
  padding: 20px 0;
  text-align: center;
}
.modal-foot {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
