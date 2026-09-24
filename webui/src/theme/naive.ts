import type { GlobalThemeOverrides } from 'naive-ui'

/**
 * Naive UI 的主题覆盖从 main.css 的 CSS 变量里现读，而不是在这里再抄一份色值。
 * 目的是让设计令牌只有一处定义——两处维护必然漂移（改了暗色忘了组件库，
 * 表现为「页面暗了但弹窗还是白的」这类最难排查的样式 bug）。
 *
 * 令牌接到 HeroUI 之后，变量值是 oklch(...) / color-mix(...)：
 * getPropertyValue 拿到的是未求值的原文，Naive 内部算 hover/pressed 色用的
 * seemly 也不认 oklch。所以每个颜色都让浏览器画到 1×1 画布上再读回 rgba——
 * 这是唯一对所有颜色语法都成立的求值办法。
 */
let probe: HTMLElement | null = null
let ctx: CanvasRenderingContext2D | null = null

function resolveColor(name: string): string {
  if (!probe) {
    probe = document.createElement('span')
    probe.style.display = 'none'
    document.body.appendChild(probe)
    const canvas = document.createElement('canvas')
    canvas.width = canvas.height = 1
    ctx = canvas.getContext('2d', { willReadFrequently: true })
  }
  probe.style.color = `var(${name})`
  const computed = getComputedStyle(probe).color
  if (!ctx) return computed
  ctx.clearRect(0, 0, 1, 1)
  ctx.fillStyle = computed
  ctx.fillRect(0, 0, 1, 1)
  const [r, g, b, a] = ctx.getImageData(0, 0, 1, 1).data
  return a === 255 ? `rgb(${r}, ${g}, ${b})` : `rgba(${r}, ${g}, ${b}, ${(a / 255).toFixed(3)})`
}

function tokens() {
  const s = getComputedStyle(document.documentElement)
  const v = (name: string) => s.getPropertyValue(name).trim()
  const c = resolveColor
  return {
    primary: c('--c-primary'),
    primaryHover: c('--c-primary-hover'),
    primaryActive: c('--c-primary-active'),
    primarySoft: c('--c-primary-soft'),
    success: c('--c-success'),
    warning: c('--c-warning'),
    danger: c('--c-danger'),
    info: c('--c-info'),
    bgBase: c('--c-bg-base'),
    bgElevated: c('--c-bg-elevated'),
    bgRaised: c('--c-bg-raised'),
    bgHover: c('--c-bg-hover'),
    overlay: c('--overlay'),
    field: c('--field-background'),
    border: c('--c-border'),
    borderStrong: c('--c-border-strong'),
    text1: c('--c-text-1'),
    text2: c('--c-text-2'),
    text3: c('--c-text-3'),
    text4: c('--c-text-4'),
    radius: v('--radius'),
    radiusSm: v('--r-sm'),
    fontSans: v('--font-sans'),
    fontMono: v('--font-mono'),
  }
}

export function buildNaiveOverrides(): GlobalThemeOverrides {
  const t = tokens()
  return {
    common: {
      primaryColor: t.primary,
      primaryColorHover: t.primaryHover,
      primaryColorPressed: t.primaryActive,
      primaryColorSuppl: t.primaryHover,
      successColor: t.success,
      successColorHover: t.success,
      warningColor: t.warning,
      warningColorHover: t.warning,
      errorColor: t.danger,
      errorColorHover: t.danger,
      infoColor: t.info,

      bodyColor: t.bgBase,
      cardColor: t.bgElevated,
      // 浮层（弹窗/下拉/气泡）用 HeroUI 的 overlay 色：暗色下比卡片亮一档，靠这个分层
      modalColor: t.overlay,
      popoverColor: t.overlay,
      tableColor: t.bgElevated,
      tableHeaderColor: t.bgRaised,
      inputColor: t.field,
      inputColorDisabled: t.bgHover,
      actionColor: t.bgRaised,
      hoverColor: t.bgHover,

      borderColor: t.border,
      dividerColor: t.border,

      textColorBase: t.text1,
      textColor1: t.text1,
      textColor2: t.text2,
      textColor3: t.text3,
      textColorDisabled: t.text4,
      placeholderColor: t.text3,
      iconColor: t.text3,
      closeIconColor: t.text3,

      // HeroUI 表单控件的圆角是 --radius × 1.5（12px），Naive 的输入框/选择器沿用这个值
      borderRadius: '12px',
      borderRadiusSmall: t.radiusSm,
      fontFamily: t.fontSans,
      fontFamilyMono: t.fontMono,
      fontSize: '14px',
      heightSmall: '30px',
      heightMedium: '36px',
      heightLarge: '40px',
    },
    Card: {
      borderRadius: 'var(--r-card)',
      paddingMedium: '20px 22px',
    },
    // 胶囊按钮是 HeroUI 的标志性造型（rounded-3xl ≥ 按钮半高）
    Button: {
      borderRadiusTiny: '999px',
      borderRadiusSmall: '999px',
      borderRadiusMedium: '999px',
      borderRadiusLarge: '999px',
      fontWeightStrong: '500',
      paddingMedium: '0 16px',
    },
    Input: {
      borderHover: `1px solid ${t.primaryHover}`,
      borderFocus: `1px solid ${t.primary}`,
    },
    Tabs: {
      tabFontWeightActive: '500',
      tabGapMediumLine: '24px',
    },
    Menu: {
      itemColorActive: t.primarySoft,
      itemColorActiveHover: t.primarySoft,
      itemTextColorActive: t.primary,
      itemTextColorActiveHover: t.primary,
      itemIconColorActive: t.primary,
      itemIconColorActiveHover: t.primary,
      itemHeight: '40px',
      borderRadius: t.radius,
    },
    DataTable: {
      thFontWeight: '500',
      borderColor: t.border,
    },
    Tag: {
      borderRadius: '999px',
    },
    Dialog: {
      borderRadius: 'var(--r-card)',
    },
  }
}
