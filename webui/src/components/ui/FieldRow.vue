<script setup lang="ts">
import { NTooltip } from 'naive-ui'
import { CircleHelp } from '@lucide/vue'

defineProps<{
  label?: string
  /** 问号气泡里的解释文案；旧版 .help-tip 的等价物 */
  tip?: string
  /** 控件下方的常驻说明，和 tip 的区别是不需要 hover */
  hint?: string
  /** 校验失败的说明，红字显示在控件下方；有它时盖掉 hint（两行叠着看着乱） */
  error?: string
  required?: boolean
  /** 控件不限宽（长 URL、回调地址等） */
  wide?: boolean
}>()
</script>

<template>
  <div class="row">
    <div class="row-label">
      <span v-if="required" class="req">*</span>
      <span>{{ label }}</span>
      <NTooltip v-if="tip" :style="{ maxWidth: '280px' }">
        <template #trigger><CircleHelp class="tip-icon" :size="14" /></template>
        {{ tip }}
      </NTooltip>
    </div>
    <div class="row-control" :class="{ wide }">
      <slot />
      <div v-if="error" class="row-error">{{ error }}</div>
      <div v-else-if="hint" class="row-hint">{{ hint }}</div>
    </div>
  </div>
</template>

<style scoped>
.row {
  display: flex;
  align-items: flex-start;
  gap: 16px;
  padding: 9px 0;
}

/* 标签左对齐固定列。旧版是右对齐——那是早期 Ant/Arco 的做法，
   在长短标签混排时视觉锯齿明显，这里改成左对齐 */
.row-label {
  display: flex;
  align-items: center;
  gap: 5px;
  width: 168px;
  flex-shrink: 0;
  padding-top: 8px;
  font-size: 13.5px;
  color: var(--c-text-2);
  line-height: 1.4;
}
.req {
  color: var(--c-danger);
  font-weight: 600;
}
.tip-icon {
  color: var(--c-text-4);
  cursor: help;
  flex-shrink: 0;
  transition: color 0.15s;
}
.tip-icon:hover {
  color: var(--c-primary);
}

.row-control {
  flex: 1;
  min-width: 0;
  max-width: 480px;
}
.row-control.wide {
  max-width: none;
}
.row-hint {
  margin-top: 5px;
  font-size: 11.5px;
  line-height: 1.6;
  color: var(--c-text-3);
}
.row-error {
  margin-top: 5px;
  font-size: 11.5px;
  line-height: 1.6;
  color: var(--c-danger);
}

@media (max-width: 720px) {
  .row {
    flex-direction: column;
    gap: 6px;
    padding: 8px 0;
  }
  .row-label {
    width: auto;
    padding-top: 0;
  }
  .row-control {
    max-width: none;
    width: 100%;
  }
}
</style>
