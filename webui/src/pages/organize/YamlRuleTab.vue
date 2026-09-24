<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HModal from '@/components/hero/HModal.vue'
import HPopconfirm from '@/components/hero/HPopconfirm.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FormActions from '@/components/ui/FormActions.vue'
import YamlEditor from '@/components/ui/YamlEditor.vue'
import { toastError, useFeedback } from '@/composables/useFeedback'
import CategoryHelp from './CategoryHelp.vue'
import WashHelp from './WashHelp.vue'
import RuleBuilder from './RuleBuilder.vue'
import RuleOutline from './RuleOutline.vue'
import { lintCategory, lintWash, outlineCategory, outlineWash } from './ruleLint'

/** 二级分类与洗版两个页签结构完全一致，只有接口、文案和体检口径不同 */
const props = defineProps<{
  kind: 'category' | 'wash'
  title: string
  hint?: string
  note: string
  load: () => Promise<{ config?: string; default_config?: string }>
  persist: (yaml: string) => Promise<unknown>
  /** 库里没有配置时填入编辑器的起始模板。洗版不传：后端首次部署就播种了真实
      配置，这里再兜底显示一份默认策略只会让「清空=不洗版」看着像还在生效 */
  fallback?: string
}>()

const { message } = useFeedback()

const yaml = ref('')
const defaultTemplate = ref(props.fallback || '')
const loading = ref(true)
const saving = ref(false)
const editor = ref<InstanceType<typeof YamlEditor>>()
const helpVisible = ref(false)
const builderVisible = ref(false)

async function read() {
  loading.value = true
  try {
    const d = await props.load()
    defaultTemplate.value = d.default_config || props.fallback || ''
    yaml.value = d.config || props.fallback || ''
  } catch (e) {
    yaml.value = props.fallback || ''
    toastError(e, '规则读取失败')
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  try {
    // 后端会解析 YAML，格式错误时返回带行号的错误信息，直接透传给用户
    await props.persist(yaml.value)
    message.success('保存成功')
  } catch (e) {
    toastError(e, '保存失败：请检查 YAML 缩进与格式')
  } finally {
    saving.value = false
  }
}

// 体检与大纲都是纯文本推导，跟着输入实时更新；判定权仍在后端，这里只提示不拦截
const issues = computed(() => (props.kind === 'category' ? lintCategory(yaml.value) : lintWash(yaml.value)))
const errors = computed(() => issues.value.filter(i => i.level === 'error').length)
const warns = computed(() => issues.value.filter(i => i.level === 'warn').length)
const outline = computed(() => (props.kind === 'category' ? outlineCategory(yaml.value) : outlineWash(yaml.value)))

const LEVEL_TEXT = { error: '错误', warn: '不生效', info: '提示' }

function jump(line: number) {
  if (line > 0) editor.value?.focusLine(line)
}

onMounted(read)
</script>

<template>
  <SectionCard :title="title" :hint="hint">
    <template #extra>
      <div class="head-actions">
        <HButton variant="secondary" size="sm" @click="builderVisible = true">
          {{ kind === 'category' ? '添加分类规则' : '添加洗版策略' }}
        </HButton>
        <HButton variant="ghost" size="sm" @click="helpVisible = true">规则说明</HButton>
      </div>
    </template>

    <HAlert status="accent" class="note">{{ note }}</HAlert>
    <HAlert status="warning" v-if="kind === 'wash' && !loading && !yaml.trim()" class="note">
      策略为空时不做版本比较或替换。编辑后请保存，保存后立即生效；重启不会恢复默认策略。
    </HAlert>

    <YamlEditor v-if="!loading" ref="editor" v-model="yaml" :rows="26" :markers="issues" />
    <div v-else class="skeleton" />

    <div v-if="!loading" class="assist">
      <section class="panel">
        <header class="panel-head">
          规则体检
          <span v-if="errors" class="count error">{{ errors }} 处错误</span>
          <span v-if="warns" class="count warn">{{ warns }} 处不生效</span>
          <span v-if="!errors && !warns" class="count ok">没发现问题</span>
        </header>
        <ul v-if="issues.length" class="issues">
          <li
            v-for="(it, i) in issues"
            :key="i"
            class="issue"
            :class="it.level"
            @click="jump(it.line)"
          >
            <span class="badge">{{ LEVEL_TEXT[it.level] }}</span>
            <span v-if="it.line" class="line">第 {{ it.line }} 行</span>
            <span class="text">{{ it.text }}</span>
          </li>
        </ul>
        <p v-else class="empty">语法、字段名和顺序都没问题。最终以保存时后端的解析结果为准。</p>
      </section>

      <section class="panel">
        <header class="panel-head">规则大纲<span class="count">按实际匹配顺序</span></header>
        <RuleOutline
          :groups="outline"
          :empty-text="kind === 'category' ? '还没解析出 movie / tv 分类规则' : '还没有洗版策略'"
          @jump="jump"
        />
      </section>
    </div>

    <FormActions>
      <HButton variant="primary" :loading="saving" @click="save">保存配置</HButton>
      <HButton variant="tertiary" @click="read">放弃修改</HButton>
      <HPopconfirm v-if="defaultTemplate" @confirm="yaml = defaultTemplate">
<HButton variant="ghost">恢复默认模板</HButton>
<template #content>当前编辑器里的内容会被默认模板覆盖（不会立刻保存）。</template>
</HPopconfirm>
    </FormActions>
  </SectionCard>

  <HModal v-model:show="helpVisible" :title="kind === 'category' ? '分类规则说明' : '洗版规则说明'" width="920px">
    <CategoryHelp v-if="kind === 'category'" />
    <WashHelp v-else />
  </HModal>

  <RuleBuilder v-model:show="builderVisible" :kind="kind" :yaml="yaml" @apply="yaml = $event" />
</template>

<style scoped>
.head-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}
.note {
  margin-bottom: 12px;
}
.skeleton {
  height: 460px;
  border-radius: var(--radius);
  background: var(--c-bg-raised);
  border: 1px solid var(--c-border);
}

