# AGENTS.md — 115-Station 项目总览

面向 AI 编码助手与新加入的开发者。阅读本文即可掌握项目定位、目录结构、关键约定与雷区。
用户向文档见 [README.md](README.md) 与 [USAGE.md](USAGE.md)。
**动 115 接口前先看 `docs/115-station-notes/REFERENCES.md`**（仓库内，但 `/docs/` 已 gitignore，不会提交）—— 外部参考项目清单与已验证的接口事实。

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

### 不入库的维护者文档

`REFERENCES.md`（外部参考项目与 115 接口事实）和 `INCR-SYNC-UPGRADE.md`
（增量同步改造记录）放在 `docs/115-station-notes/`，整个 `/docs/` 目录被 `.gitignore`
排除，刻意不提交：它们含本机绝对路径、逆向结论与内部开发流水，对使用者无意义。
本文里引用到它们的地方都指的是那个目录下的副本。

**怎么读**：直接读工作区里的文件，不需要联网——

```bash
cat docs/115-station-notes/REFERENCES.md        # 外部参考项目清单：哪个项目解决哪类问题
cat docs/115-station-notes/INCR-SYNC-UPGRADE.md # 增量同步改造全过程
```

`REFERENCES.md` 顶部写着参考项目在本机的位置（目前是 `D:\Code\115strm\` 下的
`p115client-main` / `115driver-main` 等），115 接口的字段含义、错误码、调用形态到那里
`grep` 最快。**读它们、不要抄它们**——理由见上面的许可证约束，用到某个做法时在代码注释里写明出处。
如果那些副本不在本机，按 `REFERENCES.md` 里的项目名去上游仓库看同名文件。

但**第三方组件的许可证义务是独立的**，不受上述限制，也不要删：

- [`THIRD-PARTY-NOTICES.md`](THIRD-PARTY-NOTICES.md) 与 [`licenses/`](licenses/) 目录
  是嵌入字体（OFL 1.1）、CodeMirror / Mermaid（MIT，压缩时许可头被剥掉）、
  ECharts（Apache-2.0）要求随附的版权声明与许可证副本。新增任何 vendor 进来的
  第三方文件时，同步在这两处登记。
- `.github/workflows/docker.yml` 的 `PUBLISH` 开关控制是否推 ghcr，现在是 `true`：
  push master 与 `v*` tag 都会产出 `ghcr.io/pancras-loe/115-station`（amd64 + arm64）。
  README「许可证与再分发声明」一节已说明镜像同样适用上游未授权的状况。

---

## 2. 技术栈

| 层 | 选型 |
|---|---|
| 语言 | Go 1.25（module 名与二进制名都是 `115-station`） |
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
| **同步** | `full115.go` `incr115.go` `incrdeps.go` `life115.go` `panpath.go` `incrstatus.go` `share.go` `upload115.go` `orphan115.go` `cron.go` `suppress.go` | 全量 / 增量（生活事件，只管外部变更）/ 分享转存 / 上传与监控回传 / 失效 STRM 检测 / 调度 / 整理自产事件抑制。**增量这条链分了四层**：`life115.go` 拉事件（游标 + 405 降级 + 开关门禁）、`panpath.go` 解析 cid→路径（祖先链 + `PathCache` 缓存）、`incr115.go` 消费事件落盘、`incrstatus.go` 对外报状态；`incrdeps.go` 是它们之间的注入接口，主流程靠它才能整体单测 |
| **整理流水线** | `organize.go` `org115.go` `orgstrm.go` `orgrecord.go` `emptydir.go` `resource.go` `rename.go` `wash.go` `enrich.go` `scrape.go` `tmdb.go` `airecognize.go` | 识别 → 分类 → 洗版 → 重命名 → 搬移 → **写 STRM / 下附属 → 刮削 → 刷 Emby**（一条龙，见 §6.8）；`resource.go` 是文件名结构化解析的核心，`orgstrm.go` 是落盘出口，`orgrecord.go` 是整理记录与「重新整理」，`airecognize.go` 是 TMDB 全部搜索策略都落空后的 AI 兜底（OpenAI 协议，界面「AI 增强识别」） |
| **播放链路** | `proxy.go` `offlineplay.go` `embyproxy.go` `embylibrary.go` `emby_notify.go` | 302 代理、边下边播、Emby 反代与建库 |
| **资源站** | `guanying.go` `pansou.go` `mukaku.go` `re0.go` `tgsearch.go` `tgsub.go` | 四个转存页签 + TG 抓取与关键词订阅 |
| **通知** | `notify.go` `notify_extra.go` `medianotify.go` `wecombot*.go` `wecomcrypto.go` | 企微双向机器人（AES 验签）、TG / 飞书 / OneBot / QQ 官方、入库通知防抖聚合 |
| **其他** | `dashboard.go` `offline.go` `dllink.go` `covergen.go` `checkin115.go` | 仪表盘、离线下载、**下载记录**、媒体库封面生成、115 签到 |

### 数据模型（`internal/model/model.go`）

19 个实体，关键的几个：`Storage`（网盘账号凭据）、`StrmFile`、`SyncTask` / `SyncEvent` / `SyncedFile`（同步台账）、
`CategoryRule` / `WashRule` / `ScrapeRule`（YAML 规则）、`Setting`（键值配置）、`MediaEnrich`（ffprobe 结果）、
`MediaLibrary`、`UploadMark`、`OrganizeRecord`（整理流水，一次动作一条）、`EventSuppress`（整理自产事件抑制）、
`PathCache`（115 目录 id → 网盘绝对路径）、`DownloadLink`（下载记录，见下）。

> `MediaLibrary` 与 `OrganizeRecord` 不是一回事：前者「一部影视一条」（去重 upsert，仪表盘用），
> 后者「一次整理动作一条」且失败与未识别同样留痕（记录页与「重新整理」用）。

> `DownloadLink`（`dllink.go`）是下载记录：磁力/ed2k/HTTP 离线与 115 分享转存提交时落一行，
> 内容整理入库后由 `orgSink.note` 里的 `dlLinkClaim` 把识别结果（片名 / 年份 / TMDB id /
> 分类 / 落库目录 / 整理记录 id）回写到同一行。界面在「上传下载 → 下载记录」。
>
> ⚠️ **认领产物不许新增任何 115 请求**，只能用已经在手的数据：离线走监视器既有的 30 秒轮询
> （摘 `file_id` 与任务名），分享走 `/share/snap` 返回里的顶层条目名（转存后 115 保留原名）。
> 认领因此是两级的：fid 精确、名字兜底，都对不上就让那一行停在「未认领」，不要为了配上
> 去加一次列目录 —— 这条约束是需求方明确提的。
>
> **新增任何「提交链接 → 内容落进转存目录」的通道，记得一起 `dlLinkRecord`**，
> 否则那条路进来的内容在下载记录里永远只有链接没有片名。

> ⚠️ **`PathCache` 是有失效要求的缓存，不是普通的读缓存。** 目录被改名/移动/删除后
> 必须失效或重定位对应子树，否则「已搬进冗余的目录」会被永久算成还在媒体库里 ——
> 整理的保护子树守卫失效、增量给冗余内容生成 STRM。
> 钩子挂在 `pan115Ops` 的 `moveFiles` / `rename` / `renameBatch` / `deleteFiles` 上
> （`open115.go`，就在 `markSuppressed` 旁边）。**新增任何会改动网盘目录结构的写操作，
> 都要记得一起挂上** `forgetDirSubtree`。

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
产出多架构镜像 `ghcr.io/pancras-loe/115-station`（amd64 + arm64），由 `PUBLISH` 开关控制是否推送。

> **关于镜像发布的两个坑**：
> 1. 登录 ghcr 用的是内置 `secrets.GITHUB_TOKEN` + job 的 `packages: write`，**不要**换回自建 PAT
>    ——自定义 secret 会让别人 fork 后这个 job 必红；
> 2. `latest` 标签由 `enable={{is_default_branch}}` 控制，需要 GitHub 仓库的**默认分支是 `master`**，
>    否则推 master 只会打出 `master` 标签而没有 `latest`。
>
> ghcr 包首次推送默认是 **private**，要在 GitHub 的 Package settings 里手动改成 public，
> 否则用户 `docker pull` 会报 unauthorized。这一步只需做一次。

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
   另：代码里的 `orphan*`（`orphan115.go`、`detect_orphans`、`/sync/orphans`）在界面和日志里一律叫
   **「失效 STRM」**，改这块时别把两套词混进用户可见的文案。
7. **没有应用内自更新**：这条链路（`selfupdate.go`、`update-finish` 子命令、Docker socket、
   企微「更 新」菜单、前端更新弹窗）已整条删除。它依赖发布公共镜像，与本仓库的许可证立场
   冲突。用户更新走 `docker compose pull && docker compose up -d`，**不要再把它加回来**。
8. **整理与增量同步不再重叠**：整理是一条自带落盘的完整流水线（识别 → 搬移 → 写 STRM →
   刮削 → 刷 Emby），产物**不经过**生活事件。整理用的 `pan115Ops` 打开了 `suppress`，
   自己做的每一次 move/rename 都登记进 `EventSuppress`，绕回来时被增量同步 pop 掉跳过
   （对齐 p115strmhelper 的 `pantransfercacher`）。
   - 新增任何在整理链路里改网盘的代码，都要走 `ops.moveFiles` / `ops.rename` / `ops.renameBatch`，
     绕过它们就绕过了抑制登记，增量会把同一份变更再处理一遍。
   - 整理搬走文件后如果还删了本地旧产物（洗版让位、重新整理回滚），**必须自己删**：
     台账行一旦清掉、事件又被抑制，没有第二个人会来收拾（见 `wash.go` 的洗版替换分支）。
   - 增量同步现在只负责 115 端的外部变更：手机上传、离线下载、网页端删改。
   - 抑制标记**只查不删**（`peekSuppressed`），要等事件真的标成 `applied` 之后
     才由 `unmarkSuppressed` 批量清。增量遇到目录读不出来会整轮放弃重来，
     查时就消费的话下一轮没标记可命中，整理的产物会被当成外部变更处理掉。
9. **空目录清理会删网盘内容**（`emptydir.go`）：整理搬完文件后，源目录与重新整理前的
   旧标题目录都会被清掉。删除走 `/rb/delete`（进 115 回收站，可还原），但守卫一条都不能松：
   - 工作区根（媒体库/待整理/已存在/冗余/转存，见 `orgProtectedCids`）永不删；
   - **整棵子树没有任何文件**才删，只看直接子项会误判——待整理常见
     `片名/Season 01/*.mkv`，文件搬走后父目录里还挂着空的 `Season 01`；
   - 列目录失败（风控/目录已不存在）一律按「不删」处理；
   - 限深 `emptyDirMaxDepth`。
   守卫逻辑全部由 `emptydir_test.go` 用假目录树覆盖（判断错一次就是误删用户文件），
   改这块**先把测试跑绿**。
10. **深度删除会删网盘源文件**（`deepdel.go`）：这是第二条会真删用户网盘内容的链路，
    规划全文见 `docs/115-station-notes/DEEP-DELETE-PLAN.md`。它是「失效 STRM」的镜像
    ——本地 STRM 没了、网盘源文件还在，就把网盘那份也删掉（Emby 删片子会连带删本地
    STRM，但网盘不动，下次全量同步又生成回来）。删除同样走 `/rb/delete` 进回收站，
    守卫同样一条都不能松：
    - **默认关**，开启后默认「只标记」+ 默认预演，三道开关都要用户自己关掉；
    - **只删台账（`SyncedFile`）里有的 fid**，接口不收任何「要删哪些路径」的入参——
      收了就等于把下面所有守卫都绕过去了；
    - 媒体库根或任何一个库目录 `os.Stat` 失败（挂载掉线）→ **整轮放弃**，
      绝不按「读不到 = 文件没了」继续（`checkLibRoots`）；
    - **两轮确认**：首轮只写 `vanish_at`，下一轮仍缺失才进可删集合；
    - 自动模式超过条数/占比阈值 → 拒绝执行 + 告警（`deepDelOverLimit`）。
      手动确认不受阈值限制：用户已经看过预览清单了；
    - 已被判为失效 STRM（`orphan_at` 非空）的行不进候选——网盘那份本来就没了。
    扫描器与全量/增量共用 `fullSyncMu`：**全量同步会把被删的 STRM 重新生成回来**，
    不互斥的话判定就踩在半轮全量的中间状态上。守卫由 `deepdel_test.go` 覆盖，
    改这块同样**先把测试跑绿**。

    **Emby webhook 那条线（`deepdelemby.go`）只是加速通道，不是第二条删除逻辑。**
    它收到删除事件后只做两件事：`os.Stat` 确认本地文件真没了之后打 `vanish_at`、
    必要时触发一次扫描；删不删仍由 `runDeepDelete` + 上面那套守卫决定。
    原因是 `library.deleted` 的含义是「条目没了」而不是「用户要删它」——
    Emby 扫库发现文件不在也发它，挂载抖一下就能连发一整批。
    神医助手的 `deep.delete` 是用户显式点按钮触发的，**只有它**能跳过两轮确认之间的等待
    （立即 `runDeepDelScanNow`），挂载探针与阈值照旧。新增任何事件来源时守住这条线。
    （2026-09 实测：Emby 扫库清理失效条目**确实会发** `library.deleted`，
    原生事件流里「用户删的」和「文件不见了」分辨不出来。这不是假想风险。）

    第三个入口是整理记录页的「深度删除」（`DeepDeleteOrganizeRecord`）：
    用户指定一条记录、文件都还在，所以两轮确认与阈值不适用，但守卫 #2 照旧 ——
    记录里的 fid 只用来**查台账**，查不到的一概不删（`deepDelRowsForRecord`）。
    别把它和同一行里的 `DeleteOrganizeRecord` 搞混：后者只删记录、不动任何文件。

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
| 改整理记录页 | `webui/src/pages/organize/RecordsTab.vue` + `webui/src/components/organize/RedoDialog.vue`（TMDB 搜索复用 `/tmdb/search`）。**那一行上有两个删除按钮**：「深度删除」删网盘真文件，垃圾桶图标只删记录，改动时别把两者的文案/样式拉近 |
| 改 Strm 管理页（`/sync`） | `webui/src/pages/SyncPage.vue` 是页签容器，四个页签在 `webui/src/pages/strm/`（配置 / 全量 / 增量 / 深度删除） |
| 改同步定时 | `internal/api/cron.go`：三条线 —— 自动整理 cron（`incr.cron`）、增量独立轮询（`incr.interval_sec`，默认 30 秒）、全量 cron（服务于失效 STRM 检测）。三者共用 `fullSyncMu`，整理抢不到锁会置位 `organizeMissed` 稍后补跑。**`incr.cron` 与 `incr.interval_sec` 同一个 setting key，界面却分在两个页面上**（cron 在「自动整理 → 基础配置」，间隔在「Strm 管理 → 增量同步」）：历史上两件事绑在一条 cron 上，增量拆成独立轮询后 key 没动。前端两侧都要走 `webui/src/composables/incrSetting.ts` 的 `patchIncrCfg` 只改自己那个字段，整存整取会互相覆盖 |
| 改整理落盘 / 刮削触发 | `internal/api/orgstrm.go` 的 `orgSink`（`commit` / `flushScrape` / `flushRefresh`） |
| 改整理记录 / 重新整理 | `internal/api/orgrecord.go`；路径推导在纯函数 `planRedoLayout`、原地刷新判定在 `isInPlaceRedo`，配套测试 `orgrecord_test.go`。**改 `redoOrganize` 前先读它的步骤注释**：算布局 → 动网盘 → 删旧本地产物 → 落盘，这个顺序是有来由的，破坏性动作必须排在计算之后 |
| 改空目录清理 | `internal/api/emptydir.go` 的 `pruneEmptyDirTree` / `pruneOrMove`；守卫见 §6.8 |
| 改深度删除 | `internal/api/deepdel.go`：扫描在 `scanVanished`、执行在 `runDeepDelete`、守卫在 `checkLibRoots` / `deepDelOverLimit`；Emby 事件那条线在 `deepdelemby.go`。**先读 §6.10 再动**，配套测试 `deepdel_test.go` / `deepdelemby_test.go`；整体设计与 Emby 事件的真实载荷见 `docs/115-station-notes/DEEP-DELETE-PLAN.md` |
| 想知道旧版某功能怎么做的 | `web/index.html` + `web/js/app.js`（停用但保留），对照后在 `webui/` 里实现 |
| 查某个 115 接口怎么调 | `docs/115-station-notes/REFERENCES.md` 的「115 接口实现」，再到 `p115client/client.py` 或 `115driver/pkg/driver/` 里 grep |
| 做同步/整理类功能 | `docs/115-station-notes/REFERENCES.md` 的「STRM 同步类项目」，里面有五个项目的策略对比 |
| 动增量同步任何一环 | 先读 `docs/115-station-notes/INCR-SYNC-UPGRADE.md` —— 2026-09 那轮改造的完整记录：每处改动的原因、与其他项目的逐项对比、踩过的坑、当时验证到什么程度。`§0 速查` 里有文件职责表、新增配置项、以及「改造自己引入的两笔债」是怎么还的 |
| 增量同步没反应 / 要排查 | 界面「Strm 管理 → 增量同步 → 事件流状态」卡片（门禁、通道、游标、上一轮结果、积压量），或直接打 `GET /sync/incr-status`；「测试事件流」按钮是纯读探针，随便点 |

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
WEBUI=legacy ./115-station     # PowerShell: $env:WEBUI="legacy"; .\115-station.exe
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
