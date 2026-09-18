<script setup lang="ts">
import { computed } from 'vue'
import { NButton, NInput, NTag } from 'naive-ui'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import { useSetting } from '@/composables/useSetting'
import { RENAME_PRESETS, matchPreset, renderRenameExample } from '@/utils/rename'

const { model, saving, save, reset } = useSetting('org-rename', {
  movie_folder: RENAME_PRESETS.movie.default.folder,
  movie_file: RENAME_PRESETS.movie.default.file,
  tv_folder: RENAME_PRESETS.tv.default.folder,
  tv_file: RENAME_PRESETS.tv.default.file,
})

const GROUPS = [
  { type: 'movie' as const, label: '电影', tagType: 'info' as const },
  { type: 'tv' as const, label: '剧集', tagType: 'success' as const },
]

const PRESET_LABELS: Record<string, string> = { default: '默认', lite: '精简', full: '详细' }

/** 手改过模板后与任何预设都不一致 → 全部按钮弹起（自定义状态），不自动套用 */
const activePreset = computed(() => ({
  movie: matchPreset('movie', model.value.movie_folder, model.value.movie_file),
  tv: matchPreset('tv', model.value.tv_folder, model.value.tv_file),
}))

function applyPreset(type: 'movie' | 'tv', key: string) {
  const p = RENAME_PRESETS[type][key]
  if (!p) return
  model.value[`${type}_folder`] = p.folder
  model.value[`${type}_file`] = p.file
}
</script>

<template>
  <SectionCard title="重命名策略" hint="入库后的目录与文件命名模板">
    <div v-for="g in GROUPS" :key="g.type" class="group">
      <div class="group-head">
        <NTag size="small" :bordered="false" :type="g.tagType">{{ g.label }}</NTag>
        <span class="preset-label">命名规范</span>
        <button
          v-for="(label, key) in PRESET_LABELS"
          :key="key"
          class="preset"
          :class="{ on: activePreset[g.type] === key }"
          @click="applyPreset(g.type, String(key))"
        >
          {{ label }}
        </button>
        <span v-if="!activePreset[g.type]" class="custom">自定义</span>
      </div>

      <FieldRow
        label="文件夹命名规则"
        wide
        tip="入库后的文件夹名模板，支持 {first_letter}/{title}/{year}/{tmdb_id} 等变量，详见「重命名规则」页。"
      >
        <NInput v-model:value="model[`${g.type}_folder`]" type="textarea" :autosize="{ minRows: 1 }" />
        <div class="example">
          示例：<code>{{ renderRenameExample(model[`${g.type}_folder`]) }}</code>
        </div>
      </FieldRow>

      <FieldRow
        label="文件命名规则"
        wide
        tip="入库后的文件名模板，<xxx> 块内变量为空时整块省略，详见「重命名规则」页。"
      >
        <NInput v-model:value="model[`${g.type}_file`]" type="textarea" :autosize="{ minRows: 1 }" />
        <div class="example">
          示例：<code>{{ renderRenameExample(model[`${g.type}_file`]) }}</code>
        </div>
      </FieldRow>
    </div>

    <FormActions>
      <NButton type="primary" :loading="saving" @click="save()">保存配置</NButton>
      <NButton @click="reset">重置配置</NButton>
    </FormActions>
  </SectionCard>
</template>

<style scoped>
.group {
  padding: 14px 16px;
  margin-bottom: 14px;
  border: 1px solid var(--c-border);
  border-radius: var(--radius);
}
.group-head {
  display: flex;
  align-items: center;
  gap: 7px;
  margin-bottom: 6px;
  flex-wrap: wrap;
}
.preset-label {
  margin-left: 4px;
  font-size: 12px;
  color: var(--c-text-3);
}
.preset {
  all: unset;
  padding: 2px 11px;
  border-radius: 999px;
  cursor: pointer;
  font-size: 12px;
  line-height: 20px;
  background: var(--c-bg-hover);
  color: var(--c-text-2);
  transition: background-color 0.15s, color 0.15s;
}
.preset.on {
  background: var(--c-primary);
  color: #fff;
}
.custom {
  font-size: 11.5px;
  color: var(--c-text-4);
}

.example {
  margin-top: 6px;
  padding: 6px 10px;
  width: fit-content;
  max-width: 100%;
  border-radius: var(--radius-sm);
  background: var(--c-bg-raised);
  font-size: 11.5px;
  color: var(--c-text-3);
}
.example code {
  font-family: var(--font-mono);
  color: var(--c-primary);
  word-break: break-all;
}
</style>
