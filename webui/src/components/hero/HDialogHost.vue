<script setup lang="ts">
import { computed } from 'vue'
import { CircleAlert, Info, TriangleAlert } from '@lucide/vue'
import HButton from './HButton.vue'
import HModal from './HModal.vue'
import { currentDialog } from '@/composables/useFeedback'

/** useFeedback().dialog.confirm() 的渲染层，挂在 App.vue 里一次 */
const open = computed({
  get: () => currentDialog.value !== null,
  set: (v) => {
    if (!v) currentDialog.value?.resolve(null)
  },
})
const opts = computed(() => currentDialog.value?.opts)
const icon = computed(() =>
  opts.value?.tone === 'danger' ? CircleAlert : opts.value?.tone === 'warning' ? TriangleAlert : Info,
)
</script>

<template>
  <HModal v-model:show="open" :title="opts?.title" width="440px">
    <div class="h-dlg">
      <span class="h-dlg-icon" :class="`tone-${opts?.tone ?? 'accent'}`">
        <component :is="icon" :size="18" :stroke-width="2" />
      </span>
      <p class="h-dlg-text">{{ opts?.content }}</p>
    </div>
    <template #footer>
      <HButton
        v-for="a in opts?.actions ?? []"
        :key="a.label"
        :variant="a.variant ?? 'tertiary'"
        @click="currentDialog?.resolve(a.value)"
      >
        {{ a.label }}
      </HButton>
    </template>
  </HModal>
</template>

<style scoped>
.h-dlg {
  display: flex;
  gap: 12px;
  align-items: flex-start;
}
.h-dlg-icon {
  flex: none;
  display: grid;
  place-items: center;
  width: 36px;
  height: 36px;
  border-radius: 999px;
}
.tone-accent,
.tone-success {
  background: var(--accent-soft);
  color: var(--accent-soft-foreground);
}
.tone-warning {
  background: var(--warning-soft);
  color: var(--warning-soft-foreground);
}
.tone-danger {
  background: var(--danger-soft);
  color: var(--danger-soft-foreground);
}
.h-dlg-text {
  margin: 7px 0 0;
  font-size: 13.5px;
  line-height: 1.7;
  color: color-mix(in oklab, var(--foreground) 80%, var(--muted));
}
</style>
