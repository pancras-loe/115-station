import type { GlobalThemeOverrides } from 'naive-ui'

/**
 * Naive UI 的主题覆盖从 main.css 的 CSS 变量里现读，而不是在这里再抄一份色值。
 * 目的是让设计令牌只有一处定义——两处维护必然漂移（改了暗色忘了组件库，
 * 表现为「页面暗了但弹窗还是白的」这类最难排查的样式 bug）。
 */
function tokens() {
  const s = getComputedStyle(document.documentElement)
  const v = (name: string) => s.getPropertyValue(name).trim()
  return {
    primary: v('--c-primary'),
    primaryHover: v('--c-primary-hover'),
    primaryActive: v('--c-primary-active'),
    primarySoft: v('--c-primary-soft'),
    success: v('--c-success'),
    warning: v('--c-warning'),
    danger: v('--c-danger'),
    info: v('--c-info'),
    bgBase: v('--c-bg-base'),
    bgElevated: v('--c-bg-elevated'),
    bgRaised: v('--c-bg-raised'),
    bgHover: v('--c-bg-hover'),
    border: v('--c-border'),
    borderStrong: v('--c-border-strong'),
    text1: v('--c-text-1'),
    text2: v('--c-text-2'),
    text3: v('--c-text-3'),
    text4: v('--c-text-4'),
    radius: v('--radius'),
    radiusSm: v('--radius-sm'),
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
      modalColor: t.bgElevated,
      popoverColor: t.bgElevated,
      tableColor: t.bgElevated,
      tableHeaderColor: t.bgRaised,
      inputColor: t.bgElevated,
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

      borderRadius: t.radius,
      borderRadiusSmall: t.radiusSm,
      fontFamily: t.fontSans,
      fontFamilyMono: t.fontMono,
      fontSize: '14px',
      heightSmall: '30px',
      heightMedium: '36px',
      heightLarge: '40px',
    },
    Card: {
      borderRadius: 'var(--radius-lg)',
      paddingMedium: '20px 22px',
    },
    Button: {
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
      borderRadius: t.radiusSm,
    },
  }
}
