<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import { Eye, EyeOff, KeyRound, UserRound } from '@lucide/vue'
import ThemeToggle from '@/components/ThemeToggle.vue'
import { useAuthStore } from '@/stores/auth'
import { toastError, useFeedback } from '@/composables/useFeedback'
import BrandMark from '@/components/BrandMark.vue'
import { authApi } from '@/api'
import type { LoginWallpaper } from '@/api/auth'
import { tmdbImageUrl } from '@/api/resources'

const auth = useAuthStore()
const router = useRouter()
const route = useRoute()
const { message } = useFeedback()

const username = ref('')
const password = ref('')
const submitting = ref(false)
const showPwd = ref(false)

// 账号来源是容器环境变量 AUTH_USER / AUTH_PASSWORD（未配置时首启生成随机密码，
// 见容器日志）。网页注册功能已移除，所以「未初始化」只能给配置指引，不能给注册入口。
const notInitialized = computed(() => auth.initialized === false)

// ---- 背景剧照轮播（参考 MoviePilot 登录页）----
// 两层叠着交替淡入淡出：下一张先在内存里加载好再切，避免切过去是一块正在往下刷的半张图。
// 拿不到剧照（没配 TMDB / 连不上）就保持原来的光晕背景
const WALL_INTERVAL = 12_000
const walls = ref<LoginWallpaper[]>([])
const layers = ref<[string, string]>(['', ''])
const front = ref(0)
const wallIdx = ref(-1)
const current = computed(() => (wallIdx.value >= 0 ? walls.value[wallIdx.value] : null))
let timer: number | undefined
let alive = true

const wallUrl = (w: LoginWallpaper) => tmdbImageUrl(w.path, 'w1280')

function showWall(i: number) {
  const w = walls.value[i]
  if (!w) return
  const url = wallUrl(w)
  const img = new Image()
  img.onload = () => {
    if (!alive) return
    const next = wallIdx.value < 0 ? front.value : 1 - front.value
    layers.value[next] = url
    front.value = next
    wallIdx.value = i
  }
  // 拉不到就停在当前这张（或光晕背景），下一轮接着试下一张
  img.src = url
}

onMounted(async () => {
  try {
    const { items } = await authApi.wallpapers()
    if (!alive || !items?.length) return
    walls.value = items
    let i = Math.floor(Math.random() * items.length)
    showWall(i)
    if (items.length > 1) {
      timer = window.setInterval(() => {
        i = (i + 1) % items.length
        showWall(i)
      }, WALL_INTERVAL)
    }
  } catch {
    // 背景图只是点缀，拿不到不提示
  }
})

onBeforeUnmount(() => {
  alive = false
  window.clearInterval(timer)
})

