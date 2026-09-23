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
| **播放链路** | `proxy.go` `embyproxy.go` `embylibrary.go` `emby_notify.go` | 302 代理、Emby 反代与建库 |
| **资源站** | `guanying.go` `pansou.go` `mukaku.go` `re0.go` `tgsearch.go` `tgsub.go` | 四个转存页签 + TG 抓取与关键词订阅 |
| **通知** | `notify.go` `notify_extra.go` `medianotify.go` `wecombot*.go` `wecomcrypto.go` | 企微双向机器人（AES 验签）、TG / 飞书 / OneBot / QQ 官方、入库通知防抖聚合 |
| **其他** | `dashboard.go` `medialib.go` `offline.go` `dllink.go` `covergen.go` `checkin115.go` | 仪表盘、**媒体库台账校准**、离线下载、**下载记录**、媒体库封面生成、115 签到 |

### 数据模型（`internal/model/model.go`）

19 个实体，关键的几个：`Storage`（网盘账号凭据）、`StrmFile`、`SyncTask` / `SyncEvent` / `SyncedFile`（同步台账）、
`CategoryRule` / `ScrapeRule`（YAML 规则，洗版保存在 `type=wash_config`）、`Setting`（键值配置）、`MediaEnrich`（ffprobe 结果）、
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
- **前端已重写完成**（见 §8）。改页面一律改 `webui/`；旧前端已删除，历史实现可查 Git 历史。
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
     才由 `unmarkSuppressed` 批量清。增量遇到目录**暂时**读不出来会整轮放弃重来，
     查时就消费的话下一轮没标记可命中，整理的产物会被当成外部变更处理掉。
9. **空目录清理会删网盘内容**（`emptydir.go`）：整理搬完文件后，源目录与重新整理前的
   旧标题目录都会被清掉。删除走 `/rb/delete`（进 115 回收站，可还原），但守卫一条都不能松：
   - 工作区根（媒体库/待整理/已存在/冗余/转存，见 `orgProtectedCids`）永不删；
   - **整棵子树没有任何文件**才删，只看直接子项会误判——待整理常见
     `片名/Season 01/*.mkv`，文件搬走后父目录里还挂着空的 `Season 01`；
   - 列目录失败（风控/目录已不存在）一律按「不删」处理；
   - 限深 `emptyDirMaxDepth`；
   - **进子树之前先核验树根**（`prunableRoot`，`pruneOrMove` 也走它）：祖先链末元素
     必须还是它自己、必须是某个工作区根的真子孙、且不是网盘根或一级目录
     （后者抄 MoviePilot `delete_media_file` 的 `parts <= 2`）。核不准就既不删也不搬。
     2026-09-22 线上事故：重新整理拿着记录里**早已被删掉**的源目录 cid 去清理，
     115 对失效 cid 返回网盘根的内容，清理于是从用户整个网盘根开始爬。
   - 同一个坑的根治在**列目录那一层**：`assert115SameDir` 核对响应回的 `cid`/`path`
     末级与请求的 cid 是否一致，不一致返回 `errDirGone`
     （p115client `tool/fs_files.py` 同款守卫）。`errDirGone` 是**永久**失败，
     增量里要计 `DirsGone` 直接跳过，**不要**当成「下轮重来」的临时失败。
   守卫逻辑全部由 `emptydir_test.go` 用假目录树覆盖（判断错一次就是误删用户文件），
   改这块**先把测试跑绿**。
