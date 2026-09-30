<script setup lang="ts">
import { useRouter } from 'vue-router'
import HAlert from '@/components/hero/HAlert.vue'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import HSegmented from '@/components/hero/HSegmented.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import FormActions from '@/components/ui/FormActions.vue'
import { useSetting } from '@/composables/useSetting'
import { useFullSetting } from '@/pages/strm/fullSetting'

/**
 * 监控上传。原来和「转存下载」合在「上传下载」一页里，转存下载并进「影视转存」之后
 * 只剩这一项；路由地址 /upload-download 沿用（可能被收藏），旧的 ?tab=download 由路由重定向。
 */
const router = useRouter()
const media = useFullSetting()
const monitor = useSetting('monitor', { enabled: false })
</script>

<template>
  <SectionCard title="监控上传" hint="默认禁止，显式开启后才向 115 写入">
    <HAlert status="warning" class="note">
      上传属于 115 风控敏感操作，默认关闭。开启后会监控统一配置的本地媒体库根目录，
      自动检测本站或 Emby 新产生的标准图片（poster / fanart / banner / seasonXX-poster 等）与
      NFO（tvshow / movie / season / 每集同名 .nfo），按相对路径上传到 115 对应目录。
    </HAlert>

    <FieldRow label="允许上传到 115" tip="总开关。关闭时，定时监控、刮削结束回传和兜底回传都不会上传任何文件。">
      <HSegmented
        v-model="monitor.model.value.enabled"
        :options="[{ label: '允许', value: true }, { label: '禁止（推荐）', value: false }]"
      />
    </FieldRow>

    <FieldRow label="本地媒体库根目录" tip="与全量同步、增量同步、整理和影视刮削共用同一位置；目标固定为 115 媒体库。">
      <HInput :model-value="media.model.value.local_path || '未配置'" readonly />
      <HButton variant="ghost" class="text-btn location-link" @click="router.push({ name: 'accounts' })">
        前往「账号与媒体库」修改
      </HButton>
    </FieldRow>

    <FormActions>
      <HButton variant="primary" :loading="monitor.saving.value" @click="monitor.save()">保存配置</HButton>
      <HButton variant="tertiary" @click="monitor.reset">重置配置</HButton>
    </FormActions>
  </SectionCard>
</template>

<style scoped>
.note {
  margin-bottom: 12px;
}
.location-link {
  margin-top: 6px;
}
</style>
