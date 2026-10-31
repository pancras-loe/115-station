<script setup lang="ts">
import { computed, ref } from 'vue'
import HButton from '@/components/hero/HButton.vue'
import SectionCard from '@/components/ui/SectionCard.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import { shareStrmApi } from '@/api'
import type { ShareAdoptReport } from '@/api/sharestrm'
import { toastError, useFeedback } from '@/composables/useFeedback'

/**
 * 接管 p115strmhelper 的分享 STRM：把分享 STRM 目录设成那些 STRM 所在的目录，先预演再执行。
 * 路径与修改时间都不动，只把内容换成本站地址（Emby 观看记录保留）
 */
const emit = defineEmits<{ done: [] }>()
const { message, dialog } = useFeedback()
const rep = ref<ShareAdoptReport | null>(null)
const busy = ref(false)

const todo = computed(() => (rep.value?.groups ?? []).filter((g) => !g.problem).reduce((n, g) => n + g.files, 0))

async function preview() {
  busy.value = true
  try {
    rep.value = await shareStrmApi.adopt(false)
  } catch (e) {
    toastError(e, '扫描失败')
  } finally {
    busy.value = false
  }
}

async function apply() {
  const ok = await dialog.confirm({
    title: `接管 ${todo.value} 个分享 STRM`,
    content: '把这些 STRM 的内容换成本站地址，路径和修改时间不动，Emby 里的条目与观看记录保留。原来那个项目的分享 STRM 功能请关掉，免得它再改回去。',
    actions: [
      { label: '取消', value: false, variant: 'tertiary' },
      { label: '接管', value: true, variant: 'primary' },
    ],
  })
  if (!ok) return
  busy.value = true
  try {
    rep.value = await shareStrmApi.adopt(true)
    message.success(`换了 ${rep.value.adopted ?? 0} 个`)
    emit('done')
  } catch (e) {
    toastError(e, '接管失败')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <SectionCard
    title="接管其他项目的分享 STRM"
    hint="从 p115strmhelper 换过来：先把上面的「本地目录」设成它的分享 STRM 所在的目录（不能和媒体库重叠），再扫一遍。只换 STRM 里的地址，路径与修改时间不动。"
  >
    <template #extra>
      <HButton size="sm" variant="secondary" :loading="busy && !rep" @click="preview">扫描</HButton>
    </template>
    <template v-if="rep">
      <p class="sum">
        在 <span class="mono">{{ rep.root }}</span> 下扫到 {{ rep.scanned }} 个 STRM，其中 {{ rep.scanned - rep.other }} 个是分享 STRM。
        <template v-if="rep.adopted !== undefined">这次换了 {{ rep.adopted }} 个。</template>
      </p>
      <EmptyState v-if="!rep.groups.length" text="没有找到分享 STRM。" />
      <ul v-else class="list">
        <li v-for="g in rep.groups" :key="g.share_code">
          <span class="mono">{{ g.share_code }}</span>
          <span>{{ g.dir }}</span>
          <span>{{ g.files }} 个待接管<template v-if="g.known">，{{ g.known }} 个已登记</template></span>
          <span v-if="g.problem" class="bad">{{ g.problem }}</span>
        </li>
      </ul>
      <ul v-if="rep.errors?.length" class="errs">
        <li v-for="(e, i) in rep.errors" :key="i">{{ e }}</li>
      </ul>
      <div class="foot">
        <HButton variant="primary" :disabled="!todo" :loading="busy" @click="apply">接管 {{ todo }} 个</HButton>
      </div>
    </template>
  </SectionCard>
</template>

<style scoped>
.sum {
  margin: 0 0 8px;
  font-size: 13px;
}
.mono {
  font-family: var(--font-mono, ui-monospace, monospace);
}
.list,
.errs {
  list-style: none;
  margin: 0;
  padding: 0;
  font-size: 12.5px;
}
.list li {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 14px;
  padding: 6px 0;
  border-top: 1px solid var(--border);
}
.bad,
.errs li {
  color: var(--danger);
}
.foot {
  display: flex;
  justify-content: flex-end;
  margin-top: 10px;
}
</style>
