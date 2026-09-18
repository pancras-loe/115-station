# AGENTS.md — 115-Station 项目总览

面向 AI 编码助手与新加入的开发者。阅读本文即可掌握项目定位、目录结构、关键约定与雷区。
用户向文档见 [README.md](README.md) 与 [USAGE.md](USAGE.md)。

---

## 1. 这是什么

**115-Station** 是一个 Go 单体服务：把 115 网盘的媒体库映射成本地 STRM 文件供 Emby/Jellyfin 刮削入库，
播放时以 302 重定向让播放器直连 115（服务器不转发流量），并在同一个 Web 后台里完成
同步 / 整理 / 洗版 / 重命名 / 元数据回传 / 消息机器人的闭环。

**本仓库是 [DaisyYijin/STRMhub](https://github.com/DaisyYijin/STRMhub) 的二次开发版本。**
唯一的功能性改动是：**移除 123 云盘、夸克网盘、阿里云盘支持**，只保留 115 链路。

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
| TLS 指纹 | `bogdanfinn/tls-client`（部分站点反爬需要） |
| 前端 | 原生 HTML/CSS/JS，无构建步骤（`web/`），YAML 编辑器用 CodeMirror 5 |
| 外部依赖 | ffmpeg/ffprobe（镜像内）、可选 CloudDrive2（gRPC）、可选 Emby/Jellyfin |

---

## 3. 目录结构

```
.
├── main.go                     # 启动、日志轮转、Gin 装配、TLS 明文自动跳转、优雅退出
├── internal/
│   ├── api/                    # 全部业务逻辑（~33k 行，41 个测试文件）
│   ├── cd2/                    # CloudDrive2 gRPC 客户端 + 生成的 protobuf
│   ├── config/                 # 环境变量配置、配置文件读写、TLS 自签证书
│   └── model/                  # GORM 实体与建表/默认数据初始化
├── web/                        # 管理后台前端（index.html / css / js / vendor）
├── wiki/index.html             # 完整版使用 Wiki（单文件）
├── strmhub-proposal/           # 方案设计文档（单文件 HTML + 内嵌 echarts/mermaid）
├── .tools/protogen/            # 独立 module：生成 cd2 protobuf
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
| **整理流水线** | `organize.go` `org115.go` `resource.go` `rename.go` `wash.go` `enrich.go` `scrape.go` `tmdb.go` `metatube.go` | 识别 → 分类 → 洗版 → 重命名 → 搬移；`resource.go` 是文件名结构化解析的核心 |
| **播放链路** | `proxy.go` `playback.go` `offlineplay.go` `embyproxy.go` `embylibrary.go` `emby_notify.go` | 302 代理、播放账号池、边下边播、Emby 反代与建库 |
| **观影门户** | `portal.go` `portalemby.go` `portalstream.go` | 6688 端口独立门户 + ffmpeg remux → HLS |
| **资源站** | `guanying.go` `pansou.go` `mukaku.go` `re0.go` `tgsearch.go` `tgsub.go` | 四个转存页签 + TG 抓取与关键词订阅 |
| **CloudDrive2** | `cd2.go` `cd2org.go` | 跨网盘整理引擎（定位是整理，不是播放） |
| **通知** | `notify.go` `notify_extra.go` `medianotify.go` `wecombot*.go` `wecomcrypto.go` | 企微双向机器人（AES 验签）、TG / 飞书 / OneBot / QQ 官方、入库通知防抖聚合 |
| **其他** | `dashboard.go` `cron.go` `offline.go` `covergen.go` `checkin115.go` `selfupdate.go` | 仪表盘、定时任务、离线下载、媒体库封面生成、115 签到、容器内自更新 |

### 数据模型（`internal/model/model.go`）

20 个实体，关键的几个：`Storage`（网盘账号凭据）、`StrmFile`、`SyncTask` / `SyncEvent` / `SyncedFile`（同步台账）、
`CategoryRule` / `WashRule` / `ScrapeRule`（YAML 规则）、`Setting`（键值配置）、`MediaEnrich`（ffprobe 结果）、
`MediaLibrary`、`UploadMark`、`PlaybackAltFile` / `PlaybackDevice` / `PlaybackCopy`。

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
这部分逻辑边界条件极多（剧集区间、括号嵌套、番号、拼音、洗版匹配等），测试是唯一的安全网。

---

## 5. 代码约定

- **注释与日志一律中文**，且注释解释「为什么」而不是「做什么」；现有注释里包含大量踩坑记录，改动时不要删。
- 日志前缀风格：`[模块] ✓/✗/○ 消息`，例如 `[TLS] ✓ HTTPS 已启用`。
- Gin handler 挂在 `*Handler` 上（`h.DB` / `h.Config`），注册集中在 `routes.go` 的 `SetupRoutes`。
- 路由分三档：`/api/auth/*`（公开）、`/api/*`（JWT 保护的 `protected` 组）、若干无鉴权但带 token 校验的回调端点（Emby webhook、OAuth callback、302 直链）。
- 配置读取顺序：**数据库 `Setting` 表优先，环境变量兜底**（如 115 请求间隔）。
- 前端无构建：直接改 `web/js/app.js` / `web/css/style.css`；静态资源走协商缓存（`no-cache` + Last-Modified），不要再手工加 `?v=N`。
  例外：`web/index.html` 里的 `style.css` link 标签会在启动时被整体内联替换，改动该标签需同步更新 `main.go` 的 `indexHTMLMarker` 常量。

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
| 改前端页面 | `web/index.html`（`data-page` / `data-tab`）+ `web/js/app.js` |
