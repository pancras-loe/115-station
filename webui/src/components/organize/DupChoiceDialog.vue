<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HChip from '@/components/hero/HChip.vue'
import HModal from '@/components/hero/HModal.vue'
import type { DupAction, DupGroup, OrganizeRecord } from '@/api/organize'
import { bytes } from '@/utils/format'

/**
 * 同集多份：同一次整理里几份不同的文件改出同一个名字（同一集的粤语 / 英语两份），
 * 后端停下来等用户选（orgdup.go）。每组单选：留某一份 / 都留（加 #A #B）/ 都不要。
 * 不默认替用户选：洗版规则多半管不到语言，留哪份只有用户知道。
 * 洗版策略分得出高下时只标「推荐」，仍要用户点。
 */
const props = defineProps<{ show: boolean; record: OrganizeRecord | null }>()
const emit = defineEmits<{
  'update:show': [boolean]
  submit: [DupAction[]]
}>()

const groups = computed<DupGroup[]>(() => props.record?.dup_list ?? [])
/** 每组的选择：'keep:<fid>' / 'keep_all' / 'drop_all'，没选为空 */
const picks = ref<string[]>([])

/** 已提交过的选择（排队中再打开）还原回来 */
function restore(): string[] {
  const done = props.record?.dup_picked ?? {}
  return groups.value.map((g) => {
    const vals = g.files.map((f) => done[f.fid])
    if (vals.some((v) => !v)) return ''
    const kept = g.files.filter((f) => done[f.fid] !== 'drop')
    if (kept.length === 0) return 'drop_all'
    if (kept.length === 1) return `keep:${kept[0].fid}`
    return 'keep_all'
  })
}
watch(
  () => props.show,
  (v) => {
    if (v) picks.value = restore()
  },
)

const allPicked = computed(() => picks.value.length === groups.value.length && picks.value.every(Boolean))

function setPick(i: number, v: string) {
  const next = [...picks.value]
  next[i] = v
  picks.value = next
}

/** 文件在按钮上的叫法：区别词，认不出时退回「第 N 份」 */
function fileLabel(g: DupGroup, i: number) {
  return g.files[i].label || `第 ${i + 1} 份`
}

/** 都留时的字母顺序（与后端 variantOrder 一致）：推荐的那份最前，其余体积从大到小 */
function variantLetters(g: DupGroup): Record<string, string> {
  const order = [...g.files].sort((a, b) => {
    const ra = a.fid === g.recommend ? 0 : 1
    const rb = b.fid === g.recommend ? 0 : 1
    if (ra !== rb) return ra - rb
    if ((a.size ?? 0) !== (b.size ?? 0)) return (b.size ?? 0) - (a.size ?? 0)
    return a.name.localeCompare(b.name)
  })
  return Object.fromEntries(order.map((f, i) => [f.fid, String.fromCharCode(65 + i)]))
}

function withVariant(name: string, v: string) {
  const dot = name.lastIndexOf('.')
  return dot > 0 ? `${name.slice(0, dot)}#${v}${name.slice(dot)}` : `${name}#${v}`
}
function targetName(g: DupGroup) {
  return g.target.split('/').pop() || g.target
}

/**
 * 批量：每组的区别词都是同一套（整季双语）时，给「每组都留『英语』」这种一键选项。
 * 只收每组都恰好有一份带这个词的
 */
const bulkLabels = computed(() => {
  if (groups.value.length < 2) return []
  const first = groups.value[0].files.map((f) => f.label).filter(Boolean) as string[]
  return first.filter((l) => groups.value.every((g) => g.files.filter((f) => f.label === l).length === 1))
})
function pickAllLabel(label: string) {
  picks.value = groups.value.map((g) => `keep:${g.files.find((f) => f.label === label)!.fid}`)
}
function pickAll(v: 'keep_all' | 'drop_all') {
  picks.value = groups.value.map(() => v)
}

function submit() {
  if (!allPicked.value) return
  const actions: DupAction[] = picks.value.map((p) =>
    p.startsWith('keep:') ? { action: 'keep', fid: p.slice(5) } : { action: p as 'keep_all' | 'drop_all' },
  )
  emit('submit', actions)
}
</script>

