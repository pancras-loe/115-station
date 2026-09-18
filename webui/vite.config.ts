import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { viteSingleFile } from 'vite-plugin-singlefile'

// 单文件打包（viteSingleFile）是刻意选择，不是图省事：
// 部署场景是跨境明文 HTTP，首条连接（HTML 文档）几乎总能成功，而后续并行
// 拉取的静态资源大概率被连接重置（ERR_CONNECTION_RESET）——旧版前端为此
// 专门在 Go 启动时把 style.css 内联进 index.html。打成单文件后该问题从根上
// 消失：一次请求拿到全部前端。体积代价由 gzip 中间件 + index.html 的协商
// 缓存（ETag/304）抵消，版本未变的刷新只传几十字节。
const BACKEND = process.env.BACKEND || 'http://127.0.0.1:6060'

export default defineConfig({
  plugins: [vue(), tailwindcss(), viteSingleFile()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  build: {
    // Go 直接服务这个目录，不提交进仓库
    outDir: 'dist',
    emptyOutDir: true,
    // 单文件模式下 chunk 警告没有意义
    chunkSizeWarningLimit: 4096,
    target: 'es2020',
  },
  server: {
    // 必须显式指定 host：Vite 默认的 'localhost' 在 Windows + Node 17+ 上
    // 经常只绑到 IPv6 回环（::1），而浏览器解析 localhost 走 IPv4，
    // 结果是 127.0.0.1 连接被拒。绑 0.0.0.0 同时覆盖 IPv4 回环与局域网访问
    //（手机上调移动端布局用得上）。
    host: true,
    port: 5173,
    // 端口被占时直接报错，而不是悄悄换到 5174/5175 —— 否则代理目标与
    // 你打开的地址容易对不上
    strictPort: true,
    // 本地开发：前端跑 Vite，后端跑 Go。后端端口默认 6060（config.Load 的 PORT），
    // 改过 PORT 的用 `BACKEND=http://127.0.0.1:xxxx npm run dev` 覆盖。
    proxy: Object.fromEntries(
      ['/api', '/d', '/poster', '/embyimg', '/tmdb', '/covergen', '/onebot'].map((p) => [
        p,
        { target: BACKEND, changeOrigin: true },
      ]),
    ),
  },
})
