<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import HButton from '@/components/hero/HButton.vue'
import HCheckbox from '@/components/hero/HCheckbox.vue'
import HModal from '@/components/hero/HModal.vue'
import HNumberInput from '@/components/hero/HNumberInput.vue'
import HPopconfirm from '@/components/hero/HPopconfirm.vue'
import HSwitch from '@/components/hero/HSwitch.vue'
import FieldRow from '@/components/ui/FieldRow.vue'
import CronField from '@/components/ui/CronField.vue'
import { pluginsApi } from '@/api'
import type { PersonFillConfig, PersonFillInfo, PersonType } from '@/api/plugins'
import { toastError, useFeedback } from '@/composables/useFeedback'
import { JOB_STATUS } from '@/utils/jobStatus'

const show = defineModel<boolean>('show', { required: true })
const { message } = useFeedback()
const router = useRouter()

const DEFAULTS: PersonFillConfig = {
  enabled: false,
  cron: '30 3 * * *',
  types: ['actor', 'director', 'writer'],
  image: true,
  zh_name: true,
  zh_bio: true,
  max_cast: 20,
  max_per_run: 300,
}

const TYPES: { v: PersonType; label: string }[] = [
  { v: 'actor', label: '演员' },
  { v: 'director', label: '导演' },
  { v: 'writer', label: '编剧' },
]

const form = ref<PersonFillConfig>({ ...DEFAULTS })
const info = ref<PersonFillInfo | null>(null)
const saving = ref(false)
const resetting = ref(false)

async function load() {
  try {
    const d = await pluginsApi.personFillConfig()
    info.value = d.data ?? null
    form.value = { ...DEFAULTS, ...(d.data?.config ?? {}) }
  } catch (e) {
    toastError(e, '读取配置失败')
  }
}

watch(show, (v) => {
  if (v) void load()
})

function toggleType(t: PersonType, on: boolean) {
  const s = new Set(form.value.types)
  if (on) s.add(t)
  else s.delete(t)
  form.value.types = TYPES.map((x) => x.v).filter((v) => s.has(v))
}

const invalid = computed(() => {
  if (!form.value.types.length) return '至少勾选一类人物'
  if (!form.value.image && !form.value.zh_name && !form.value.zh_bio) return '头像、中文名、中文简介至少开一项'
  return ''
})

/** 记账统计：这些人物查过了但没补全，到期前任务不再碰它们 */
const markRows = computed(() => {
  const m = info.value?.marks ?? {}
  const t = info.value?.state_text ?? {}
  return Object.entries(m)
    .filter(([, n]) => n > 0)
    .map(([k, n]) => ({ k, text: t[k] ?? k, n }))
})
const markTotal = computed(() => markRows.value.reduce((s, r) => s + r.n, 0))

async function save() {
  if (invalid.value) {
    message.warning(invalid.value)
    return
  }
  saving.value = true
  try {
    await pluginsApi.savePersonFill({
      ...form.value,
      cron: form.value.cron.trim() || DEFAULTS.cron,
      max_cast: form.value.max_cast ?? 0,
      max_per_run: form.value.max_per_run || DEFAULTS.max_per_run,
    })
    message.success('已保存')
    show.value = false
  } catch (e) {
    toastError(e, '保存失败')
  } finally {
    saving.value = false
  }
}

async function reset() {
  resetting.value = true
  try {
    const d = await pluginsApi.resetPersonFill()
    message.success(d.message || '已清空')
    await load()
  } catch (e) {
    toastError(e, '清空失败')
  } finally {
    resetting.value = false
  }
}

function openLastJob() {
  const id = info.value?.last_job?.id
  if (!id) return
  show.value = false
  void router.push({ name: 'tasks', query: { job: String(id) } })
}
</script>

