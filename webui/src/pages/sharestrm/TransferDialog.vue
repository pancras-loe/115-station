<script setup lang="ts">
import { ref } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import HModal from '@/components/hero/HModal.vue'
import SubdirPicker from '@/components/transfer/SubdirPicker.vue'
import { shareStrmApi } from '@/api'
import type { ShareSource } from '@/api/sharestrm'
import { useStagingSubs } from '@/composables/stagingSubs'
import { toastError, useFeedback } from '@/composables/useFeedback'

/**
 * 转存这份：整个分享转进某个库的转存文件夹，照常整理入库；入库后这里对应的分享 STRM 自动收掉
 * （后端 sharestrmxfer.go）。转存失败什么都不动
 */
const props = defineProps<{ source: ShareSource | null }>()
const show = defineModel<boolean>('show', { required: true })
const emit = defineEmits<{ done: [] }>()
const { message } = useFeedback()
const { current: subdir } = useStagingSubs()
const saving = ref(false)

async function go() {
  if (!props.source) return
  saving.value = true
  try {
    const d = await shareStrmApi.transfer(props.source.id, subdir.value || '')
    message.success(d.message)
    show.value = false
    emit('done')
  } catch (e) {
    toastError(e, '转存失败')
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <HModal v-model:show="show" :title="`转存这份：${source?.title || source?.share_code || ''}`" width="520px">
    <p class="note">
      把整个分享转存进你的 115，照常自动整理入库。分享不会再失效，但会占网盘空间。
    </p>
    <p class="note">
      整理入库后，这里对应的分享 STRM 会自动收掉，Emby 里换成库里那一份；没入库的（整理失败、判输了洗版）分享 STRM 留着照常能播。转存后不再追更。
    </p>
    <div class="pick">
      <span>转存到</span>
      <SubdirPicker />
    </div>
    <template #footer>
      <div class="foot-right">
        <HButton variant="tertiary" @click="show = false">取消</HButton>
        <HButton variant="primary" :loading="saving" @click="go">转存</HButton>
      </div>
    </template>
  </HModal>
</template>

<style scoped>
.note {
  margin: 0 0 8px;
  font-size: 13px;
  color: var(--muted);
}
.pick {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 6px;
  font-size: 13px;
}
.foot-right {
  display: flex;
  gap: 8px;
  justify-content: flex-end;
}
</style>