10. **深度删除会删网盘源文件**（`deepdel.go`、`deepdelemby.go`）：
    2026-09-21 按维护者要求改为事件触发，移除定时全库扫描、预演和待删清单执行；保留整理记录入口。
    - 默认关闭。事件只处理路径 / pickcode 命中的 `SyncedFile`，不得扩大成全库缺失扫描。
    - 原生 `library.deleted` 也可能来自扫库清理，不能证明用户主动删除。仅接受 Movie / Episode / Season / Series；Folder、库容器及空/未知类型一律拦截，通知仍保留。电影/单集精确匹配 STRM；剧/季目录须通过台账布局验证，不能无条件展开前缀。
    - 执行前实时查询 Emby `/Library/VirtualFolders` 的 Locations 并映射到本地，拒绝库根/祖先、已移除的库、查询失败与库目录不可访问；pickcode 命中也不得绕过。查询后再次检查开关和本地缺失，只收缩候选。单次数量/占比阈值保持移除，不恢复全库扫描。
    - 原生事件使用两次短间隔检查，神医 `deep.delete` 不等待后台轮询。事件先于本地删除时有一次短暂重查。
    - 与整理、全量、增量共用 `taskMu`（§6.12），事件等锁后重查，不可丢弃事件再指望定时扫描补上。
    - 旧 `vanish_at` 仅保留数据库兼容，不读写、不参与删除；旧模式、预演与 `max_batch` / `max_ratio` 配置不再生效。
    - **空目录只沿本次文件的父目录链往上清**：叶子目录（影片目录 / 季目录）走 `pruneEmptyDirTree`
      （整棵子树没有文件才删，顺带收掉空季目录），再往上的分类目录只接受「自己完全为空」，
      禁止对祖先递归 —— 那会遍历兄弟影片并删掉无关空目录。
    - 目录 cid 由 `resolveDeepDelDirCids` 从同步根**逐级按名字列目录**查出来，**不要再改回查 `PathCache`**：
      那张表只在解析生活事件祖先链时才写，全量同步建起来的媒体库目录从来没进去过，
      反查必然落空 —— 2026-09-21 之前网盘空目录一直没被清掉就是这个原因。
    - 所有删除仍走 `ops.deleteFiles`（回收站、节流、事件抑制、缓存失效），先删网盘成功再清台账。
    - 整理记录入口只删记录 fid 与台账的交集，不受事件开关、缺失复核和阈值限制；记录本身保留。
    - 测试见 `deepdel_event_test.go` / `deepdel_test.go` / `deepdelemby_test.go`；改动前先跑绿。

    历史设计与 Emby 真实载荷见 `docs/115-station-notes/DEEP-DELETE-PLAN.md`，其中旧定时扫描设计已被上述流程替代。

11. **数「有几部」时 Emby 的 `/Items` 必须带 `IncludeItemTypes`**（`dashboard.go` 的 `embyCountTypes`）：
    `/Items?ParentId=..&Recursive=true` 不带类型过滤时，`TotalRecordCount` 把子树里**所有**条目都算上——
    每部影片自己那层目录（`Type=Folder`）也是一条，「一部影片一个目录」的电影库正好翻倍
    （2026-09 的现场：面板显示 1243 部、实际 619 部）；剧集库更离谱，Series + Season + Episode 全进去。
    按库的 `CollectionType` 收敛到「一部 = 一条」的那个类型，数字才和 Emby 自己界面上的一致。
    - 库类型从 `/Library/MediaFolders` 的 `CollectionType` 取；为空（混合库）按 `Movie,Series` 算。
    - 顺手带 `IsVirtualItem=false`：剧集库里「已排播但没有文件」的占位集不该算进库存。
    - 另一半是**本地整理台账会虚高**：`MediaLibrary` 只在整理时写入，用户手工删本地 STRM、
      在 Emby 里解除媒体库目录关联、直接上网盘删片，三种都不会回头改它。
      所以面板上的电影/剧集数量 **Emby 可用时一律以 Emby 为准**，台账数字另走 `media.local_*` 并排显示；
      对不上就提示用户跑一次「校准台账」（`medialib.go`，拿本地 STRM 树当事实清幽灵行，
      只删 `MediaLibrary` 这一张表，不碰网盘/Emby/`SyncedFile`，且「一条都对不上」时拒绝执行）。