.assist {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
  gap: 14px;
  margin-top: 14px;
}
.panel {
  padding: 12px 14px;
  border: 1px solid var(--c-border);
  border-radius: var(--radius);
  background: var(--c-bg-raised);
}
.panel-head {
  display: flex;
  align-items: baseline;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 10px;
  font-size: 12.5px;
  font-weight: 600;
  color: var(--c-text-2);
}
.count {
  font-size: 11px;
  font-weight: 400;
  color: var(--c-text-4);
}
.count.error {
  color: var(--c-danger);
}
.count.warn {
  color: var(--c-warning);
}
.count.ok {
  color: var(--c-success);
}

.issues {
  list-style: none;
  margin: 0;
  padding: 0;
  max-height: 260px;
  overflow-y: auto;
}
.issue {
  display: flex;
  align-items: baseline;
  gap: 7px;
  flex-wrap: wrap;
  padding: 5px 8px;
  border-radius: var(--r-sm);
  font-size: 12px;
  line-height: 1.6;
  color: var(--c-text-2);
  cursor: pointer;
}
.issue:hover {
  background: var(--c-primary-soft);
}
.badge {
  flex-shrink: 0;
  padding: 1px 6px;
  border-radius: 999px;
  font-size: 10.5px;
  background: var(--c-bg-hover);
  color: var(--c-text-3);
}
.issue.error .badge {
  background: color-mix(in srgb, var(--c-danger) 18%, transparent);
  color: var(--c-danger);
}
.issue.warn .badge {
  background: color-mix(in srgb, var(--c-warning) 18%, transparent);
  color: var(--c-warning);
}
.line {
  flex-shrink: 0;
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--c-text-4);
}
.text {
  flex: 1;
  min-width: 0;
}
.empty {
  margin: 0;
  font-size: 12px;
  color: var(--c-text-4);
}
</style>
