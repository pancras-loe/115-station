import { computed, ref } from 'vue'
import { organizeApi } from '@/api'
import type { ScrapeConfig } from '@/api/organize'
import { toastError, useFeedback } from '@/composables/useFeedback'

export const SCRAPE_DEFAULTS: ScrapeConfig = {
  local_root: '',
  write_nfo: true,
  write_images: true,
  force: false,
  auto_after_organize: false,
  auto_after_sync: false,
  probe_streams: false,
  skip_shared_stills: true,
}

/** 后端回的 cfg 补成完整配置：write_nfo / write_images / skip_shared_stills 缺省视为开启 */
export function normalizeScrapeConfig(c: Partial<ScrapeConfig> = {}): ScrapeConfig {
  return {
    local_root: c.local_root ?? '',
    write_nfo: c.write_nfo !== false,
    write_images: c.write_images !== false,
    force: !!c.force,
    auto_after_organize: !!c.auto_after_organize,
    auto_after_sync: !!c.auto_after_sync,
    probe_streams: !!c.probe_streams,
    skip_shared_stills: c.skip_shared_stills !== false,
  }
}

/**
 * 刮削配置（setting `scrape`）的读写。「刮削」页签与「媒体信息」页签（轨道探测）各改一部分字段，
 * 但后端是整存整取：两边都在挂载时重新读一次、保存时交整份，页签按需挂载，不会拿着旧值互相覆盖。
 */
export function useScrapeConfig() {
  const { message } = useFeedback()
  const cfg = ref<ScrapeConfig>({ ...SCRAPE_DEFAULTS })
  /** 读回来的那份，用来判断改过没有；先按默认值起步，否则读回来之前保存条会闪一下「有未保存的改动」 */
  const savedJson = ref(JSON.stringify(cfg.value))
  const loaded = ref(false)
  const saving = ref(false)

  async function load() {
    try {
      // 后端是 { cfg, status }：摊平读的话字段全是 undefined，开关一律显示「关闭」
      const res = await organizeApi.getScrapeConfig()
      cfg.value = normalizeScrapeConfig(res.cfg)
    } catch {
      // 首次使用尚无配置
    } finally {
      savedJson.value = JSON.stringify(cfg.value)
      loaded.value = true
    }
  }

  const dirty = computed(() => JSON.stringify(cfg.value) !== savedJson.value)

  /** localRoot：刮削写到本地媒体库根，以「账号与媒体库」里的为准，保存时一并带上 */
  async function save(localRoot?: string) {
    saving.value = true
    try {
      if (localRoot !== undefined) cfg.value.local_root = localRoot
      await organizeApi.saveScrapeConfig(cfg.value)
      savedJson.value = JSON.stringify(cfg.value)
      message.success('保存成功')
    } catch (e) {
      toastError(e, '保存失败')
    } finally {
      saving.value = false
    }
  }

  return { cfg, loaded, saving, dirty, load, save }
}