<template>
  <HModal :show="show" title="同一集有几份文件" width="680px" @update:show="emit('update:show', $event)">
    <div class="body">
      <p class="intro">
        这几份是不同的文件，按命名规则会改成同一个名字。选择保留哪份：不要的移到「冗余」；都保留时名字后面加
        <code>#A</code> <code>#B</code> 区分。
      </p>

      <div v-if="groups.length > 1" class="bulk">
        <span class="bulk-label">全部 {{ groups.length }} 组：</span>
        <HButton v-for="l in bulkLabels" :key="l" size="sm" variant="secondary" @click="pickAllLabel(l)">
          都留「{{ l }}」
        </HButton>
        <HButton size="sm" variant="tertiary" @click="pickAll('keep_all')">都保留</HButton>
        <HButton size="sm" variant="tertiary" @click="pickAll('drop_all')">都不要</HButton>
      </div>

      <section v-for="(g, gi) in groups" :key="g.target" class="group">
        <header class="group-head">
          <b>{{ g.episode || '同一部' }}</b>
          <span class="dim" :title="g.target">改名后都叫 {{ targetName(g) }}</span>
        </header>

        <div class="opts" role="radiogroup" :aria-label="`${g.episode || targetName(g)} 的选择`">
          <button
            v-for="(f, fi) in g.files"
            :key="f.fid"
            type="button"
            role="radio"
            class="opt"
            :class="{ picked: picks[gi] === `keep:${f.fid}` }"
            :aria-checked="picks[gi] === `keep:${f.fid}`"
            @click="setPick(gi, `keep:${f.fid}`)"
          >
            <span class="opt-head">
              <b>只留「{{ fileLabel(g, fi) }}」</b>
              <span v-if="f.size" class="dim">{{ bytes(f.size) }}</span>
              <HChip v-if="g.recommend === f.fid" color="success" :title="g.reason">推荐</HChip>
            </span>
            <span class="opt-name" :title="f.name">{{ f.name }}</span>
          </button>

          <button
            type="button"
            role="radio"
            class="opt"
            :class="{ picked: picks[gi] === 'keep_all' }"
            :aria-checked="picks[gi] === 'keep_all'"
            @click="setPick(gi, 'keep_all')"
          >
            <span class="opt-head"><b>都保留</b></span>
            <span v-for="(f, fi) in g.files" :key="f.fid" class="opt-name">
              {{ fileLabel(g, fi) }} → {{ withVariant(targetName(g), variantLetters(g)[f.fid]) }}
            </span>
          </button>

          <button
            type="button"
            role="radio"
            class="opt"
            :class="{ picked: picks[gi] === 'drop_all' }"
            :aria-checked="picks[gi] === 'drop_all'"
            @click="setPick(gi, 'drop_all')"
          >
            <span class="opt-head"><b>都不要</b></span>
            <span class="opt-name">全部移到「冗余」</span>
          </button>
        </div>
      </section>
    </div>

    <template #footer>
      <span class="foot-note">提交后加入任务队列，按选择入库。</span>
      <div class="foot-btns">
        <HButton variant="tertiary" @click="emit('update:show', false)">取消</HButton>
        <HButton variant="primary" :disabled="!allPicked" @click="submit">
          {{ allPicked ? '加入队列' : `还有 ${picks.filter((p) => !p).length} 组没选` }}
        </HButton>
      </div>
    </template>
  </HModal>
</template>

<style scoped>
.body {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.intro {
  margin: 0;
  font-size: 12.5px;
  color: var(--muted);
}
.intro code {
  color: var(--foreground);
}
.bulk {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.bulk-label {
  font-size: 12.5px;
  color: var(--muted);
}
.group {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.group-head {
  display: flex;
  align-items: baseline;
  gap: 8px;
  min-width: 0;
}
.group-head .dim {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.dim {
  font-size: 12px;
  color: var(--muted);
}
.opts {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.opt {
  display: flex;
  flex-direction: column;
  gap: 3px;
  width: 100%;
  box-sizing: border-box;
  padding: 10px 12px;
  border: 0;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  color: var(--foreground);
  text-align: left;
  cursor: pointer;
  transition:
    background-color 150ms ease,
    box-shadow 150ms ease;
}
@media (hover: hover) {
  .opt:hover {
    background: color-mix(in oklab, var(--surface-secondary) 70%, var(--surface-tertiary));
  }
}
.opt:focus-visible {
  outline: none;
  box-shadow: 0 0 0 2px var(--focus);
}
.opt.picked {
  background: var(--accent-soft);
  box-shadow: inset 0 0 0 2px var(--accent);
}
.opt-head {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
}
.opt-name {
  font-size: 12px;
  color: var(--muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.foot-note {
  font-size: 12px;
  color: var(--muted);
}
.foot-btns {
  display: flex;
  gap: 8px;
  margin-left: auto;
}
</style>
