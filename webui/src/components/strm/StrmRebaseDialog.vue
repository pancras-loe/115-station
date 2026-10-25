<script setup lang="ts">
import HButton from '@/components/hero/HButton.vue'
import HModal from '@/components/hero/HModal.vue'
import StrmRebasePanel from '@/components/strm/StrmRebasePanel.vue'
import type { StrmRebaseReport } from '@/api/sync'

/**
 * 「Strm 管理 → 配置」改了直连域名后弹出的替换框。内容与扩展功能「STRM 迁移 → 替换地址」是同一个面板，
 * 这里只是换个入口：最常见的就是改完域名要换旧地址，不该让人再去扩展功能里找
 */
const show = defineModel<boolean>('show', { required: true })
defineProps<{
  /** 保存配置后自动弹出时，父组件已经读过分组，直接用它 */
  preload?: StrmRebaseReport | null
  afterSave?: boolean
}>()
</script>

<template>
  <HModal v-model:show="show" title="替换已有 STRM 的地址" width="720px">
    <!-- 每次打开都重新挂载：上次的结果留着会让人以为还没改 -->
    <StrmRebasePanel v-if="show" :preload="preload" :after-save="afterSave" />
    <template #footer>
      <HButton variant="tertiary" @click="show = false">关闭</HButton>
    </template>
  </HModal>
</template>
