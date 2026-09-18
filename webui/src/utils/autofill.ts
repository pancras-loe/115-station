/**
 * 阻止浏览器把「本站保存的登录密码」自动填进配置页的密钥框。
 *
 * 现象：Emby 管理页的「服务器地址 + API 密钥」、企业微信的「企业 ID + 应用 Secret」
 * 这类「文本框紧跟密码框」的组合，会被 Chrome/Edge 的启发式判定成登录表单，
 * 于是把本站的管理员账号密码填进去——用户一保存就把后台密码写进了 Emby 配置。
 *
 * 有效的做法：
 *   1. 密码框标 `autocomplete="new-password"`。浏览器据此认定这是「设置新密码」
 *      而不是「登录」，不再填入已保存的凭据。单纯写 `off` 会被 Chrome 忽略。
 *   2. 配对的文本框给一个不像用户名的 name，并显式 `autocomplete="off"`，
 *      免得被当成 username 字段。
 *   3. 密码管理器（1Password / LastPass / Dashlane）各有自己的忽略属性，一并带上。
 *
 * 真正的登录页（LoginPage）不应该用这些——那里恰恰需要自动填充。
 */

const MANAGER_IGNORE = {
  'data-1p-ignore': 'true',
  'data-lpignore': 'true',
  'data-bwignore': 'true',
  'data-form-type': 'other',
} as const

/** 密钥 / token / 密码这类需要遮罩、但绝不能被凭据填充的字段 */
export function secretProps(name: string) {
  return {
    name,
    autocomplete: 'new-password',
    autocorrect: 'off',
    autocapitalize: 'off',
    spellcheck: false,
    ...MANAGER_IGNORE,
  }
}

/** 紧挨着密钥字段的普通文本框（服务器地址、企业 ID、client_id 等） */
export function plainProps(name: string) {
  return {
    name,
    autocomplete: 'off',
    autocorrect: 'off',
    autocapitalize: 'off',
    spellcheck: false,
    ...MANAGER_IGNORE,
  }
}
