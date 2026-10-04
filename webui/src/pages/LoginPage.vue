<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import HButton from '@/components/hero/HButton.vue'
import HInput from '@/components/hero/HInput.vue'
import { ArrowLeft, Eye, EyeOff, KeyRound, ShieldCheck, UserRound } from '@lucide/vue'
import ThemeToggle from '@/components/ThemeToggle.vue'
import { useAuthStore } from '@/stores/auth'
import { toastError, useFeedback } from '@/composables/useFeedback'
import BrandMark from '@/components/BrandMark.vue'
import { authApi } from '@/api'
import type { LoginWallpaper } from '@/api/auth'

const auth = useAuthStore()
const router = useRouter()
const route = useRoute()
const { message } = useFeedback()

const username = ref('')
const password = ref('')
const submitting = ref(false)
const showPwd = ref(false)
// 二步验证：密码通过后后端回 otp_required，切到验证码这一步（账号密码留着，随验证码一起再交一次）
const otpStep = ref(false)
const otp = ref('')
const otpInput = ref<InstanceType<typeof HInput>>()

// 账号来源是容器环境变量 AUTH_USER / AUTH_PASSWORD（未配置时首启生成随机密码，
// 见容器日志）。网页注册功能已移除，所以「未初始化」只能给配置指引，不能给注册入口。
const notInitialized = computed(() => auth.initialized === false)

// ---- 背景剧照轮播（参考 MoviePilot 登录页）----
// 剧照由后端预取并转码好放在本站（TMDB 不通也有图，见 loginwall.go），这里只管怎么显示：
// - 第一张：先铺内嵌的 32px 小图（列表里自带，不发请求）模糊着顶上，大图到了再淡入 ——
//   服务器上行带宽小的时候也不会是一片空白；
// - 之后的轮换：下一张大图在内存里加载好才切，切过去不会是一块正在往下刷的半张图；拉不到就跳过这一轮。
// 两层叠着交替淡入淡出。一张都没有（没配过 TMDB）就保持原来的光晕背景
const WALL_INTERVAL = 12_000
interface WallLayer {
  thumb: string
  full: string
}
const walls = ref<LoginWallpaper[]>([])
const layers = ref<[WallLayer, WallLayer]>([
  { thumb: '', full: '' },
  { thumb: '', full: '' },
])
const front = ref(0)
const wallIdx = ref(-1)
const current = computed(() => (wallIdx.value >= 0 ? walls.value[wallIdx.value] : null))
let timer: number | undefined
let alive = true

// 要哪一档：剧照按 cover 铺满，16:9 的图在竖屏上要按高度撑，实际显示宽度比屏幕宽得多。
// 不乘设备像素比：背景压着暗角，960 / 1280 两档够用，省带宽优先
function wallUrl(w: LoginWallpaper) {
  const need = Math.max(window.innerWidth, (window.innerHeight * 16) / 9)
  return `/api/auth/wallpaper?path=${encodeURIComponent(w.path)}&w=${Math.round(need)}`
}

function loadImage(url: string) {
  return new Promise<boolean>((resolve) => {
    const img = new Image()
    img.onload = () => resolve(true)
    img.onerror = () => resolve(false)
    img.src = url
  })
}

async function showFirst(i: number) {
  const w = walls.value[i]
  layers.value[front.value] = { thumb: w.thumb, full: '' }
  wallIdx.value = i
  const url = wallUrl(w)
  if ((await loadImage(url)) && alive && wallIdx.value === i) layers.value[front.value].full = url
}

async function showNext(i: number) {
  const w = walls.value[i]
  const url = wallUrl(w)
  if (!(await loadImage(url)) || !alive) return
  const next = 1 - front.value
  layers.value[next] = { thumb: w.thumb, full: url }
  front.value = next
  wallIdx.value = i
}

