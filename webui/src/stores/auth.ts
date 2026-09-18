import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { authApi, setUnauthorizedHandler, tokenStorage } from '@/api'

export const useAuthStore = defineStore('auth', () => {
  const token = ref<string | null>(tokenStorage.get())
  const username = ref(localStorage.getItem('username') ?? '')
  /** null = 尚未探测。false = 容器没配 AUTH_USER/AUTH_PASSWORD，登录页要给配置指引 */
  const initialized = ref<boolean | null>(null)
  const booting = ref(true)

  const isAuthed = computed(() => !!token.value)

  async function bootstrap() {
    try {
      const s = await authApi.status()
      initialized.value = s.initialized
    } catch {
      // 状态接口挂了也要让用户看到登录页，而不是空白
      initialized.value = true
    } finally {
      booting.value = false
    }
  }

  async function login(user: string, pass: string) {
    const res = await authApi.login(user, pass)
    tokenStorage.set(res.token)
    localStorage.setItem('username', res.username)
    token.value = res.token
    username.value = res.username
  }

  function logout() {
    tokenStorage.clear()
    token.value = null
  }

  /** api 层拿到鉴权中间件的 401 时回调，把登录态清掉（路由守卫随即跳登录页） */
  setUnauthorizedHandler(() => {
    token.value = null
  })

  return { token, username, initialized, booting, isAuthed, bootstrap, login, logout }
})
