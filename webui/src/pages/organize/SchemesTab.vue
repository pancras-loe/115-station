<script setup lang="ts">
/**
 * 二级分类：可以存多套「分类方案」（V2，MULTI-ACCOUNT-V2-PLAN.md §10.4，后端 catschemeapi.go）。
 * 每套就是一份 YAML，媒体库在库卡片上选用哪一套；被库用着的不能删。
 * 方案只管「怎么分目录」，不管「进哪个库」（规则里的 library 键作废：放进哪个库的转存文件夹就进哪个库）。
 *
 * 每套方案有简单模式（CategorySimple，2026-10-23）与专家模式（YAML）两种改法，存的是同一份 YAML；
 * 后端认不回简单模式的（GET 返回 simple = null）只能在专家模式里改。
 */
import { computed, onMounted, ref, watch } from 'vue'
import { Copy, Pencil, Plus, Trash2 } from '@lucide/vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import HModal from '@/components/hero/HModal.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import { libraryApi } from '@/api'
import type { SchemeDetail, SchemeView } from '@/api/library'
import { toastError, useFeedback } from '@/composables/useFeedback'
import CategorySimple from './CategorySimple.vue'
import CategoryTry from './CategoryTry.vue'
import YamlRuleTab from './YamlRuleTab.vue'
import { DEFAULT_CATEGORY_YAML } from './defaultRules'

const { message, dialog } = useFeedback()
const schemes = ref<SchemeView[]>([])
const current = ref(0)
const loading = ref(true)

async function load() {
  try {
    const v = await libraryApi.getMediaLibs()
    schemes.value = v.schemes
    if (!schemes.value.some((s) => s.id === current.value)) current.value = schemes.value[0]?.id ?? 0
  } catch (e) {
    toastError(e, '读取分类方案失败')
  } finally {
    loading.value = false
  }
}
onMounted(async () => {
  await load()
  await loadDetail()
})

const cur = computed(() => schemes.value.find((s) => s.id === current.value))

// ---- 简单模式 / 专家模式 ----
const detail = ref<SchemeDetail | null>(null)
const view = ref<'simple' | 'expert'>('simple')
const simpleDirty = ref(false)
const yamlDraft = ref('')
/** 简单模式能不能用：认得回来，或者还一条规则都没有 */
const simpleOK = computed(() => !!detail.value && (!!detail.value.simple || detail.value.empty))

async function loadDetail(keepView = false) {
  const id = current.value
  if (!id) {
    detail.value = null
    return
  }
  try {
    const d = await libraryApi.getScheme(id)
    if (id !== current.value) return
    detail.value = d
    if (!keepView || !simpleOK.value) view.value = simpleOK.value ? 'simple' : 'expert'
  } catch (e) {
    toastError(e, '读取分类方案失败')
  }
}
watch(current, () => {
  simpleDirty.value = false
  void loadDetail()
})

async function confirmDropSimple() {
  if (view.value !== 'simple' || !simpleDirty.value) return true
  return dialog.confirm({
    title: '简单模式还没保存',
    content: '这里改的会丢掉。',
    actions: [
      { label: '留在这里', value: false, variant: 'tertiary' },
      { label: '丢掉', value: true, variant: 'primary' },
    ],
  })
}
async function pickScheme(id: number) {
  if (id === current.value || !(await confirmDropSimple())) return
  current.value = id
}
async function switchView(v: 'simple' | 'expert') {
  if (v === view.value || !(await confirmDropSimple())) return
  simpleDirty.value = false
  view.value = v
  await loadDetail(true) // 两边看的都是已保存的那份
}

const loadYaml = async () => {
  const d = await libraryApi.getScheme(current.value)
  return { config: d.yaml }
}
const persistYaml = async (yaml: string, opts?: { confirmCatChange?: boolean }) => {
  const d = await libraryApi.updateScheme(current.value, { yaml, confirm_cat_change: opts?.confirmCatChange || undefined })
  schemes.value = d.schemes
  await loadDetail(true) // 改成了简单模式认得回来的写法，切回去就能接着用表单
}
async function onSimpleSaved() {
  await load()
  await loadDetail(true)
}

// ---- 新建 / 复制 / 改名 ----
const nameShow = ref(false)
const nameMode = ref<'new' | 'copy' | 'rename'>('new')
const nameText = ref('')
const nameBusy = ref(false)
function openName(mode: 'new' | 'copy' | 'rename') {
  nameMode.value = mode
  nameText.value = mode === 'rename' ? cur.value?.name || '' : mode === 'copy' ? `${cur.value?.name || ''} 副本` : ''
  nameShow.value = true
}
const nameTitle = computed(() => ({ new: '新建分类方案', copy: `复制「${cur.value?.name}」`, rename: `改名「${cur.value?.name}」` })[nameMode.value])
async function submitName() {
  nameBusy.value = true
  try {
    if (nameMode.value === 'rename') {
      schemes.value = (await libraryApi.updateScheme(current.value, { name: nameText.value })).schemes
    } else {
      // 新建 = 复制第一套（「默认」）当起点；复制 = 复制当前这套
      const d = await libraryApi.createScheme(nameText.value, nameMode.value === 'copy' ? current.value : 0)
      schemes.value = d.schemes
      current.value = d.id
    }
    nameShow.value = false
    message.success('已保存')
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    nameBusy.value = false
  }
}

