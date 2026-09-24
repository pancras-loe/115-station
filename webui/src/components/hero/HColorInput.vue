<script setup lang="ts">
/**
 * 颜色选择（替代 NColorPicker）：一排常用色块 + 原生取色器 + 十六进制值。
 * 原生 <input type="color"> 在手机上直接调起系统取色盘，比自己画一个取色面板好用。
 */
withDefaults(defineProps<{ swatches?: string[] }>(), { swatches: () => [] })
const model = defineModel<string>({ default: '#000000' })
</script>

<template>
  <div class="h-color">
    <button
      v-for="c in swatches"
      :key="c"
      type="button"
      class="h-color-swatch"
      :class="{ on: model?.toLowerCase() === c.toLowerCase() }"
      :style="{ background: c }"
      :aria-label="`选择颜色 ${c}`"
      @click="model = c"
    />
    <label class="h-color-custom" title="自定义颜色">
      <span class="h-color-dot" :style="{ background: model }" />
      <input v-model="model" type="color" class="h-color-native" aria-label="自定义颜色" />
    </label>
    <code class="h-color-hex">{{ model }}</code>
  </div>
</template>

<style scoped>
.h-color {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.h-color-swatch,
.h-color-custom {
  position: relative;
  width: 26px;
  height: 26px;
  padding: 0;
  border: 0;
  border-radius: 999px;
  cursor: pointer;
  box-shadow: inset 0 0 0 1px color-mix(in oklab, var(--foreground) 12%, transparent);
  transition: transform 120ms ease;
}
.h-color-swatch:hover {
  transform: scale(1.08);
}
.h-color-swatch.on {
  box-shadow:
    0 0 0 2px var(--surface),
    0 0 0 4px var(--accent);
}
/* 自定义那一颗：彩虹圈里放当前颜色，一眼看出是「点开自己选」 */
.h-color-custom {
  display: grid;
  place-items: center;
  background: conic-gradient(#f43f5e, #f59e0b, #22c55e, #3b82f6, #a855f7, #f43f5e);
  box-shadow: none;
}
.h-color-dot {
  width: 16px;
  height: 16px;
  border-radius: 999px;
  box-shadow: 0 0 0 2px var(--surface);
}
.h-color-native {
  position: absolute;
  inset: 0;
  opacity: 0;
  cursor: pointer;
}
.h-color-hex {
  margin-left: 4px;
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--muted);
  text-transform: uppercase;
}
</style>
