<script setup lang="ts">
defineProps<{ height?: string; width?: string; radius?: string }>()
</script>

<template>
  <div
    class="skeleton skeleton--shimmer"
    :style="{ height: height ?? '1em', width: width ?? '100%', borderRadius: radius }"
    aria-hidden="true"
  />
</template>