async function remove() {
  const s = cur.value
  if (!s) return
  if (s.used_by.length) {
    message.error(`「${s.name}」还有媒体库在用（${s.used_by.join('、')}），先在库卡片上换一套再删`)
    return
  }
  const ok = await dialog.confirm({
    title: `删除分类方案「${s.name}」`,
    content: '删了就没了（没有库在用它）。',
    actions: [
      { label: '取消', value: false, variant: 'tertiary' },
      { label: '删除', value: true, variant: 'danger' },
    ],
  })
  if (!ok) return
  try {
    schemes.value = (await libraryApi.deleteScheme(s.id)).schemes
    current.value = schemes.value[0]?.id ?? 0
    message.success('已删除')
  } catch (e) {
    toastError(e, '删除失败')
  }
}
</script>

<template>
  <div class="schemes">
    <div class="bar">
      <div class="chips" role="tablist" aria-label="分类方案">
        <button
          v-for="s in schemes"
          :key="s.id"
          class="chip-btn"
          :class="{ on: s.id === current }"
          role="tab"
          :aria-selected="s.id === current"
          @click="pickScheme(s.id)"
        >
          {{ s.name }}
          <span class="use">{{ s.used_by.length ? `${s.used_by.length} 个库在用` : '没有库在用' }}</span>
        </button>
      </div>
      <div class="ops">
        <HButton size="sm" variant="secondary" @click="openName('new')"><Plus :size="14" />新建</HButton>
        <HButton size="sm" variant="ghost" :disabled="!cur" @click="openName('copy')"><Copy :size="14" />复制</HButton>
        <HButton size="sm" variant="ghost" :disabled="!cur" @click="openName('rename')"><Pencil :size="14" />改名</HButton>
        <HButton size="sm" variant="ghost" :disabled="!cur || schemes.length <= 1" @click="remove"><Trash2 :size="14" />删除</HButton>
      </div>
    </div>
    <p v-if="cur" class="muted small">
      <template v-if="cur.used_by.length">用着它的库：{{ cur.used_by.join('、') }}。</template>
      <template v-else>还没有库用它：在「账号与媒体库 → 媒体库」的库卡片上选。</template>
      规则只管分目录；进哪个库看内容放进了哪个库的转存/待整理文件夹。
    </p>

    <template v-if="cur && detail">
      <div class="mode">
        <HSegmented
          :model-value="view"
          :options="[
            { label: '简单模式', value: 'simple', disabled: !simpleOK },
            { label: '专家模式（YAML）', value: 'expert' },
          ]"
          aria-label="二级分类的编辑方式"
          @update:model-value="(v) => switchView(v as 'simple' | 'expert')"
        />
        <span class="muted small">两边是同一份规则；分类名就是 115 上的目录名，整理时没有会自动建。</span>
      </div>

      <CategorySimple
        v-if="view === 'simple'"
        :key="`simple-${cur.id}`"
        :scheme-id="cur.id"
        :detail="detail"
        @saved="onSimpleSaved"
        @dirty="simpleDirty = $event"
      />
      <template v-else>
        <HAlert v-if="!simpleOK" status="warning" title="这套方案只能在专家模式里改">
          里面有简单模式认不回来的写法，切过去会改掉它们：
          <ul class="why">
            <li v-for="w in detail.simple_why" :key="w">{{ w }}</li>
          </ul>
          把这几处改成支持的写法并保存后，简单模式会自动可用。
        </HAlert>
        <div class="expert">
          <YamlRuleTab
            :key="`scheme-${cur.id}`"
            kind="category"
            :title="`分类方案「${cur.name}」`"
            hint="分类名即 115 目录名"
            note="分类名即 115 目录名，整理时目录不存在会自动创建。规则从上往下匹配，先命中的先停；不想手写 YAML 就用「简单模式」。"
            :load="loadYaml"
            :persist="persistYaml"
            :fallback="DEFAULT_CATEGORY_YAML"
            @change="yamlDraft = $event"
          />
          <CategoryTry :yaml="yamlDraft" />
        </div>
      </template>
    </template>
    <p v-else-if="!loading && !cur" class="muted">还没有分类方案，点「新建」建一套。</p>

    <HModal v-model:show="nameShow" :title="nameTitle" width="420px">
      <HInput v-model="nameText" placeholder="如 电影分类" :input-attrs="{ 'aria-label': '方案名' }" @enter="submitName" />
      <template #footer>
        <HButton variant="tertiary" @click="nameShow = false">取消</HButton>
        <HButton variant="primary" :loading="nameBusy" @click="submitName">保存</HButton>
      </template>
    </HModal>
  </div>
</template>

<style scoped>
.schemes {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.chip-btn {
  display: inline-flex;
  align-items: baseline;
  gap: 6px;
  padding: 6px 12px;
  border: 0;
  border-radius: 999px;
  background: var(--default);
  color: var(--foreground);
  font-size: 13px;
  cursor: pointer;
}
.chip-btn.on {
  background: var(--accent);
  color: var(--accent-foreground);
}
.chip-btn .use {
  font-size: 11.5px;
  opacity: 0.75;
}
.ops {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}
.muted {
  color: var(--muted);
}
.small {
  margin: 0;
  font-size: 12.5px;
}
.mode {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 14px;
}
.why {
  margin: 6px 0;
  padding-left: 20px;
}
.expert {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(300px, 380px);
  gap: 16px;
  align-items: start;
}
@media (max-width: 1100px) {
  .expert {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
