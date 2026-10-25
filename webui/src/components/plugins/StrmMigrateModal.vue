<script setup lang="ts">
import HButton from '@/components/hero/HButton.vue'
import HModal from '@/components/hero/HModal.vue'
import HTabs from '@/components/hero/HTabs.vue'
import AdoptPanel from '@/components/strm/AdoptPanel.vue'
import StrmRebasePanel from '@/components/strm/StrmRebasePanel.vue'

/**
 * 扩展功能「STRM 迁移」：两件事都是处理本地已有的 STRM，放在一起。
 * - 替换地址：只换内容开头的地址（换了 IP / 端口 / 挂载点），文件归谁管不变（strmrebase.go）；
 * - 接管其他项目：认出旧 STRM 指向哪个网盘文件，换成本站直链并登记台账，之后由本站管理（adopt.go）。
 * 两个页签各自挂载，切走时卸掉；任务在队列里照常跑完，结果在任务中心也看得到
 */
export type StrmMigrateTab = 'rebase' | 'adopt'

const show = defineModel<boolean>('show', { required: true })
const tab = defineModel<StrmMigrateTab>('tab', { default: 'rebase' })

const TABS: { value: StrmMigrateTab; label: string }[] = [
  { value: 'rebase', label: '替换地址' },
  { value: 'adopt', label: '接管其他项目' },
]
</script>

<template>
  <HModal v-model:show="show" title="STRM 迁移" width="860px">
    <div class="migrate">
      <HTabs v-model="tab" :items="TABS" variant="secondary" />
      <template v-if="show">
        <StrmRebasePanel v-if="tab === 'rebase'" />
        <AdoptPanel v-else />
      </template>
    </div>
    <template #footer>
      <HButton variant="tertiary" @click="show = false">关闭</HButton>
    </template>
  </HModal>
</template>

<style scoped>
.migrate {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
</style>