<template>
  <HModal v-model:show="show" title="演职人员补全" width="560px">
    <p class="lead">
      给 Emby 里缺头像、名字不是中文的演职人员补上 TMDB 的头像、中文名与中文简介。
      通过 Emby API 写入（人物头像存在 Emby 自己的元数据目录里，不在媒体文件夹旁边），
      头像经本站的 TMDB 代理下载，Emby 连不上 TMDB 也能补。不发任何 115 请求。
    </p>

    <FieldRow label="定时运行" tip="开启后按计划自动排进任务队列（单独一条人物队列，不挡整理与刮削）。「立即运行」不受此开关影响。">
      <HSwitch v-model="form.enabled" aria-label="定时运行" />
    </FieldRow>
    <FieldRow label="执行计划" :hint="info?.next_run ? `下次：${info.next_run}` : ''">
      <CronField v-model="form.cron" placeholder="30 3 * * *" />
    </FieldRow>
    <FieldRow label="人物类型" tip="演员包括剧集的客串。剧集的导演、编剧挂在每一集上，勾选后会额外读取各集的人员表。">
      <div class="checks">
        <HCheckbox
          v-for="t in TYPES"
          :key="t.v"
          :checked="form.types.includes(t.v)"
          @update:checked="(v) => toggleType(t.v, v)"
        >
          {{ t.label }}
        </HCheckbox>
      </div>
    </FieldRow>
    <FieldRow label="补头像" tip="只补 Emby 里还没有头像的人物，已有的不覆盖。">
      <HSwitch v-model="form.image" aria-label="补头像" />
    </FieldRow>
    <FieldRow
      label="中文名"
      tip="名字不含汉字的人物改成 TMDB 上的中文名（翻译或别名，繁体统一转简体），并锁定名字字段，免得 Emby 刷新时又改回去。若同名的人物已存在则不改名，只补头像。补过的中文名也会用于之后刮削写 NFO 的演员表。"
    >
      <HSwitch v-model="form.zh_name" aria-label="中文名" />
    </FieldRow>
    <FieldRow label="中文简介" tip="原简介不是中文、而 TMDB 有中文简介时替换，同样锁定字段。">
      <HSwitch v-model="form.zh_bio" aria-label="中文简介" />
    </FieldRow>
    <FieldRow label="每部片演员" tip="每部片只看演员表前 N 位（导演、编剧不占名额）。龙套在 TMDB 上多半既无头像也无中文名。0 = 不限。">
      <HNumberInput v-model="form.max_cast" :min="0" :max="200" aria-label="每部片演员数">
        <template #suffix>位</template>
      </HNumberInput>
    </FieldRow>
    <FieldRow label="单次上限" tip="一次任务最多处理多少个人物，剩下的下次接着补。每个人物约需 1~3 次 TMDB 请求。">
      <HNumberInput v-model="form.max_per_run" :min="1" :max="5000" :step="50" aria-label="单次上限">
        <template #suffix>人</template>
      </HNumberInput>
    </FieldRow>

    <div class="stats">
      <div class="stats-head">
        <span>
          已缓存 TMDB 人物 {{ info?.cached ?? 0 }} 个（其中有中文名 {{ info?.cached_zh ?? 0 }} 个）
        </span>
      </div>
      <div v-if="markTotal" class="marks">
        <span>暂缓 {{ markTotal }} 个人物：</span>
        <span v-for="r in markRows" :key="r.k" class="mark">{{ r.text }} {{ r.n }}</span>
        <span class="muted">（查无结果的一个月后再试，出错的次日再试）</span>
      </div>
      <div v-if="info?.last_job" class="last">
        <span>上次：</span>
        <a class="link" @click="openLastJob">{{ JOB_STATUS[info.last_job.status]?.text ?? info.last_job.status }}</a>
        <span v-if="info.last_job.message" class="muted"> · {{ info.last_job.message }}</span>
      </div>
    </div>

    <template #footer>
      <div class="foot">
        <HPopconfirm v-if="markTotal" side="top" @confirm="reset">
          <HButton variant="tertiary" :loading="resetting">清空记账</HButton>
          <template #content>清空后，下次任务会把暂缓的人物全部重新查一遍 TMDB。</template>
        </HPopconfirm>
        <span class="spacer" />
        <HButton variant="tertiary" @click="show = false">取消</HButton>
        <HButton variant="primary" :loading="saving" :disabled="!!invalid" @click="save">保存</HButton>
      </div>
    </template>
  </HModal>
</template>

<style scoped>
.lead {
  margin: 0 0 14px;
  font-size: 13px;
  line-height: 1.6;
  color: var(--muted);
}
.checks {
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
}
.stats {
  margin-top: 14px;
  padding: 12px 14px;
  border-radius: var(--r-lg);
  background: var(--surface-secondary);
  font-size: 13px;
  line-height: 1.7;
  color: var(--foreground);
}
.marks .mark {
  margin-right: 10px;
}
.muted {
  color: var(--muted);
}
.link {
  color: var(--accent);
  cursor: pointer;
}
.foot {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
}
.spacer {
  flex: 1;
}
</style>