12. **任务互斥锁 `taskMu` 与增量的遍历范围**（`synclock.go`、`incr115.go`、`files115.go`）：
    增量同步、自动整理、全量、洗版、深删全部串行在 `taskMu` 上。2026-09-22 之前它是一把裸
    `sync.Mutex` + 各处 `TryLock`，配上「增量 30 秒一轮、而一轮增量可能递归遍历整棵分类树」，
    结果是整理被饿死（现场：转存完几小时不入库、手动点整理永远提示有任务在跑）。现在：
    - **等待方登记让路**（`Acquire`），**持有方主动收工**：增量在「逐条事件推导路径 / 零遍历落盘 /
      目录遍历」三段里都查 `taskMu.YieldRequested()`，有人排队就就地停下。没消费完的事件保持
      `pending`，下一轮原样重来（STRM upsert、删除幂等）。
    - 增量轮询用 `TryLockPolite`：**有人在排队就主动不抢**。少了这道礼让，30 秒一轮的增量会在
      整理刚放开锁的瞬间又抢回去，让路白做。
    - 抢不到锁的日志/报错一律走 `busyErr()` / `logBusy()`，必须说清**被谁占着、占了多久**。
    - **回退目录遍历分浅深**（`fallbackTarget`）：文件级事件只列父目录**这一层**（文件就在那一层），
      目录级事件才递归，且递归目标取**目录自己的 file_id**，不是父目录 cid。改造前一律深遍历父目录，
      于是「往 影视/剧集 丢了个文件」或「给分类目录改个名」都等于整个分类重扫，每列一次目录还要等 1 秒节流。
    - **跳过的目录要区分临时与永久**：只有「暂时读不到」（`DirsSkipped`）才阻止本轮消费；
      「不在媒体库内」「已被删除」「事件没带目录 id」是永久的，只记账。混在一起的后果是
      **一条永远处理不完的事件把整批事件钉死，每 30 秒重放一次整轮遍历**，直到 7 天后被
      `pruneSyncEvents` 强杀 —— 这正是用户看到「一直在轮询 / 全盘 strm 一直在重读」的原因。
    - 溯源日志见 `incrtrace.go`：每轮一个 `[同步#N]` 轮次号、一份「回退遍历哪些目录 ← 哪条事件带来的」
      清单、一行「本轮账单」，以及连续多轮不消费时的重放告警。状态页 `/sync/incr-status` 同步暴露
      `task_lock` 与 `stall`。
    - `writeStrm` 返回 `wrote bool`：跳过已存在 / 内容一致的重写**不算新增**。混着算的话，
      任何一次重复遍历都会报成满屏「新增视频 N 个」并连带触发 Emby 刷新。
    - 测试：`synclock_test.go`（锁与让路）、`walkctl_test.go`（深度上限与中断）、
      `incr_scope_test.go`（永久跳过不阻塞消费、浅/深遍历选型、让路不消费）、`strmwrite_test.go`。

13. **洗版：判定逐文件、执行必须整批**（`wash.go`、`organize.go` 的 `processDir`）：
    2026-09-22 之前每集各发一次 115 写请求（旧版让位一次、新版移「已存在」一次），
    153 集的动漫光这两件事就是十几分钟，而整理全程占着 `taskMu`（§6.12），
    增量同步每 30 秒来一次全被挡回去 —— 现场日志里「转存后自动整理未开始」刷了两分半。
    - `decideWash` **只读台账**，不发任何 115 请求、不动本地文件，返回 `washPlan`；
      `applyWashPlans` 收一批 plan，按去向目录分组后每组**一次** move / 整批**一次** delete。
      新增洗版分支时判定和执行要各放各的地方，别又在判定里插一次网盘写。
    - `washScanner` 缓存库名前缀与每个目标目录的台账行。缓存成立的前提就是
      「判定期间没有任何写入」，把执行挪回判定里这份缓存立刻失真。
    - 判为「已存在」的新文件同样攒成一批搬一次，**整理记录也只写一条**（`SourceKind=dir`、
      `VideoCount` 记集数）。一集一条的话记录页会被一部剧刷掉好几页。
    - **sha1 去重不许再抢在洗版前面**。此前 `processDir` 里一句全表
      `sha1ExistsInLibrary` 命中就短路，既不打日志也不看策略：用户配着
      `mode: replace` 只看到「库内已有相同或更优版本」，无从解释（现场：龙珠 153 集）。
      现在并进 `decideWash`，分三种情况——同一份文件**就在本次目标目录下** → `washSameFile`
      （真的没什么可洗的）；在库内**别的位置**（全量同步带进来的旧目录结构）→ 按策略让位，
      replace 就是让规范路径上的新副本接管；**没配策略** → `washNoStrategy` 退回纯去重。
      `orphan_at` 非空的台账行一律不参与去重，否则一行陈旧记录能把一部片永久钉死在「已存在」。
    - 两种「已存在」的文案必须分开（`washExistsMsg`）：策略判输了和撞了 sha1 是两回事。
    - 测试：`washbatch_test.go`（批量、分组、去重、缓存、失效行）、
      `wash_regression_test.go` / `washflow_test.go` / `wash_recycle_test.go`。

