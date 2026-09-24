<script setup lang="ts">
import HHelpTip from '@/components/hero/HHelpTip.vue'

defineProps<{
  label?: string
  /** 问号气泡里的解释文案；旧版 .help-tip 的等价物。触屏上点问号也能看（见 HHelpTip） */
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
      <HHelpTip v-if="tip" :text="tip" />
    </div>
    <div class="row-control field-row-control" :class="{ wide }">
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
  padding: 8px 0;
}

/* 标签左对齐固定列。旧版是右对齐——那是早期 Ant/Arco 的做法，
   在长短标签混排时视觉锯齿明显，这里改成左对齐 */
.row-label {
  display: flex;
  align-items: center;
  gap: 5px;
  width: 168px;
  flex-shrink: 0;
  min-height: 36px;
  font-size: 13.5px;
  font-weight: 500;
  color: color-mix(in oklab, var(--foreground) 80%, var(--muted));
  line-height: 1.4;
}
.req {
  color: var(--danger);
  font-weight: 600;
}

.row-control {
  flex: 1;
  min-width: 0;
  max-width: 480px;
}
/* 控件高 36px、标签列按 36px 垂直居中；纯文字的值（状态页）没有控件高度，
   补上半行的上边距才和标签对得齐。不用 flex 居中：那会把还没迁移的 Naive 开关、单选组拉满整行 */
.row-control > :slotted(:is(span, strong):first-child) {
  display: inline-block;
  padding-top: 8px;
}
.row-control.wide {
  max-width: none;
}
.row-hint {
  margin-top: 6px;
  font-size: 12px;
  line-height: 1.6;
  color: var(--muted);
}
.row-error {
  margin-top: 6px;
  font-size: 12px;
  line-height: 1.6;
  color: var(--danger);
}

/* 手机：标签在上、控件在下，控件占满整行 */
@media (max-width: 720px) {
  .row {
    flex-direction: column;
    gap: 6px;
    padding: 8px 0;
  }
  .row-label {
    width: auto;
    min-height: 0;
  }
  .row-control {
    max-width: none;
    width: 100%;
  }
  .row-control > :slotted(:is(span, strong):first-child) {
    padding-top: 0;
  }
}
</style>

<!-- 开关只有 20px 高：撑到控件行高再垂直居中，和左边标签对齐。
     HSwitch 的根元素是子组件的根，scoped 的 :slotted() 选不中它，只能用全局规则 + 独有类名 -->
<style>
.field-row-control > .switch:first-child {
  min-height: 36px;
  justify-content: center;
}
@media (max-width: 720px) {
  .field-row-control > .switch:first-child {
    min-height: 0;
  }
}
</style>