async function submit() {
  if (!username.value || !password.value) {
    message.warning('请输入账号和密码')
    return
  }
  submitting.value = true
  try {
    await auth.login(username.value, password.value)
    message.success('登录成功')
    const redirect = route.query.redirect
    router.replace(typeof redirect === 'string' ? redirect : '/')
  } catch (e) {
    toastError(e, '登录失败')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="auth" :class="{ 'has-wall': current }">
    <div class="auth-bg" aria-hidden="true" />
    <div v-if="walls.length" class="wall" :class="{ ready: current }" aria-hidden="true">
      <div
        v-for="(url, i) in layers"
        :key="i"
        class="wall-layer"
        :class="{ on: current && front === i }"
        :style="url ? { backgroundImage: `url(${url})` } : undefined"
      />
      <div class="wall-shade" />
    </div>
    <div class="auth-toggle" :class="{ 'on-dark': current }"><ThemeToggle /></div>

    <div v-if="current" class="wall-caption on-dark">
      <span class="wall-title">{{ current.title }}</span>
      <span v-if="current.year" class="wall-year">{{ current.year }}</span>
      <span class="wall-src">TMDB 本周热门</span>
    </div>

    <div class="auth-card">
      <div class="brand">
        <BrandMark :size="56" class="brand-mark" />
        <div class="brand-name">Strm<span>Station</span></div>
        <div class="brand-sub">网盘媒体库管理 · STRM 自动生成</div>
      </div>

      <div v-if="notInitialized" class="notice">
        <strong>尚未设置管理员账号</strong>
        <p>
          请在 docker-compose 中配置环境变量 <code>AUTH_USER</code> / <code>AUTH_PASSWORD</code>
          后重启容器；未配置时首次启动已生成随机账号，见容器日志。
        </p>
      </div>

      <!-- 登录页是全站唯一「要」浏览器自动填充的地方：name / autocomplete 按标准写，
           密码框用真正的 type=password（其余页面的密钥框刻意避开它，见 SecretInput） -->
      <form class="form" @submit.prevent="submit">
        <label class="field">
          <span class="field-label">账号</span>
          <HInput
            v-model="username"
            placeholder="请输入账号"
            :input-attrs="{ name: 'username', autocomplete: 'username' }"
          >
            <template #prefix><UserRound :size="16" /></template>
          </HInput>
        </label>

        <label class="field">
          <span class="field-label">密码</span>
          <HInput
            v-model="password"
            :type="showPwd ? 'text' : 'password'"
            placeholder="请输入密码"
            :input-attrs="{ name: 'password', autocomplete: 'current-password' }"
          >
            <template #prefix><KeyRound :size="16" /></template>
            <template #suffix>
              <button type="button" class="eye" :aria-label="showPwd ? '隐藏密码' : '显示密码'" @click="showPwd = !showPwd">
                <component :is="showPwd ? EyeOff : Eye" :size="16" />
              </button>
            </template>
          </HInput>
        </label>

        <HButton variant="primary" size="lg" full-width type="submit" :loading="submitting" class="submit">
          登录
        </HButton>
      </form>
    </div>
  </div>
</template>

<style scoped>
.auth {
  position: relative;
  min-height: 100vh;
  display: grid;
  place-items: center;
  padding: 24px;
  overflow: hidden;
}

/* 背景光晕用主色的极低透明度做，暗色下自动跟着令牌变，不需要两套图 */
.auth-bg {
  position: absolute;
  inset: 0;
  background:
    radial-gradient(60rem 30rem at 15% -10%, color-mix(in oklab, var(--accent) 16%, transparent), transparent 70%),
    radial-gradient(50rem 28rem at 95% 110%, color-mix(in oklab, var(--accent) 12%, transparent), transparent 70%);
  pointer-events: none;
}

.auth-toggle {
  z-index: 1;
  position: absolute;
  top: 20px;
  right: 20px;
}

.auth-card {
  position: relative;
  width: 100%;
  max-width: 400px;
  padding: 36px 32px 32px;
  background: var(--surface);
  border-radius: var(--r-card);
  box-shadow: var(--overlay-shadow);
}

.brand {
  display: flex;
  flex-direction: column;
  align-items: center;
  margin-bottom: 26px;
}
.brand-mark {
  border-radius: 15px;
  box-shadow: 0 10px 24px -8px color-mix(in oklab, var(--accent) 60%, transparent);
  margin-bottom: 14px;
}
.brand-name {
  font-size: 22px;
  font-weight: 700;
  letter-spacing: -0.03em;
  color: var(--foreground);
}
.brand-name span {
  color: var(--accent);
}
.brand-sub {
  margin-top: 5px;
  font-size: 12.5px;
  color: var(--muted);
}

.notice {
  margin-bottom: 20px;
  padding: 12px 14px;
  border-radius: 16px;
  background: var(--warning-soft);
  font-size: 12.5px;
  line-height: 1.6;
  color: color-mix(in oklab, var(--foreground) 75%, var(--muted));
}
.notice strong {
  display: block;
  margin-bottom: 4px;
  color: var(--foreground);
}
.notice p {
  margin: 0;
}
.notice code {
  font-family: var(--font-mono);
  font-size: 11.5px;
  padding: 1px 5px;
  border-radius: 4px;
  background: color-mix(in oklab, var(--warning) 16%, transparent);
}

.form {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.field-label {
  font-size: 13px;
  font-weight: 500;
  color: color-mix(in oklab, var(--foreground) 80%, var(--muted));
}
.form :deep(.input-group) {
  min-height: 44px;
}
.eye {
  display: grid;
  place-items: center;
  padding: 0;
  border: 0;
  background: none;
  color: var(--muted);
  cursor: pointer;
}
.eye:hover {
  color: var(--accent);
}
.submit {
  margin-top: 8px;
}
/* ---- 背景剧照 ---- */
.wall {
  position: absolute;
  inset: 0;
  overflow: hidden;
  pointer-events: none;
}
.wall-layer {
  position: absolute;
  inset: 0;
  background: center / cover no-repeat;
  opacity: 0;
  transform: scale(1.06);
  transition:
    opacity 1.6s ease,
    transform 14s linear;
}
.wall-layer.on {
  opacity: 1;
  transform: scale(1);
}
/* 剧照上压一层暗角：中间的卡片和左下角的片名都要看得清，亮色主题也一样暗 */
.wall-shade {
  position: absolute;
  inset: 0;
  background:
    radial-gradient(ellipse at center, rgb(0 0 0 / 0.15), rgb(0 0 0 / 0.55) 75%),
    linear-gradient(to top, rgb(0 0 0 / 0.7), transparent 40%);
  opacity: 0;
  transition: opacity 1.6s ease;
}
.wall.ready .wall-shade {
  opacity: 1;
}
.has-wall .auth-bg {
  display: none;
}
.has-wall .auth-card {
  background: color-mix(in oklab, var(--surface) 82%, transparent);
  backdrop-filter: blur(18px) saturate(140%);
  -webkit-backdrop-filter: blur(18px) saturate(140%);
  box-shadow: 0 24px 60px -12px rgb(0 0 0 / 0.55);
}
.wall-caption {
  position: absolute;
  left: 32px;
  bottom: 28px;
  display: flex;
  align-items: baseline;
  gap: 10px;
  max-width: calc(100% - 64px);
  text-shadow: 0 1px 8px rgb(0 0 0 / 0.5);
}
.wall-title {
  font-size: 20px;
  font-weight: 700;
  letter-spacing: -0.02em;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.wall-year,
.wall-src {
  flex-shrink: 0;
  font-size: 13px;
  color: var(--muted);
}
.wall-src::before {
  content: '·';
  margin-right: 10px;
}
@media (prefers-reduced-motion: reduce) {
  .wall-layer {
    transform: none;
    transition: opacity 0.6s ease;
  }
}

/* 手机：卡片贴满宽度，少一圈留白 */
@media (max-width: 480px) {
  .auth {
    padding: 16px;
  }
  .auth-card {
    padding: 28px 20px 24px;
  }
  .wall-caption {
    left: 16px;
    bottom: 16px;
    max-width: calc(100% - 32px);
  }
  .wall-title {
    font-size: 16px;
  }
  .wall-src {
    display: none;
  }
}
</style>