14. **人工确认：停在识别之后、任何网盘写之前**（`orgconfirm.go`，开关是 `org-basic.manual_confirm`）：
    打开后 `processDir` / `processSingleFile` 识别完（含没识别出来的）只登记一条
    `status=awaiting` 的整理记录，文件原地留在待整理/转存目录，不搬、不改名、不查重、不洗版。
    - 确认入库**复用** `processDir` / `processSingleFile` 本体（`orgCtx.forced` 跳过识别），
      不要另写一条入库路径——洗版、去重、补全、落盘、刮削必须和自动整理同一套。
    - 结果写回原来那条待确认记录（`orgSink.reuse`），不另起一行。
    - 后续每轮整理按 fid 跳过待确认条目（`loadAwaiting` / `dropHeld`），散文件的待确认记录
      挂着同前缀的其他集，这些 fid 同样算；转存守望者用 `countUnheld` 判断目录是否还有活，
      否则只剩待确认条目时会被当成「整理后仍未清空」反复重试直到熔断。
    - 开关关掉后，下一轮自动整理接手这些条目（`adoptAwaiting`），结果同样写回原记录。
    - 新增识别之后的分支时，记得在 `ctx.holdable()` 为真时先停下，别在停之前发网盘写请求。
    - 测试：`orgconfirm_test.go`。

---

## 7. 常见任务入口

