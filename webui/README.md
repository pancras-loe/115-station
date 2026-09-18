# webui —— 115-Station 新版管理后台前端

Vue 3 + TypeScript + Vite + Naive UI 重写版，正在逐页替换 `web/` 下的原生实现。

## 为什么是这套选型

| 选择 | 理由 |
|---|---|
| **Vue 3** 而非 React | 后台是 12 页、28 页签的重表单应用，`v-model` 相比受控组件能省掉大量样板 |
| **Naive UI** | TS 优先；暗色主题是一等公民（`darkTheme` + `n-config-provider`），不是补丁；自带 Tree / DataTable / Form / Modal，115 目录选择器直接用 Tree |
| **Tailwind v4** | 只做布局工具层。组件样式一律走 `<style scoped>` + 设计令牌，不用工具类堆组件 |
| **单文件打包** | 见下方「单文件打包」 |

## 开发

后端（Go）默认跑在 `:6060`（`config.Load()` 的 `PORT`），前端 dev server 跑在 `:5173`，
把 `/api`、`/poster`、`/embyimg` 等路径代理过去：

```bash
cd webui && npm install && npm run dev
```

改过 `PORT` 的用 `BACKEND=http://127.0.0.1:xxxx npm run dev` 覆盖代理目标。

没有后端也能看界面 —— 假数据模式会拦截 `window.fetch`：

```bash
cd webui && npm run build:mock
```

产物是 `dist-mock/index.html` 单个文件，双击即可在浏览器打开。

## 构建与检查

```bash
cd webui && npm run typecheck && npm run build
```

`npm run build` 的产物是 `dist/index.html` 一个文件，Go 直接服务它。
`dist/` 与 `dist-mock/` 都不提交进仓库，由 Dockerfile 的 node 阶段现场构建。

## 目录结构

```
src/
├── api/            # 按后端模块分的接口封装 + client.ts（超时/重试/401）
├── components/     # 跨页面复用组件；ui/ 下是设计系统原子件
├── composables/    # useFeedback（把 Naive 的 message/dialog 暴露给非组件代码）
├── layouts/        # AppLayout（侧栏 + 顶栏）、navItems（导航与图标）
├── pages/          # 每个路由一个页面组件
├── router/         # 路由表；路径沿用旧版，不要改
├── stores/         # Pinia：auth、theme
├── styles/main.css # 设计令牌（明暗双主题）—— 唯一的颜色定义处
├── theme/naive.ts  # 从 CSS 变量现读，生成 Naive 主题覆盖
├── types/          # 与后端 JSON 一一对应的类型
└── utils/          # 格式化、媒体 URL 拼装
```

## 三条约定（破坏了会出问题）

### 1. 颜色只从设计令牌取

`src/styles/main.css` 的 `:root` / `html.dark` 是**唯一**的颜色定义处。
组件里写死 `#fff`、`#1D2129` 这类字面值，暗色模式必然漏。

`theme/naive.ts` 用 `getComputedStyle` 现读这些变量来生成 Naive 的 theme-overrides，
所以令牌只有一份。不要在 `naive.ts` 里再抄一份色值 —— 两处维护必然漂移，
表现为「页面暗了但弹窗还是白的」这种最难排查的样式 bug。

### 2. 路由路径不能改

`/sync`、`/plugins`、`/subscriptions` 等路径沿用旧版前端，用户可能已经收藏。
换成「更规整」的命名会直接 404。

### 3. 单文件打包

`vite-plugin-singlefile` 把 JS/CSS 全部内联进 `index.html`，一次请求拿完整个前端。

这不是图省事。部署场景是跨境明文 HTTP：首条连接（HTML 文档）几乎总能成功，
而后续并行拉取的静态资源大概率被连接重置（`ERR_CONNECTION_RESET`）——
旧版前端为此专门在 Go 启动时把 `style.css` 内联进 `index.html`（见 `main.go`
的 `indexHTMLMarker`）。打成单文件后这个问题从根上消失。

体积代价由两件事抵消：Gin 的 gzip 中间件（590KB → 180KB），以及 `index.html`
的协商缓存（ETag/304，版本未变的刷新只传几十字节）。

> 如果后续页面全部迁完后单文件 gzip 超过 ~350KB，再考虑改成
> 「入口内联 + 路由分包 immutable 缓存」，而不是现在就提前优化。

## 迁移进度

**13 个页面已全部迁移完成**，`web/` 下的原生实现不再有对应页面。

| 页面 | 路由 | 页签 |
|---|---|---|
| 登录 | `/login` | — |
| 总览面板 | `/` | — |
| 订阅管理 | `/subscriptions` | — |
| 账号管理 | `/accounts` | — |
| 账号同步 | `/sync` | — |
| 自动整理 | `/organize` | 9 |
| 上传下载 | `/upload-download` | 2 |
| 影视转存 | `/media-transfer` | 4 |
| 系统配置 | `/settings` | 4 |
| 消息配置 | `/message` | 5 |
| 扩展功能 | `/plugins` | — |
| 实时日志 | `/logs` | — |

页签状态同步到地址栏 `?tab=`，刷新与收藏都能回到原页签（旧版页签只存在内存里）。

### 迁移中修掉的旧版缺陷

- **「开始增量同步」按钮是坏的**：`app.js` 调用了从未定义的 `startIncrementalSync()`，
  点击只抛 `ReferenceError`，`/sync/incremental` 在整个旧前端里没有任何引用 ——
  增量同步实际只能靠 cron 触发。新版按后端接口接上了。
- **115 目录 cid 与路径的失配**：旧版把 cid 存在 DOM `dataset` 上，靠「dataset.path 是否
  等于当前输入值」判断可信度。新版建模成 `{ cid, path }` 值对象，路径一变 cid 立即作废，
  提交前统一 `ensureCid()` 校验，避免拿旧 cid 搬错整个目录。

## 与旧前端的关系

**Go 默认服务 `webui/dist/index.html`。** 旧版 `web/` 已停用但保留在仓库里，
新前端出问题时可以立刻切回去对照：

```bash
WEBUI=legacy ./strmhub        # PowerShell: $env:WEBUI="legacy"; .\strmhub.exe
```

`webui/dist/index.html` 不存在时（没跑 `npm run build`）Go 会打一行日志自动回退旧前端，
不会启动失败。旧前端不再接收任何新功能；确认新版稳定后可整体删除，清单见 AGENTS.md §8。

## 密钥字段必须用 SecretInput

浏览器会把「文本框 + 密码框」的组合当成登录表单，把**本站保存的管理员密码**
自动填进 Emby API 密钥、企业微信 Secret 这类字段里，用户一保存就写错了配置。

所以所有 API Key / Secret / token / 第三方站点密码都走
[`SecretInput`](src/components/ui/SecretInput.vue)，配对的文本框加
`:input-props="plainProps('字段名')"`。成因与具体手法见
[`utils/autofill.ts`](src/utils/autofill.ts)。

例外只有登录页 —— 那里恰恰需要自动填充。
