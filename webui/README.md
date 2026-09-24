# webui —— 115-Station 新版管理后台前端

Vue 3 + TypeScript + Vite 重写版，已整体替换旧版原生实现。
外观是 HeroUI v3 的样式（`@heroui/styles`），交互由 Reka UI 提供；2026-09 从 Naive UI 整体迁过来，Naive 已移除。

## 为什么是这套选型

| 选择 | 理由 |
|---|---|
| **Vue 3** 而非 React | 后台是 12 页、28 页签的重表单应用，`v-model` 相比受控组件能省掉大量样板 |
| **HeroUI v3 样式 + Reka UI** | HeroUI 只有 React 版，但它的外观全在不依赖 React 的 `@heroui/styles`（BEM 类 + Tailwind v4）里；交互（键盘、焦点、浮层定位）交给 Reka UI（Radix 的 Vue 版）。两者在 `components/hero/` 拼成我们自己的组件 |
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
├── components/     # 跨页面复用组件；ui/ 下是业务原子件，hero/ 下是 HeroUI 样式 + Reka 交互的基础组件
├── composables/    # useFeedback（全站轻提示 / 确认框，组件内外都能调）
├── layouts/        # AppLayout（侧栏 + 顶栏）、navItems（导航与图标）
├── pages/          # 每个路由一个页面组件
├── router/         # 路由表；路径沿用旧版，不要改
├── stores/         # Pinia：auth、theme
├── styles/main.css # 按需引入 HeroUI 组件 CSS + 令牌别名；hero-adapter.css 是 Reka→HeroUI 的状态桥
├── types/          # 与后端 JSON 一一对应的类型
└── utils/          # 格式化、媒体 URL 拼装
```

## 三条约定（破坏了会出问题）

### 1. 颜色只从设计令牌取

颜色的唯一来源是 HeroUI 主题变量（`--accent`、`--surface`、`--muted`…，明暗两套由它按
`html.dark` 切换）。`main.css` 的 `--c-*` 只是别名，老样式里还大量在用；新代码直接写 HeroUI 变量。
组件里写死 `#fff`、`#1D2129` 这类字面值，暗色模式必然漏。

圆角用 `--r-sm` / `--r-lg` / `--r-xl` / `--r-card`。`--radius-sm/lg/xl` 被 HeroUI 的 `@theme` 占用且刻度不同，别用。

Tailwind 的颜色名别和字号刻度撞名（`base` / `xs` / `sm` / `lg` …）：`--color-base` 会让 `text-base`
同时输出 color，HeroUI 的输入框靠 `text-base` 设字号，字色就被刷成了页面底色。

页面私有的类名别和 HeroUI 的组件块名撞（`.card` / `.chip` / `.tag` / `.input` / `.skeleton` / `.button` …），
也别和 Tailwind 工具类撞（`.outline` 会画出一圈黑框）：那些都是全局类，同名的私有样式会被叠上它们的外观。
加个页面前缀就好（`.plugin`、`.ro-chip`）。

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

**13 个页面已全部迁移完成**，旧版 `web/` 已删除，历史实现可查 Git 历史。

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

**Go 服务 `webui/dist/index.html`。** 旧版 `web/` 与回退入口已删除。
产物不可用时，Go 记录日志，页面返回 503 并提示执行 `cd webui && npm run build`。

## 密钥字段必须用 SecretInput

浏览器会把「文本框 + 密码框」的组合当成登录表单，把**本站保存的管理员密码**
自动填进 Emby API 密钥、企业微信 Secret 这类字段里，用户一保存就写错了配置。

所以所有 API Key / Secret / token / 第三方站点密码都走
[`SecretInput`](src/components/ui/SecretInput.vue)，配对的文本框加
`:input-props="plainProps('字段名')"`。成因与具体手法见
[`utils/autofill.ts`](src/utils/autofill.ts)。

例外只有登录页 —— 那里恰恰需要自动填充。