| 我要… | 从哪看起 |
|---|---|
| 加一个 API 接口 | `internal/api/routes.go` 的 `SetupRoutes` |
| 加一张表 | `internal/model/model.go` + `InitDB` 的 AutoMigrate |
| 加一个环境变量 | `internal/config/config.go` 的 `Load()` |
| 改文件名识别/解析 | 片名/年份/季集在 `tmdb.go` 的 `parseFileName`，画质串在 `resource.go`，TMDB 候选校验在 `tmdbmatch.go`。**搜索结果不许再「取第一条」**：每条候选都要过 `choose` 的片名校验（标题/原名相等 → 别名译名相等 → 包含且年份相符），对不上就判未识别。改动前后跑 `recognize_corpus_test.go`，语料在 `testdata/recognize_corpus.txt`（解析）与 `testdata/recognize_match.json`（假 TMDB + 期望条目），新踩到的坑样本往里加。文件名只有集号时季号是缺省的 1（`SeasonGuessed`），目录名上明写的季号要经 `applySeasonHint` 盖过它，新增按文件解析季号的地方记得一起调用（目录里的每一集统一走 `parseVideoInDir`：替换规则 → 所在子目录季号 → 条目季号）。路径上下文合并在 `recognizepath.go`；识别记忆在 `recogmemory.go`（`RecognizeMemory` 表，**只在人工改指定时写**，别改成自动识别也写，会把一次误识别固化下去）。其余测试 `recognize_test.go` / `paren_test.go` / `eprange_test.go` |
| 改重命名模板变量 | `internal/api/rename.go`（变量体系与 CMS 对齐） |
| 改洗版规则 | `internal/api/wash.go` + `model.InitDefaultWashConfig`；默认 YAML 首次写入，已有配置（含空配置）不覆盖，保存后立即失效缓存。**判定（`decideWash`）与执行（`applyWashPlans`）是分开的**，改之前先读 §6.13 |
| 接一个新资源站 | 照 `re0.go` 或 `mukaku.go` 的结构写，前端在 `index.html` 的 `mt-*` 页签 |
| 加一个通知通道 | `internal/api/notify_extra.go` |
| 改前端页面 | `webui/src/pages/` 下对应的页面组件；路由表在 `webui/src/router/index.ts` |
| 改总览面板 | `internal/api/dashboard.go`（数据）+ `webui/src/pages/DashboardPage.vue`（界面）。**Emby 计数别再改回不带 `IncludeItemTypes`**，见 §6.11；台账校准在 `internal/api/medialib.go` + `webui/src/components/dashboard/CalibrateModal.vue` |
| 改整理记录页 | `webui/src/pages/organize/RecordsTab.vue` + `webui/src/components/organize/RedoDialog.vue`（TMDB 搜索复用 `/tmdb/search`；`mode=confirm` 时用于待确认条目改指定）。筛选栏角标与页签角标共用 `recordStats.ts`。**那一行上有两个删除按钮**：「深度删除」删网盘真文件，垃圾桶图标只删记录，改动时别把两者的文案/样式拉近 |
| 改 Strm 管理页（`/sync`） | `webui/src/pages/SyncPage.vue` 是页签容器，四个页签在 `webui/src/pages/strm/`（配置 / 全量 / 增量 / 深度删除） |
| 改同步定时 | `internal/api/cron.go`：三条线 —— 自动整理 cron（`incr.cron`）、增量独立轮询（`incr.interval_sec`，默认 30 秒）、全量 cron（服务于失效 STRM 检测）。三者共用 `taskMu`（见 §6.12），整理抢不到锁会先登记让路、等一段，仍抢不到才置位 `organizeMissed` 每分钟补跑。**`incr.cron` 与 `incr.interval_sec` 同一个 setting key，界面却分在两个页面上**（cron 在「自动整理 → 基础配置」，间隔在「Strm 管理 → 增量同步」）：历史上两件事绑在一条 cron 上，增量拆成独立轮询后 key 没动。前端两侧都要走 `webui/src/composables/incrSetting.ts` 的 `patchIncrCfg` 只改自己那个字段，整存整取会互相覆盖 |
| 改整理落盘 / 刮削触发 | `internal/api/orgstrm.go` 的 `orgSink`（`commit` / `flushScrape` / `flushRefresh`） |
| 改整理记录 / 重新整理 | `internal/api/orgrecord.go`；路径推导在纯函数 `planRedoLayout`、原地刷新判定在 `isInPlaceRedo`，配套测试 `orgrecord_test.go`。**改 `redoOrganize` 前先读它的步骤注释**：算布局 → 动网盘 → 删旧本地产物 → 落盘，这个顺序是有来由的，破坏性动作必须排在计算之后 |
| 改空目录清理 | `internal/api/emptydir.go` 的 `pruneEmptyDirTree` / `pruneOrMove`；守卫见 §6.8 |
| 改深度删除 | `internal/api/deepdel.go`：执行在 `runDeepDelete`、事件范围在 `deepDelEventRows`、守卫在 `checkLibRoots`、网盘空目录在 `pruneDeepDelDirs`；Emby 事件那条线在 `deepdelemby.go`。**先读 §6.10 再动**，配套测试 `deepdel_test.go` / `deepdelemby_test.go`；整体设计与 Emby 事件的真实载荷见 `docs/115-station-notes/DEEP-DELETE-PLAN.md` |
| 想知道旧版某功能怎么做的 | 查 Git 历史中的 `web/`；现役实现在 `webui/` |
| 查某个 115 接口怎么调 | `docs/115-station-notes/REFERENCES.md` 的「115 接口实现」，再到 `p115client/client.py` 或 `115driver/pkg/driver/` 里 grep |
| 做同步/整理类功能 | `docs/115-station-notes/REFERENCES.md` 的「STRM 同步类项目」，里面有五个项目的策略对比 |
| 动增量同步任何一环 | 先读 `docs/115-station-notes/INCR-SYNC-UPGRADE.md` —— 2026-09 那轮改造的完整记录：每处改动的原因、与其他项目的逐项对比、踩过的坑、当时验证到什么程度。`§0 速查` 里有文件职责表、新增配置项、以及「改造自己引入的两笔债」是怎么还的 |
| 增量同步没反应 / 要排查 | 界面「Strm 管理 → 增量同步 → 事件流状态」卡片（门禁、通道、游标、上一轮结果、积压量、**当前任务锁**、**重放检测**），或直接打 `GET /sync/incr-status`；「测试事件流」按钮是纯读探针，随便点 |
| 「一直在轮询 / 反复扫同样的目录」 | 日志按轮次号 `[同步#N]` 对比相邻两轮：内容一样就是重放。看「本轮账单」那行的结尾（消费了没有、为什么没消费）与「回退遍历 N 个目录 ← 哪条事件带来的」清单。机制见 §6.12，代码在 `incrtrace.go` |
| 「整理/转存半天不动」 | `GET /sync/incr-status` 的 `task_lock`：谁占着、占了多久、谁在排队。日志里 `[定时] ○ 整理未开始：…` / `[整理] ○ 转存后自动整理未开始：…` 会写明被谁挡住。机制见 §6.12 |

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

Go 服务 `webui/dist/index.html`；产物不可用时记录日志，页面返回 503 并提示先构建前端。
旧版 `web/` 及回退入口已删除，历史实现可查 Git 历史。

### 三条不要动的约定

1. **路由路径**（`/sync`、`/plugins`、`/subscriptions` …）沿用旧版，用户可能已收藏，
   改成「更规整」的命名会直接 404。
2. **`api/client.ts` 的超时与重试策略**：默认 60s 超时 + GET 网络层失败重试一次，
   是旧版在跨境明文链路上踩出来的，注释里写了来由。
3. **单文件打包**（`vite-plugin-singlefile`）：一次请求拿完整个前端。这不是图省事，
   是旧版「启动时把 style.css 内联进 HTML」那套优化的替代品，见 webui/README.md。
