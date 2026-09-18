# AGENTS.md — 115-Station 项目总览

面向 AI 编码助手与新加入的开发者。阅读本文即可掌握项目定位、目录结构、关键约定与雷区。
用户向文档见 [README.md](README.md) 与 [USAGE.md](USAGE.md)。

---

## 1. 这是什么

**115-Station** 是一个 Go 单体服务：把 115 网盘的媒体库映射成本地 STRM 文件供 Emby/Jellyfin 刮削入库，
播放时以 302 重定向让播放器直连 115（服务器不转发流量），并在同一个 Web 后台里完成
同步 / 整理 / 洗版 / 重命名 / 元数据回传 / 消息机器人的闭环。

**本仓库是 [DaisyYijin/STRMhub](https://github.com/DaisyYijin/STRMhub) 的二次开发版本。**
功能性改动包括：**移除 123 云盘、夸克网盘、阿里云盘支持**，只保留 115 链路；**移除 MetaTube、成人影片番号识别及其专属分类、刮削、重命名和通知功能**（AV1、AVC 等普通视频编码支持保留）；**移除播放账号功能**（小号播放 / 多端播放 / 账号池），播放统一走主号直链。

### ⚠️ 许可证约束（改动前必读）

上游仓库**没有 LICENSE 文件**，按 GitHub ToS 与著作权法通行规则默认为「保留所有权利」。因此：

- **不要**给本仓库添加 LICENSE 文件、SPDX 头或任何开源授权声明；
- **不要**在文档里声称本项目是 MIT / Apache / GPL 等许可；
- **不要**建议发布预构建二进制或公共镜像；
- README 的「许可证与再分发声明」章节是刻意这样写的，修改前先与维护者确认。

---

## 2. 技术栈

| 层 | 选型 |
|---|---|
| 语言 | Go 1.25（`go.mod` module 名仍是 `strmhub`，二进制名也是 `strmhub`） |
| Web 框架 | Gin（`gin.New()`，**不是** `gin.Default()`） |
| ORM / DB | GORM + SQLite（纯 Go 驱动 `glebarez/sqlite`，`CGO_ENABLED=0`） |
| 认证 | JWT（`golang-jwt/v5`）+ 环境变量管理员账号 |
| 115 客户端 | `SheltonZhu/115driver`（Cookie 通道）+ 自研 OpenAPI 客户端 |
| 前端（现役） | Vue 3 + TypeScript + Vite + Naive UI（`webui/`），详见 [webui/README.md](webui/README.md) |
| 前端（已停用·保留备查） | 原生 HTML/CSS/JS（`web/`），`WEBUI=legacy` 可切回 |
| 外部依赖 | ffmpeg/ffprobe（镜像内）、可选 Emby/Jellyfin |

---

## 3. 目录结构

```
.
├── main.go                     # 启动、日志轮转、Gin 装配、TLS 明文自动跳转、优雅退出
├── internal/
│   ├── api/                    # 全部业务逻辑（~33k 行，41 个测试文件）
│   ├── config/                 # 环境变量配置、配置文件读写、TLS 自签证书
│   └── model/                  # GORM 实体与建表/默认数据初始化
├── webui/                      # 管理后台前端·现役（Vue3 + TS + Vite + Naive UI）
├── web/                        # 管理后台前端·旧版，已停用，保留供对照实现（WEBUI=legacy 可切回）
├── wiki/index.html             # 完整版使用 Wiki（单文件）
├── strmhub-proposal/           # 方案设计文档（单文件 HTML + 内嵌 echarts/mermaid）
├── .github/workflows/docker.yml# CI：测试门禁 → 多架构镜像构建
├── Dockerfile                  # 多阶段交叉编译 → alpine + ffmpeg
└── docker-compose.yml
```

### `internal/api/` 模块地图

文件很多但命名规律清晰，按职责分组：

| 分组 | 文件 | 说明 |
|---|---|---|
| **路由与认证** | `routes.go` | `Handler{DB, Config}` + 全部路由注册 + 登录防爆破 + 备份/日志接口 |
| **115 基础设施** | `115.go` `115crypto.go` `http115.go` `open115.go` `files115.go` `ops115.go` `dir.go` `ratelimit.go` | Cookie 通道、ECC 加密、专用 HTTP 客户端（处理缺 SAN 证书）、OpenAPI（PKCE + 刷新）、文件/目录操作、**全局节流器** |
| **同步** | `full115.go` `incr115.go` `share.go` `upload115.go` | 全量 / 增量（生活事件）/ 分享转存 / 上传与监控回传 |
| **整理流水线** | `organize.go` `org115.go` `resource.go` `rename.go` `wash.go` `enrich.go` `scrape.go` `tmdb.go` | 识别 → 分类 → 洗版 → 重命名 → 搬移；`resource.go` 是文件名结构化解析的核心 |
| **播放链路** | `proxy.go` `offlineplay.go` `embyproxy.go` `embylibrary.go` `emby_notify.go` | 302 代理、边下边播、Emby 反代与建库 |
| **资源站** | `guanying.go` `pansou.go` `mukaku.go` `re0.go` `tgsearch.go` `tgsub.go` | 四个转存页签 + TG 抓取与关键词订阅 |
| **通知** | `notify.go` `notify_extra.go` `medianotify.go` `wecombot*.go` `wecomcrypto.go` | 企微双向机器人（AES 验签）、TG / 飞书 / OneBot / QQ 官方、入库通知防抖聚合 |
| **其他** | `dashboard.go` `cron.go` `offline.go` `covergen.go` `checkin115.go` `selfupdate.go` | 仪表盘、定时任务、离线下载、媒体库封面生成、115 签到、容器内自更新 |

### 数据模型（`internal/model/model.go`）

15 个实体，关键的几个：`Storage`（网盘账号凭据）、`StrmFile`、`SyncTask` / `SyncEvent` / `SyncedFile`（同步台账）、
`CategoryRule` / `WashRule` / `ScrapeRule`（YAML 规则）、`Setting`（键值配置）、`MediaEnrich`（ffprobe 结果）、
`MediaLibrary`、`UploadMark`。

---

## 4. 构建与测试

```bash
go build ./... && go vet ./... && go test ./... -count=1
```

CI（`.github/workflows/docker.yml`）的 `test` job 门禁就是这三条，过了才进 `docker` job 出镜像。

本地 Docker 构建：

```bash
docker build -t 115-station:local .
```

CI 行为：push 到 `master` 或打 `v*` tag 时触发（PR 只跑测试与构建，不推送），
产出多架构镜像 `ghcr.io/<owner>/115-station`（amd64 + arm64）。

> **推镜像前需要确认三件事**：
> 1. 仓库 Secrets 里配好 `CR_TOKEN`（有 `write:packages` 权限的 PAT），否则登录 ghcr 会失败；
> 2. `latest` 标签由 `enable={{is_default_branch}}` 控制——需要把 GitHub 仓库的**默认分支设为 `master`**，
>    否则推 master 只会打出 `master` 标签而没有 `latest`；
> 3. 先读 §1 的许可证约束：上游未授权前，ghcr 包应保持 private，不要公开分发。

测试集中在 `internal/api/*_test.go`（41 个文件），全部是纯单元测试，不需要网络或 115 账号。
测试数据在 `internal/api/testdata/`。改动识别 / 重命名 / 解析逻辑时，**务必先跑一遍对应测试**——
这部分逻辑边界条件极多（剧集区间、括号嵌套、拼音、洗版匹配等），测试是唯一的安全网。

---

## 5. 代码约定

- **注释与日志一律中文**，且注释解释「为什么」而不是「做什么」；现有注释里包含大量踩坑记录，改动时不要删。
- 日志前缀风格：`[模块] ✓/✗/○ 消息`，例如 `[TLS] ✓ HTTPS 已启用`。
- Gin handler 挂在 `*Handler` 上（`h.DB` / `h.Config`），注册集中在 `routes.go` 的 `SetupRoutes`。
- 路由分三档：`/api/auth/*`（公开）、`/api/*`（JWT 保护的 `protected` 组）、若干无鉴权但带 token 校验的回调端点（Emby webhook、OAuth callback、302 直链）。
- 配置读取顺序：**数据库 `Setting` 表优先，环境变量兜底**（如 115 请求间隔）。
- **前端已重写完成**（见 §8）。改页面一律改 `webui/`；`web/` 是停用的旧实现，只作对照，**不要再往里加功能**。
- 前端有构建：`cd webui && npm run typecheck && npm run build`，产物是单个 `webui/dist/index.html`（不提交进仓库）。
- 颜色只能从 `webui/src/styles/main.css` 的设计令牌取，组件里写死色值必然漏暗色模式。
- **API Key / Secret / token 一律用 `SecretInput` 组件**，不要直接写 `<NInput type="password">`——
  浏览器会把本站保存的管理员密码自动填进去（见 `webui/src/utils/autofill.ts`）。

---

## 6. 雷区

1. **115 风控**：所有对 `webapi.115.com` / `proapi.115.com` 的请求必须经过 `throttle115()`。
   读接口默认 1s 间隔、写接口 ≥3s。新增 115 调用时别绕过节流器。
2. **不要恢复被移除的网盘**：`removedCloudResource()`（`internal/api/pansou.go`）是本 fork 的核心改动点，
   按 `cloud_type` 标签 + 链接域名双重判定过滤 123 / 夸克 / 阿里。新增资源站接入时记得接上这个过滤。
   `upload115.go` 里的「阿里云 OSS」是 115 上传协议本身，**不是**阿里云盘，不要误删。
3. **优雅退出**：SIGTERM 后需要约 3 秒收尾（停 worker、冲刷通知队列）。直接杀进程会留下
   「115 已搬移、台账未写」的中间态，破坏后续去重与洗版判定。compose 的 `stop_grace_period` 必须 ≥ 该值。
4. **反代头不可信**：`r.SetTrustedProxies(nil)` 是有意为之——信任 XFF 会让登录防爆破被轮换头部绕过。
   若确实要部署在反代之后，显式把反代 IP 加进白名单，不要改回信任全部。
5. **凭据不入库**：`.gitignore` 已排除 `.localtest/`、`data/`、`*.db`、`*.log`。
   任何 115 Cookie、TMDB key、机器人密钥都只能落在 `/config` 或数据库，不进源码。
6. **别用 `gin.Default()`**：它自带的访问日志会让每个 HTTP 请求刷一行，实时日志页会被淹没。
7. **自更新路径**：`selfupdate.go` + `main.go` 的 `update-finish` 子命令依赖挂载 Docker socket。
   主容器不能停自己，收尾必须由独立进程完成——改动这条链路前先读懂两处注释。

---

## 7. 常见任务入口

| 我要… | 从哪看起 |
|---|---|
| 加一个 API 接口 | `internal/api/routes.go` 的 `SetupRoutes` |
| 加一张表 | `internal/model/model.go` + `InitDB` 的 AutoMigrate |
| 加一个环境变量 | `internal/config/config.go` 的 `Load()` |
| 改文件名识别/解析 | `internal/api/resource.go`，配套测试 `recognize_test.go` / `paren_test.go` / `eprange_test.go` |
| 改重命名模板变量 | `internal/api/rename.go`（变量体系与 CMS 对齐） |
| 改洗版规则 | `internal/api/wash.go` + `model.InitDefaultWashRules` |
| 接一个新资源站 | 照 `re0.go` 或 `mukaku.go` 的结构写，前端在 `index.html` 的 `mt-*` 页签 |
| 加一个通知通道 | `internal/api/notify_extra.go` |
| 改前端页面 | `webui/src/pages/` 下对应的页面组件；路由表在 `webui/src/router/index.ts` |
| 想知道旧版某功能怎么做的 | `web/index.html` + `web/js/app.js`（停用但保留），对照后在 `webui/` 里实现 |

---

## 8. 前端（`webui/`）

`web/` 的原生实现已被 `webui/`（Vue 3 + TypeScript + Vite + Naive UI）**整体替换**，
动因是原生版本没有暗色模式、228 个内联 `onclick` 导致无法安全重构，且视觉停留在早期
企业后台风格。13 个页面全部迁移完成。

### 运行与构建

```bash
# 开发：Vite :5173，/api 代理到后端 :6060
cd webui && npm install && npm run dev
#   后端端口改过：BACKEND=http://127.0.0.1:xxxx npm run dev

# 构建：产物是单个 webui/dist/index.html
cd webui && npm run typecheck && npm run build
```

Go 默认服务 `webui/dist/index.html`；产物不存在时打日志自动回退旧前端，不会启动失败。

### 旧前端为什么还留着

`web/` 已停用但**刻意保留在仓库里**：新前端出问题时可以 `WEBUI=legacy` 立刻切回，
排查「旧版这里是怎么做的」也不必翻 git 历史。它不再接收任何新功能。

```bash
WEBUI=legacy ./strmhub     # PowerShell: $env:WEBUI="legacy"; .\strmhub.exe
```

确认新前端稳定后可以整体删除 `web/`、`main.go` 的 `inlinedIndexHTML` / `indexHTMLMarker`
与三个 `r.Static`，以及 Dockerfile 里那行 `COPY --from=builder /build/web ./web`。

### 三条不要动的约定

1. **路由路径**（`/sync`、`/plugins`、`/subscriptions` …）沿用旧版，用户可能已收藏，
   改成「更规整」的命名会直接 404。
2. **`api/client.ts` 的超时与重试策略**：默认 60s 超时 + GET 网络层失败重试一次，
   是旧版在跨境明文链路上踩出来的，注释里写了来由。
3. **单文件打包**（`vite-plugin-singlefile`）：一次请求拿完整个前端。这不是图省事，
   是旧版「启动时把 style.css 内联进 HTML」那套优化的替代品，见 webui/README.md。
