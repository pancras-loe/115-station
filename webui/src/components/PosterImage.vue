<script setup lang="ts">
import { ref } from 'vue'
import { Clapperboard } from '@lucide/vue'

defineProps<{ src?: string | null; alt?: string }>()

// 海报 404 很常见（刮削未完成、Emby 图片代理失败），裂图比没图更难看
const failed = ref(false)
</script>

<template>
  <div class="poster">
    <img v-if="src && !failed" :src="src" :alt="alt ?? ''" loading="lazy" decoding="async" @error="failed = true" />
    <div v-else class="poster-fallback"><Clapperboard :size="20" :stroke-width="1.5" /></div>
  </div>
</template>

<style scoped>
.poster {
  position: relative;
  aspect-ratio: 2 / 3;
  border-radius: var(--r-sm);
  overflow: hidden;
  background: var(--c-bg-hover);
}
.poster img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}
.poster-fallback {
  width: 100%;
  height: 100%;
  display: grid;
  place-items: center;
  color: var(--c-text-4);
}
</style>
