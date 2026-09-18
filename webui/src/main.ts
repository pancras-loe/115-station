import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import './styles/main.css'

async function bootstrap() {
  // 假数据预览构建（npm run build:mock）；正式构建里这段会被静态求值为 false 后摇掉
  if (import.meta.env.VITE_MOCK === '1') {
    const { installMockApi } = await import('./api/mock')
    installMockApi()
    localStorage.setItem('token', 'mock-token')
    localStorage.setItem('username', 'demo')
  }

  const app = createApp(App)
  app.use(createPinia())
  app.use(router)
  app.mount('#app')
}

bootstrap()