onMounted(async () => {
  try {
    const { items } = await authApi.wallpapers()
    if (!alive || !items?.length) return
    walls.value = items
    let i = Math.floor(Math.random() * items.length)
    void showFirst(i)
    if (items.length > 1) {
      timer = window.setInterval(() => {
        i = (i + 1) % items.length
        void showNext(i)
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
  if (otpStep.value && !/^\d{6}$/.test(otp.value.trim())) {
    message.warning('请输入 6 位验证码')
    return
  }
  submitting.value = true
  try {
    const r = await auth.login(username.value, password.value, otpStep.value ? otp.value.trim() : undefined)
    if (r === 'otp') {
      otpStep.value = true
      otp.value = ''
      await nextTick()
      otpInput.value?.focus()
      return
    }
    message.success('登录成功')
    const redirect = route.query.redirect
    router.replace(typeof redirect === 'string' ? redirect : '/')
  } catch (e) {
    toastError(e, '登录失败')
    if (otpStep.value) otp.value = ''
  } finally {
    submitting.value = false
  }
}

function backToPassword() {
  otpStep.value = false
  otp.value = ''
}

/** 输满 6 位直接提交，省一次点按钮 */
function onOtpInput(v: string) {
  const digits = v.replace(/\D/g, '').slice(0, 6)
  otp.value = digits
  if (digits.length === 6 && !submitting.value) submit()
}
</script>

<template>
  <div class="auth" :class="{ 'has-wall': current }">
    <div class="auth-bg" aria-hidden="true" />
    <div v-if="walls.length" class="wall" :class="{ ready: current }" aria-hidden="true">
      <div v-for="(layer, i) in layers" :key="i" class="wall-layer" :class="{ on: current && front === i }">
        <div class="wall-img wall-thumb" :style="layer.thumb ? { backgroundImage: `url(${layer.thumb})` } : undefined" />
        <div
          class="wall-img wall-full"
          :class="{ loaded: layer.full }"
          :style="layer.full ? { backgroundImage: `url(${layer.full})` } : undefined"
        />
      </div>
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
      <form v-if="otpStep" class="form" @submit.prevent="submit">
        <div class="otp-head">
          <ShieldCheck :size="20" />
          <div>
            <div class="otp-title">二步验证</div>
            <div class="otp-sub">打开身份验证器，输入 {{ username }} 的 6 位验证码</div>
          </div>
        </div>
        <label class="field">
          <span class="field-label">验证码</span>
          <HInput
            ref="otpInput"
            :model-value="otp"
            placeholder="6 位数字"
            mono
            :input-attrs="{ name: 'otp', autocomplete: 'one-time-code', inputmode: 'numeric', maxlength: '6' }"
            @update:model-value="onOtpInput"
          >
            <template #prefix><ShieldCheck :size="16" /></template>
          </HInput>
        </label>
        <HButton variant="primary" size="lg" full-width type="submit" :loading="submitting" class="submit">
          验证并登录
        </HButton>
        <button type="button" class="back" @click="backToPassword"><ArrowLeft :size="14" /> 返回重新输入密码</button>
      </form>

      <form v-else class="form" @submit.prevent="submit">
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
.otp-head {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  color: var(--accent);
}
.otp-title {
  font-size: 15px;
  font-weight: 600;
  color: var(--foreground);
}
.otp-sub {
  margin-top: 2px;
  font-size: 12.5px;
  color: var(--muted);
}
.back {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  padding: 0;
  border: 0;
  background: none;
  font-size: 12.5px;
  color: var(--muted);
  cursor: pointer;
}
.back:hover {
  color: var(--accent);
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
.wall-img {
  position: absolute;
  inset: 0;
  background: center / cover no-repeat;
}
/* 32px 小图放大必然是马赛克，糊开当底色；略放大一圈，免得模糊的边缘透出底下的页面 */
.wall-thumb {
  filter: blur(24px);
  transform: scale(1.1);
}
.wall-full {
  opacity: 0;
  transition: opacity 0.8s ease;
}
.wall-full.loaded {
  opacity: 1;
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
