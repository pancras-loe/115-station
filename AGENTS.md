# AGENTS.md — 115-Station 项目总览

面向 AI 编码助手与新加入的开发者。阅读本文即可掌握项目定位、目录结构、关键约定与雷区。
用户向文档见 [README.md](README.md) 与 [USAGE.md](USAGE.md)。
**动 115 接口前先看 `docs/115-station-notes/REFERENCES.md`**（仓库内，但 `/docs/` 已 gitignore，不会提交）—— 外部参考项目清单与已验证的接口事实。
**动 Emby 接口前先看 `docs/emby-api/README.md`**（同样不入库）—— Emby 4.10 REST API 中文参考：按路径的接口总表、各服务参数表、数据结构，以及本项目踩过的坑。

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
| 前端（现役） | Vue 3 + TypeScript + Vite（`webui/`），外观走 HeroUI v3 的样式包 `@heroui/styles`，交互走 Reka UI，详见 [webui/README.md](webui/README.md) |
| 浏览器自动化 | `go-rod` 驱动 CloakBrowser（只给 RE0 网页登录用；浏览器本体运行时下载，不进镜像，见 §6.18） |
| 外部依赖 | 可选 Emby/Jellyfin |

---

## 3. 目录结构

```
.
├── main.go                     # 启动、日志轮转、Gin 装配、TLS 明文自动跳转、优雅退出
├── internal/
│   ├── api/                    # 全部业务逻辑（~33k 行，41 个测试文件）
│   ├── config/                 # 环境变量配置、配置文件读写、TLS 自签证书
│   └── model/                  # GORM 实体与建表/默认数据初始化
├── webui/                      # 管理后台前端·现役（Vue3 + TS + Vite + HeroUI 样式 + Reka UI）
├── wiki/index.html             # 完整版使用 Wiki（单文件）
├── .github/workflows/docker.yml# CI：测试门禁 → 多架构镜像构建
├── Dockerfile                  # 多阶段交叉编译 → debian-slim（带 Chromium 运行库，给 RE0 浏览器用）
└── docker-compose.yml
```

### `internal/api/` 模块地图

文件很多但命名规律清晰，按职责分组：

| 分组 | 文件 | 说明 |
|---|---|---|
| **路由与认证** | `routes.go` `authotp.go` | `Handler{DB, Config}` + 全部路由注册 + 登录防爆破 + 日志接口（备份在 `backup.go`）；`authotp.go` 是登录二步验证（TOTP，形态对齐 MoviePilot：密码对了才回 `otp_required`，验证码错计入防爆破，同一验证码不能用两次；密钥存 `auth.yaml` 的 `otp_secret`，环境变量同步密码时保留，`AUTH_OTP_RESET=true` 清掉）。登录令牌有效期同 MoviePilot 默认 8 天（`ACCESS_TOKEN_EXPIRE_MINUTES`） |
| **115 基础设施** | `115.go` `115crypto.go` `http115.go` `open115.go` `files115.go` `ops115.go` `dir.go` `ratelimit.go` | Cookie 通道、ECC 加密、专用 HTTP 客户端（处理缺 SAN 证书）、OpenAPI（PKCE + 刷新）、文件/目录操作、**全局节流器** |
| **同步** | `full115.go` `incr115.go` `incrdeps.go` `life115.go` `panpath.go` `incrstatus.go` `share.go` `upload115.go` `libupload.go` `orphan115.go` `cron.go` `suppress.go` | 全量 / 增量（生活事件，只管外部变更）/ 分享转存 / 上传与监控回传 / 失效 STRM 检测 / 调度 / 整理自产事件抑制。**增量这条链分了四层**：`life115.go` 拉事件（游标 + 405 降级 + 开关门禁）、`panpath.go` 解析 cid→路径（祖先链 + `PathCache` 缓存）、`incr115.go` 消费事件落盘、`incrstatus.go` 对外报状态；`incrdeps.go` 是它们之间的注入接口，主流程靠它才能整体单测 。**Strm 管理三个页签的记录区**（2026-10-23，参考 openStrm / p115strmhelper）：最近事件 `GET /sync/incr-events`（`increvents.go`，只读 `SyncEvent`，目录位置只查路径缓存；消费时把每条事件的去向写进 `SyncEvent.Note`：`self` 整理自产、`outside` 不涉及媒体库，界面默认藏 `self`）；最近轮次 `GET /sync/incr-rounds`（`incrstatus.go` 的 `roundHist`，内存里只留最近 50 轮有事的，空转只计数，重启清空）；深删记录接口带 `status` 筛选、近 30 天 `stats30`、每条的 `media_type` / `episodes`（`deepDelEpisodeLabel`）/ 本地还在的海报；全量各库结果直接读库表 `last_sync`，不另开接口。深删页的 Webhook 状态读 `GET /emby/webhook-status`（`embywebhookseen.go`：Emby 查不到它的 Webhook 配置，只能记本站收到过什么——任何事件 / 删除事件 / 测试事件 / 因 token 或格式被拒，存 setting `emby-webhook-seen`，「任何事件」10 分钟最多写一次）；「开始全量」的确认框 `components/strm/FullRunConfirm.vue` 列出这次扫的库、三组后缀、失效检测与刷新 Emby，后缀比上一次全量少了单独标出（上次的后缀在 setting `full_last_scan`，全量至少一个库成功时写）；还没跑过全量时页签把设置排到记录上面。**STRM 迁移**（扩展功能，2026-10-25 起两件事合在 `components/plugins/StrmMigrateModal.vue` 两个页签里；「Strm 管理 → 全量」的「接管已有 STRM」只是跳转 `/plugins?open=strm-migrate&tab=adopt`）。**接管其他项目**（`adopt.go` / `adoptparse.go`，2026-10-17，设计见 `docs/115-station-notes/ADOPT-STRM-PLAN.md`）：从别的项目换过来时认下本地已有的 STRM，路径和文件名不动、只换直链，Emby 条目与观看记录保留。`parseForeignStrm` 认 p115strmhelper / qmediasync / MediaSync115 / 本站 / OpenList / 挂载路径几种写法（按 pickcode → 文件 id → 网盘路径后缀对号），`planAdopt` 分桶（纯函数，同时给出要接管的 `adoptAction`）。预演 `POST /sync/adopt/preview`、执行 `POST /sync/adopt/apply`，都是 kind `adopt`（主队列、拿 `taskMu`），库清单按媒体库表（`adoptLibs`，附加账号的库用各自通道、文件带 `Acct`）。**执行不拿上一次预演的结果**，重新扫、重新对号，结论不是「可以接管」就不动；只碰「原样接管」「名字不同」两桶：内容整个换成 `strmURLFor` 拼的本站直链（还原修改时间，**不调 `markFreshStrm`**，否则下一次入库确认把整库排进探测）、登记台账，本地已有的附属文件一并登记，再补写两个 STRM 迁移的完成标记（否则下次启动 `MigrateStrmNames` 会把接管来的 `stem.mkv.strm` 改名）。「名字不同」的台账行记 `SyncedFile.AdoptName`（当时的网盘文件名），`strmNameFor` 在网盘没改名时沿用本地现有的名字，否则全量会在旁边按本站写法再写一份；**别把这个判断去掉**。没填直连域名不许执行（兜底的 Docker 回环会让上万个 STRM 一下子全播不了，`strmExplicitDomain`）；可选接管后排一次全量补写本地没有的。Emby 查询与播放都现读 STRM 内容（2026-10-17 实测），接管不用通知 Emby。前端 `components/strm/AdoptPanel.vue`。**还没用真实的别家 STRM 目录跑过执行**。**替换地址**（`strmrebase.go`，2026-10-24 起；2026-10-25 改成用户自己填「旧地址开头 → 新地址开头」）：起因是没填直连域名时写成了 Docker 回环 `172.17.0.1:6086`，上千个改不动；现在扫本地媒体根下**所有** `.strm`（`walkStrmFiles`，不按台账），本站与别的项目写的都能换（p115strmhelper 换了 IP、挂载点换了）。只换内容第一条有效行的开头：旧地址后面紧跟 `/ ? #` 或到结尾才算命中（`rebaseLine`，旧地址最长的那组优先，一条只换一次），别改成整条找子串。两侧只认 http(s) 地址或至少一级的绝对路径（`normRebasePairs`）。写回后还原修改时间（SmartStrm 踩过：改了 Emby 按加入时间的排序会乱），零 115 请求、不通知 Emby。预演 `POST /sync/strm-rebase/preview`（`pairs` 空时只给按地址开头的分组，本站格式的标 `station`），执行入主队列 kind `strmrebase`（拿 `taskMu`，同步也写这些文件；老任务的 `RebaseFrom` 换算成到直连域名的替换）。只换地址：改格式 / 保留后缀仍要覆盖模式全量（台账没存原文件名）。前端 `components/strm/StrmRebasePanel.vue`；「Strm 管理 → 配置」直连域名下面另有入口 `StrmRebaseDialog.vue`（同一个面板），保存时域名变了先读分组，有别的本站地址才弹出、预填成「旧 → 新域名」。参考 LitePan「一键替换播放基址」。测试 `strmrebase_test.go` / `adopt_test.go` |
| **整理流水线** | `organize.go` `org115.go` `orgstrm.go` `orgrecord.go` `emptydir.go` `resource.go` `rename.go` `wash.go` `scrape.go` `tmdb.go` `airecognize.go` | 识别 → 分类 → 洗版 → 重命名 → 搬移 → **写 STRM / 下附属 → 刮削 → 刷 Emby**（一条龙，见 §6.8）；`resource.go` 是文件名结构化解析的核心，`orgstrm.go` 是落盘出口，`orgrecord.go` 是整理记录与「重新整理」，`airecognize.go` 是模型接口与两个提示词（改写片名 / 从候选里挑），`airecogflow.go` 是 TMDB 全部搜索策略都落空后的 AI 这一环（改写 → 搜 → 挑、打分 `aiScore`、要不要停下 `aiHoldReason`；界面「AI 增强识别」） |
| **读取媒体信息** | `mediaprobe.go` + `internal/mediaprobe/` | 整理改名前按 115 直链读一点文件头，补全命名模板里文件名没写的画质变量（分辨率 / 特效 / 视频编码 / 音频编码 / 帧率，蓝光 ISO 还有资源质量，UHD 盘写 `UHD.BluRay`），2026-10-17，设计见 `docs/115-station-notes/MEDIA-PROBE-PLAN.md`。**纯 Go 解析、不用 ffmpeg**（镜像不带，§6.16）：`internal/mediaprobe` 不碰网络，读数据走 `io.ReaderAt` + 分块缓存（连续缺的几块合成一次 Range；单文件最多 24 次请求、8 MB，超了报 `ErrBudget`）；MKV 读 Tracks（不在头部按 SeekHead 跳），轨道名没写清楚的 DTS / TrueHD 再读第一个 Cluster 里的音频块；MP4 只走盒子头到 stsd / stts（moov 在尾部也一样，别整块读 moov）；TS / M2TS 读开头 6 MB：PAT / PMT → 按 PID 拼 PES，H.264 / HEVC 的 SPS（含 VUI）、HEVC 的 SEI（HDR10+、母版显示信息）与 RPU（NAL 62）、MPEG-2 / VC-1 序列头，没写帧率的按 PTS 间隔算，第二条 HEVC 流 / DOVI 描述符 = 杜比视界；蓝光 ISO 解 UDF（含 2.50 元数据分区）→ 读全部 .mpls 挑最长的 → 第一个片段的 STN 表，再按 STN 的 PID 读这个片段 m2ts 开头 2 MB 补声道与全景声；DVD ISO 按 VOB 总大小挑主标题集读 IFO。音频帧头（`audioframe.go`）：AC-3 / E-AC-3 的 acmod、TrueHD 主同步头（7.1 等布局；子流信息最高位 = 16 声道呈现 = 全景声）、DTS 核心 + 扩展子流（资源描述符里的总声道数；XLL 同步字 = HD MA，再带 02000850 = DTS:X，MediaInfo 同款认法）、蓝光 LPCM 头。E-AC-3 的全景声（JOC）只认 MP4 dec3 写明的，码流里的不认。ffprobemeta 插件的 `ffprobe bluray:` 只能开本地路径，**别照搬**。setting `media-probe`（默认关；`overwrite` 以读到的为准；`each_episode` 剧集逐集读，默认同一版本只读一集：`probeSession` 按「目录 + 文件名画质串 + 发布组 + 扩展名 + 体积 0.6–1.6 倍」分组，连续失败 3 次本轮停）。只在模板用到这些变量、文件名又没写全时读（`needProbe`），**放在识别与人工确认之后**（停下待确认的不读）、主视频在算目录名之前读（目录模板也可能用画质变量）。结果挂在 `ParsedName.Media`，`buildRenameContext` 叠加；结果按 sha1 存 `MediaProbe` 表，失败 7 天内不重试，重新整理（`redoPlanOpts.probe`）读缓存，指定季集的预览只查缓存（`cachedProbeHook`，纯判定不许发请求）。**洗版 / 同集多份排名看补全后的名字**（`probeJudgeName`：画质串插在结尾发布组前面）。**写进文件名的值必须能被 `ParseResourceInfo` 原样读回**（`TestProbeResourceRoundTrip`）：为此修了 DTS-HD.MA.7.1 被读成 DTS-HD、DTS.X 读成 DTS、帧率吞掉前面的声道（`7.1.24FPS`）、HLG 认不出几处，新增写法先过这个测试。前端 `components/organize/MediaProbeCard.vue`（「自动整理 → 重命名」）。测试 `internal/mediaprobe/probe_test.go` / `phase2_test.go`（夹具在测试里按规范拼，不带音视频数据；与解析器同一个人照规范写，**没有拿真实文件校验过**，碰到真实文件读错先补一个截断的真实样本）/ `mediaprobe_test.go` |
| **任务队列** | `taskqueue.go` `taskjobs.go` `taskjobsync.go` `taskstage.go` `taskprogress.go` `taskhistory.go` | **六条队列**：主队列（下面这些）串行在 `taskMu` 上；**刮削队列**只跑 `kind=scrape`（外加几十秒就扫完的 `metafill`）、**探测队列**只跑 `kind=probe`、**人物队列**只跑 `kind=person`（演职人员补全）、**订阅队列**只跑 `kind=subscribe`（资源订阅）、**秒传队列**只跑 `kind=ali2115`（阿里云盘秒传），都不拿 `taskMu`（见 §6.16）。进度按队列各存一份（`jobLane`，主队列沿用 `setJobProgress` 等包级函数，刮削显式用 `scrapeLane`），刮削还有第二级进度 `progress.sub`。**所有手动任务**（重新整理 / 确认入库 / 忽略 / 深度删除 / 手动整理 / 全量 / 手动增量 / 机器人「整理」「同步」）**入队立即返回（202）**；**后台任务**（定时整理 / 定时全量 / 转存与离线完成触发的 `transfer` / 转存守望者）也只入队，优先级 1（排队中手动优先，运行中不抢占），同类去重（`organize` / `full` / `transfer`），空转轮次（`jobOutcome.Idle`）跑完删行、同因重复失败只留最新一条。常驻 worker 串行执行（每个任务单独拿放 `taskMu`），`TaskJob` 表存状态与历史（仍不进队列的只剩增量轮询与 Emby 事件深删，后者在 `endTask` 时补一行 `kind=background`；取代原来内存里的 `recentRuns`）；结构化进度 `setJobProgress`（旧的 `SetTaskProgress` 同时写进当前任务）；前端是顶栏 `TaskQueuePanel.vue`（只看当前）+ `stores/queue.ts`，完整的历史、筛选与任务详情在**任务中心** `/tasks`（`taskhistory.go` 的 `GET /tasks/history` 与扩充后的 `GET /tasks/:id`；`OrganizeRecord.JobID` 记「最近一次处理它的任务」，任务与记录据此互相跳转，老记录为 0 不回填）。整理记录可先**暂存指定**（`OrganizeRecord.Pending*`，`PUT /organize/records/:id/pending`），勾选后 `POST /organize/records/submit` 统一入队（`taskstage.go` 的 `planSubmit` 决定怎么拆）。设计见 `docs/115-station-notes/TASK-QUEUE-PLAN.md` |
| **网盘文件页** | `filebrowser.go` `fileorganize.go` `filelibrary.go` `fileepisode.go` | 浏览 115 目录树（`GET /files/115`），每行「整理 / 移动」，勾选后批量整理 / 移动，都入任务队列（`orgpick` / `libredo` / `filemove`）。**每一行能做什么由后端列目录时算好**（`rowActionsOf`：前端带面包屑 `chain`，复用 `orgPickPrecheck` / `moveItemBlock` / `libTitleOf`，返回 `organize` / `move` / `title` / `block`），前端不许再自己抄视频后缀或片目判定（曾经两边不一致，按钮点下去 400）；做不了事的行不能勾选。行菜单只有整理 / 移动，片目另有「本地详情」「整理记录」（`?target_dir=`）两个跳转；新建文件夹、重命名、删除、「在 115 打开」维护者明确不要。**媒体库里只有片目目录（当前二级分类目录的下一层，`libCategoryLayout.isTitleRel`）能整理和移动**：整理不走新文件流水线（洗版会撞上自己），而是现场列文件建一条记录交给 `redoOrganize`（`libredo`）；移动只能到 冗余 / 已存在 / 转存/待整理，移出媒体库时 `cleanupMovedTitle` **先删台账再删本地、最后通知 Emby**（反过来深度删除会按台账把刚移走的网盘文件删掉），并清空记录的 `target_cid`（否则之后重新整理被判原地刷新）。整理复用 `processEntry`，媒体库内的条目拒收（走重新整理）。**刮削不在这一页**（2026-09 挪到海报墙）。**剧集片目里的视频能「指定季集」**（`fileepisode.go`，kind=`libepisode`，2026-10-04 起）：修个别集认错季集用（蜡笔小新第二季-720 被当分辨率进了 Season 0；整部重整修不好，兄弟集早已是规范名、`fillEpisodesFromSiblings` 找不到同模板的兄弟）。预填走 `guessEpisode`（认不出集号拿末段数字，标「推测」），判定 `planEpisodePicks` 只读台账：新名与落点、按洗版策略逐集判（勾选文件自己的台账行要排除，否则 sha1 查重让它给自己让位），同名冲突拒收；执行先 `applyWashPlans` / 判输的移「已存在」，其余交给 `redoOrganizeWith`（`redoOpts.episodes` 经 remap 钩子盖住解析、不参与连续编号换算），不另写入库路径。模板算出的片目目录与现状不一致时拒绝（不许借它换标题目录）。测试 `filebrowser_test.go` / `fileepisode_test.go` |
| **刮削方式** | `scrapeprovider.go` `libopts.go` | **每个库刮不刮**（2026-10-15 起，LIBRARY-ROLES-PLAN.md 阶段 2，取代原来的全局 `scrape.provider` 与「整理后 / 同步后自动刮削」两个开关）：库卡片上三选一，存 `lib_opts` 的 `scrape`：`auto` 自动刮新片（整理进来、增量同步进来的新片目都刮，定时补全也补它）/ `manual` 只手动刮（只在海报墙勾选后刮）/ `off` 不刮（交给 Emby，本站一样元数据都不写，海报墙按 Emby 分级）。没设过的库按老字段换算（`legacyLibScrape`：provider=emby → off；auto_after_organize / auto_after_sync / 定时补全开着补刮 → auto；否则 manual），老字段**只读不写**（`ScrapeSaveConfig` 原样保留存着的值），回退旧版本照旧能用；`POST /scrape/provider` 已删。判断一律经 `libScrapePolicy`（`loadLibScrapePolicy`，片目 key 第一段是库名，认不出按首个库）：整理后 `orgSink.autoScrapeJobsLocked` / `willScrape`、`enqueueSyncScrape`、媒体信息补全的补刮（只补 auto 的库）、`POST /local/scrape`（off 的库 409）、`execScrapeJob` 开头（off 的库的片目剔掉，全剔掉按空转；放在 `scrapeEmbyRefresh` 那个 defer 之后，整理交过来的 Emby 刷新照刷）、海报墙分级（`localTitleQuery.EmbyGrade` 按片目，卡片带 `emby_graded`）、演职人员 NFO 同步（off 的库不改）。**新增刮削入口一律先过 `libScrapePolicy`**。轨道探测与演职人员补全不归它管。`GET /scrape/config` 带 `libs`（各库方式）；`GET /scrape/emby-check` 每个 Emby 库带 `modes`（覆盖到的本站库各是什么方式，`embyFolderScrapeModes`），界面按它只报用得上的问题（`checkEmbyLib`，只读、不替用户改 Emby）。前端：改在库卡片（`LibraryCards.vue`，单库也显示），影视刮削页顶部 `components/scrape/ProviderCard.vue` 只剩 Emby 检查与一行跳转（各库摘要 2026-10-23 删了：看了想改还得再跳一次），模块级状态 `composables/scrapeProvider.ts`（`embyScrapes` = 全部库不刮、`anyEmby`、`anyAuto`）。测试 `scrapeprovider_test.go` / `libopts_test.go` |
| **海报墙** | `locallib.go` `localscrape.go` `localdetail.go` `localemby.go` | 本地媒体库的片目卡片墙（`GET /local/titles`）：一张卡片 = 台账里一个片目（`scanLedgerTitles`），哪一层是片目由 `libCategoryLayout` 定：只认二级分类规则算出的分类目录（规则外目录里的片不算片目，对账弹窗归进「分类规则外」）。别改成按目录结构自动推断片目层，平铺与多级分类混在一起时认不准；状态与详情抽屉同口径（`inspectLocalTitleDetail` + `grade`：片目级 / 季 / 每集 NFO、海报、背景图都在才算刮全，剧照 / 季海报只进 `Soft` 提示；背景图在刮削时确认 TMDB 上没有的（详情没给主背景、`images.backdrops` 兜底也挑不到）记进 `ScrapeArtGap`，同样降成 `Soft`，片目改指定了别的 TMDB 编号就不算数，下载失败不记，LitePan `no_backdrop` 同款），**零 115 请求**，列表 30 秒缓存（刮削任务结束时 `forgetLocalTitles`）。目录筛选（`?dir=`，2026-10-12）按片目 key 的整段前缀收窄，选项由 `localTitleDirs` 从同一份快照算出（片目所在目录连同各级上层，计数不套用其余筛选）。卡片上的「未探测 N」来自 `localemby.go`：**打开页面时**（`GET /local/titles/emby-stats`）分页读一次 Emby 全库媒体流、按路径归到片目，一分钟内复用，没有后台定时拉取。海报缩略图走公开路由 `GET /local/poster`（`<img>` 带不了登录态）：列表按 key 签 HMAC（JWT 密钥），`underRoot` 防 `../`，缩成 400px 宽 JPEG 放内存缓存。刮削 `POST /local/scrape` 入队（`scrape`，参数在 `jobParams.Local`），核心是 `scrape.go` 的 `scrapeTitleMeta` + `fileScrapeWriter`，产物写本地媒体库。「上传到网盘」只管这一次，按片目所在库的账号走 `uploadAs`（不看库的上传开关；别的账号的库用那个账号的 `opsFor` 通道，2026-10-24 起不再拒绝）；**没勾时不登记上传指纹**，传不传照常由监控上传决定。形态参考 LitePan 的海报墙（PolyForm Noncommercial，只看思路），但片目边界与类型取台账，不在目录里写标记文件。**平铺片目**（2026-10-12，`ledger.go` 的 `isSoloTitleKey`）：电影分类目录里直接放着的视频，一个算一部，片目 key 是这个 STRM 的相对路径（以 `.strm` 结尾），`e.dirKey()` 才是目录。产物一律按视频名（`<名>.nfo` / `<名>-poster.jpg`，刮削里 `scrapeTitle.Solo` 走多版本那套命名），分类目录里的 `poster.jpg` / `movie.nfo` 不读、不写、不收；查 Emby、交探测都用 STRM 本身（`scrapeTitle.localPath`），**别把分类目录交出去**，那是整个分类。新代码拿片目 key 当目录用之前先想平铺片目，按前缀查台账的也一样（`subHaveOf` 吃过一次）。剧集不平铺，整理也不往平铺目录放。测试 `flattitle_test.go`。**卡片只放要紧的，细节进详情抽屉**（`?title=` 打开）：「刮削文件」`GET /local/titles/detail` 列出每个产物在不在（片目级 / 季 / 每集，季目录判定复用 `scrapeSeasonDirs`，零 115 请求）；「媒体信息」`GET /local/titles/emby` 显示 Emby 的音视频字幕轨道与每个条目的提前探测状态（`embyProbeStateOf`，与 `embyExtractAllowed` 同口径，把「为什么没探」说出来）；`POST /local/titles/probe` 单片目提前探测，走同一条队列与记账，不开后门。**Emby 快照还带片目级条目**（电影的 Movie、剧集另读一遍 Series：`ItemID` / TMDB / IMDb / 有没有海报与背景图）：刮削方式为 Emby 时状态改按 Emby 分级（`gradeByEmby`：认出且有图 = ok；缺编号 / 缺图 / 认成的 TMDB 与台账不一致 = partial；Emby 里没有 = miss；快照没读到 = `pending`，不进三档计数），海报 / 背景图取 Emby 的（`emby_poster` / `emby_backdrop`，走 `/embyimg`）；本站刮削时 Emby 海报只补本地没有的。「让 Emby 刷新元数据」`POST /local/titles/emby-refresh`（`FullRefresh`，可选替换全部；和入库刷新 `embyRefreshItem` 的 `Default` 模式是两回事），多版本电影的几个 Movie 条目逐个刷。**与 Emby 对账**（`embyreconcile.go`，2026-10-10）：总览数量读 Emby 全服务器、海报墙数台账片目，两边对不上时 `GET /local/titles/reconcile` 把 Emby 的每个 Movie / Series 换回本地路径分桶（不在本站目录下 / Emby 残留 / 分类规则外 / 台账没有 / 类型不一致 / 一部拆成多条 / Emby 没有），各桶之和正好等于差值；路径映射一个都对不上时不分桶，改给 `suggestEmbyRoot` 推算的「Emby 媒体库目录」（`GET /emby/path-suggest`，Emby 设置页「自动检测」也用它），**只填表单不替用户保存**。路径映射 `path_mapping` 写作「本地#Emby」，两侧都由用户填（`embyPathRoots`，2026-10-10 起，p115strmhelper 同款）：本地一侧**空 = 跟随本地媒体库目录**、绝对路径照用、相对路径 = 根下子目录（v26.10.10-6 的存法）。STRM 第一层是 115 库目录名，Emby 直接从这一层挂进去时（Emby `/Movies` ⇄ 本地 `/Movies/资源库`）要填它。老版本保存时会把当时的本地根写进前半段、后端一直忽略，启动时 `MigrateEmbyLocalSide` 一次性清空，**别去掉这道迁移**（用户改过本地目录的话旧值是过时的）。推算在根下对不上时往下找一层台账里的库目录（`ledgerLibNames`）。前端 `components/local/ReconcileDialog.vue`，海报墙与总览各有入口。**剧集集数进度**（`localtvprog.go`，2026-10-20）：`GET /local/titles/tv-progress` 给剧集卡片的「12/16 集」与完结 / 连载中，有几集按台账认季集（`ledgerVideoEpisodes`，与订阅盘点同口径，第 0 季不算），播了几集读 TMDB `/tv/{id}` 存 `TmdbTVProgress`（连载 1 天、完结 30 天过期）；接口只回已有的，缺的后台逐部拉（间隔 300ms，出错停 10 分钟），**别改成请求里现查**。筛选另有一档「存疑」（`titleDoubtful`：没有 TMDB 编号或 Emby 认成别的编号，和刮削三档不互斥）。测试 `embyreconcile_test.go` / `locallib_test.go` / `localscrape_test.go` / `localdetail_test.go` / `localtvprog_test.go` |
| **播放链路** | `proxy.go` `embyproxy.go` `embylibrary.go` `emby_notify.go` `embyextract.go` | 302 代理、Emby 反代与建库、入库后让 Emby 提前探测媒体信息 |
| **影视转存**（界面叫「发现与订阅」，侧栏在「媒体库」组，2026-10-24 起） | `transferhub.go` `transfertg.go` `guanying.go` `pansou.go` `mukaku.go` `re0.go` `tgsearch.go` | 按影片聚合各资源站（2026-09-30 起）：选定 TMDB 条目后前端对每个来源各发一次 `GET /transfer/resources/:source`（慢的盘搜不拖住别的），后端统一成 `ResourceItem`：画质标签（`ParseResourceInfo`）、相关性（`resRelevant`：片名 / 原名 / TMDB 别名被标题包含，电影再看年份）、洗版偏好排名（`resWashRank`，命中洗版策略第几条规则）、提交过没有（`DownloadLink.Hash`，零 115 请求）。**其他网盘（百度 / 迅雷 / UC …，`Kind=pan`）在 `resNormalize` 里一律丢掉**（2026-10-14，维护者要求：本站转存不了，RE0 上非 115 的解锁了也没用），所有来源、机器人、订阅都经过这里。提交一律 `POST /transfer/submit` → `submitResource`，里面才调 `shareReceiveCore` / `offlineSubmitCore`（它们自己登记来源链接），**别另起提交通道**。把资源换成可用链接（直接贴的分流、观影取磁力、RE0 解锁）在 `resolveResourceLink`，网页提交与订阅共用；分享转存拆成 `shareList`（列一层）/ `shareWalk`（递归，订阅按集挑选用）/ `shareReceive`（转存任意层级的条目，`share.go`），`shareReceiveCore` 与订阅的 `shareReceivePicked` 都是它们的组合。RE0 有两条通道（`re0Channel`）：**网页登录**（2026-10-14 起，站方停止开放 OpenAPI 申请；`re0browser.go` 下载 / 启动 CloakBrowser，`re0web.go` 登录、签到、读 `/tmdb/{类型}/{编号}` 页面里的 Next.js 数据、解锁，见 §6.18）与已授权老用户继续用的官方 OpenAPI（已授权的优先）。OpenAPI 部分在 `re0.go`（按 2026-10 的新版文档：包括 ping 在内所有 `/api/open/*` 都要用户 Token；申请 `meta query unlock write` 四组权限，老授权缺 meta / write 时界面提示重新授权；`OPENAPI_REAUTH_REQUIRED` / `INVALID_OPENAPI_USER_TOKEN` 清掉 Token 不重试；只对有效 V 用户开放，403 时提示），每日签到在 `re0checkin.go`（08:00 后没签成就签，不开放会扣分的 `is_gambler`）。RE0 解锁花积分，只接受带 `confirm=true` 的请求；机器人的 `/search` 也列 RE0（2026-10-14 起，原来整个跳过，只开了 RE0 的用户什么都搜不到），免费 / 解锁过的直接解锁转存、自动择优也挑；要花积分的选中后先出确认屏（写明积分，TG 按钮 / 企微回 y 确认、c 取消，`botFlow.unlockAsk`），确认了才带 `confirm` 解锁，自动择优从不挑（`re0BotFree`）；订阅的自动解锁是用户在订阅配置里事先设了积分上限、由订阅自己核过上限与预算后才带 `confirm=true`。机器人找资源（TG 与企微共用 `botflow.go`：`botFind` / `botAct` 管会话，渲染成与通道无关的 `botView`，TG 编辑原消息、企微发文字）也走 `searchResources` / `botSubmitResource`：`/search` 全部来源服务端并发（`botResourcesAll`，40 秒不等、`resDedupe` 去重），`/gy` `/wp` 单来源；每页 8 条、`0` 自动择优（`bestIndex`）、提交后会话保留、同一条只提交一次（`botFlow.sent`）。观影（`guanying.go` 末段，2026-10-14 起）同不太灵是「先认站内影片再列资源」：`type=0` 搜影片 → `gyPickTitle` 按类型（电影 mv / 剧集 tv·ac）、片名、年份（差一年以内）挑一部 → `/res/downurl/{类型}/{id}` 读这部片的磁力（直接拼链接）与网盘清单（115 分享可直接转存）；**别改回 `type=4` 按关键词搜全站种子**，「希望」会把《马萨卡的孩子：舞出希望》搜进来，只在跳过 TMDB 时才退回它（参考 pansou 的 gying 插件）。第五个来源「TG 频道」（`transfertg.go`）复用 `tgsearch.go` 抓公开网页 `t.me/s/频道?q=`，频道清单在 setting `tgsearch` 的 `channels`，并发 3、最多 20 个频道，全部抓不到才报错。第六个来源「癫影」（`dian.go` / `dianweb.go`，2026-10-15，m.dian115.com、站名 MADOW）：同 RE0 要花积分解锁（`resPaidSource` / `resByTmdb` 两处认这两个来源，新增付费来源加在那里），资源列表按 TMDB 条目读 `resource-detail`（`dianResourceKey` 是站点前端的异或编码），不登录也能读；剧集一次给全部季，站点标的季集写在标题**最前面**（`[S01E01-E07]`，覆盖估计取最左边的匹配，分享标题常是第一个文件名）；分享自带文件清单（`ResourceItem.files`，不下发前端），订阅付费解锁前拿它当预览（`dianPreview`）；离线分享解锁出来可能是一批 ed2k（`resLink.More`），网页 / 机器人逐条提交；订阅（`tryOfflineBatch`，2026-10-22 起，原来停下推通知让用户手动提交、积分白花）把这批 ed2k 当成一层分享交给 `pickShareEpisodes` 只挑缺的集，几个任务进同一个包装目录、算这一轮的那一个离线，本月上限与 115 配额按任务数核（`offlineRoom`，每月记账读 `SubAttempt.Tasks`），秒传到了的留下、没到的删任务，`LinkID` 之外的来源链接记在 `SubAttempt.LinkIDs`、结算连它们一起查；有认不出文件名的链接（磁力）挑不了集，仍推通知交给用户。订阅的自动解锁上限与预算两个站各设一套（`dian_unlock_max` / `dian_daily_budget`，`subPointsSpentToday` 按 `SubAttempt.Source` 分开算）。对真站点的冒烟测试 `dian_live_test.go`（build tag `dianlive`，本机 Edge / Chrome）。四个站点文件只剩配置 / 登录与底层搜索函数；前端 `webui/src/pages/MediaTransferPage.vue`（2026-10-08 起与资源订阅合成一页：找资源 / 订阅 / 链接转存 / 资源来源 / 订阅设置五个页签，子页签在 `pages/transfer/`；两个设置页签共用 `SettingsTab.vue`（`part`），左边列小节、右边一次一张（`?sec=`：资源来源是 `folder` 转存目录与各来源 key，订阅设置是 `check` 检查计划 / `cond` 资源筛选 / `wash` 洗版 / `offline` 转存与离线 / `notify` 通知 / `limits` 节流与限额，顶上一行 `StatusStrip` 摘要，检查间隔给积极 / 标准 / 省着用三档预设、「标准」= 后端默认值），维护者要的是不用长滚动，别再改回整列卡片；订阅的自动解锁积分（2026-10-24 起）放在 RE0 / 癫影各自的来源设置里（`AutoUnlockRows.vue`），走只改这两项的 `POST /subscribe/config/unlock`，别改成整份提交订阅配置；旧 `?tab=gy`、`?tab=sources&focus=`、`?tab=settings&sec=` 由 `MediaTransferPage.vue` 带到对应页签与小节，`/upload-download?tab=download` 带到链接转存）。链接转存页一次可贴多个链接，按链接形态抓（`composables/transferLinks.ts`，ed2k 文件名可能带空格、115 分享文案一段话里带访问码），逐个 `POST /transfer/submit`；转存/待整理文件夹每个库各一个，在「账号与媒体库 → 媒体库」设；「设置」里只在有两个以上能转存的库时显示「默认转存库」（`FolderSettings.vue`，`?sec=folder`），改不了的设置不放（2026-10-23 删了共用转存目录与转存入口两张卡）。「转存后整理」开关已去掉：守望者每分钟接管转存目录，关了也会整理。测试 `transferhub_test.go`；TG 抓取在 `tgsearch.go`。找资源页没在搜东西时铺 TMDB 趋势 / 热门榜单（`tmdbdiscover.go` 的 `GET /tmdb/discover`，榜单清单在 `tmdbDiscoverLists`，半小时缓存、最多 10 页），点一部与搜索结果点一部同一条路。前端（2026-10-24 改版，照 Jellyseerr / MoviePilot 的「发现」首页）：`components/transfer/DiscoverHome.vue` = 顶部聚光灯横幅 `DiscoverHero.vue`（今日趋势里有背景图的前几部，搜索框 `SearchBar.vue` 嵌在横幅里）+ 每个榜单一行横向海报带 `DiscoverRow.vue`（滚到眼前才请求）；「查看全部」/「按地区 / 题材筛选」换成整页海报墙 `DiscoverPanel.vue`（能翻页）。海报卡片统一 `PosterCard.vue`（搜索结果 `CandidateGrid` 也用它）；候选带 `backdrop`（TMDB `backdrop_path`），选定影片后的头图也用它。榜单下面一行按 电影 / 剧集 + 地区 + 题材 筛选（`list=filter`，`discoverFilterList`：地区与题材只认 `tmdbDiscoverCountries` / `tmdbDiscoverGenres` 里的值，电影与剧集题材表不同）。整页海报墙另有排序（`sort=vote` 评分 / `date` 上映时间，`sortDiscoverPool`）：trending / popular 端点不收 `sort_by`，所以一律取榜单前 5 页去重后在本站重排、再按 20 条切页，评分人数不到 20 的垫底；**别改成 discover 全库按评分排**，出来的全是几票的冷门片、已经不是这个榜单了。海报右上角与选定影片后的「订阅」是**一键订阅**（2026-10-23，`POST /subscriptions` 带 `quick: true` → `subDefaultForm`，与机器人 `/sub` 同一套默认值），要改范围点旁边「自定义」开弹窗。**豆瓣榜单没接**：要逆向的 frodo 签名，条目也没有 TMDB 编号、得按片名反查，认错就把找资源与订阅挂到错的条目上。TG 关键词订阅（`tgsub.go`）已于 2026-10-07 删除：只按关键词匹配、不认片目、不挑资源，要做订阅按 TMDB 条目重做 |
| **阿里云盘秒传** | `ali2115.go` `aliyun.go` `alisign.go` | 扩展功能「阿里云盘秒传」（2026-10-24，设计见 `docs/115-station-notes/ALI2115-PLAN.md`，光鸭 / 123 只给 MD5、要整份下载算 SHA1，没做）：用户贴的阿里云盘分享按整文件 SHA1（`content_hash`）秒传进 115，**不经本站转发文件内容**，只在 115 要二次认证时按 Range 读源文件一小段（`aliRangeSHA1`）。流程照 p115strmhelper `helper/ali2115`（只读）：分享列表没有 hash、取不到下载链接，先把要的文件存进自己阿里云盘根目录 `115-Station秒传/job<任务id>`（`saveShareIDs`，按文件存、只存视频字幕，`media_only` 默认开），传完彻底删（`/v3/file/delete`，回收站也占空间），下一次任务开头清掉残留的 `job*`。115 一侧在目标库的转存文件夹下建包装目录「分享名 ·秒传<任务id>」（分享根只有一个目录时用它的名字、里面结构照建），任务结束前 `holdSubWrapper` 压着不整理（与订阅共用），来源链接 `DownloadLink.Kind=ali`、按包装目录名认领；一个都没进来就删掉空包装目录。秒传走 `rapidUpload115ByHash`（Cookie 通道 initupload，`rapidUploadInit115` 的二次认证改成传 `digest` 回调），115 回 status 1 = 115 上没有这份 = 未命中，**不中转上传**。查重按目标账号的台账 sha1（`ledgerAcctSQL`）。任务 `kind=ali2115` 走秒传队列 `xferLane`，不拿 `taskMu`、逐文件可停。落点同分享转存（`stagingTarget` → `xferTargetOf`，别的账号的库 `a{账号}:{cid}` 用那个账号的 Cookie）。阿里云盘账号存新表 `PanSource`（`Kind=ali`，**不放进 `Storage`**），refresh token 每刷一次都轮换、`aliClientOf` 立刻写回，刷新全局串行（`aliTokenMu`）；设备会话签名 secp256k1（`alisign.go` 用 big.Int 自己实现，标准库不支持这条曲线；私钥 = sha256(user_id)，OpenList drivers/aliyundrive 同款）。入口：链接转存页（`resolveResourceLink` → `Action=ali` → `submitAli2115`）、机器人贴链接（`wecomHandleLink` → `botAli2115`，`onJobDone` 回结果）；离线下载拒收阿里云盘链接。前端 `components/plugins/AliTransferModal.vue`（扩展功能页，扫码 / 贴 refresh token）。**还没用真账号跑过**：扫码 `query.do` 没带 p115strmhelper 那串 `ua` 风控值、`/adrive/v2/batch` 存分享的请求体与返回按 aligo 推的、资源盘 / 备份盘的选择，第一次跑看日志 `[阿里秒传]`。测试 `ali2115_test.go`（签名已知点、假阿里云盘客户端、假两端的端到端） |
| **转存后分享** | `shareout.go` `shareout_re0.go` `shareout_dian.go` `shareoutrecords.go` | 用户**自己贴链接**转存的 115 分享（链接转存页、机器人里贴链接，`shareReceiveManual` / `submitResource` 里 `req.Source == ""`），整理认出是哪一部之后发布到 RE0 / 癫影（2026-10-17，设计见 `docs/115-station-notes/SHARE-OUT-PLAN.md`）。从资源站选的、订阅转存的**不分享**（发回原站没意义，跨站搬付费解锁的链接多半违反站规），别把它们接进来。开关在 RE0 / 癫影卡片上（`components/transfer/sources/ShareOutRow.vue`），存 setting `shareout`（按站点分开保存，**不塞进 `re0` / `dian`**：那两个的保存接口整份覆盖）。提交时 `shareOutMark` 把当时开着的站和访问码记在 `DownloadLink.ShareOut` / `Code` 上；调度器每分钟 `shareOutTick`（后台 goroutine，不进任务队列、不拿 `taskMu`）按 `OrganizeRecord.LinkID` 找记录，`shareOutJudge` 判等 / 不发 / 能发（有待确认的等；最近一条记录写下后再等 10 分钟；7 天没整理出来、没认出、一条链接几部片都不发），`shareOutPayloadOf` 从整理记录算载荷（片名年份、季集、体积、文件名，画质按原文件名 `ParseResourceInfo`，字幕语言按字幕文件名 `subLangOf`、认不出不填），**零 115 请求**。标题一律 `shareOutHeadline`（「片名 (年份) S01 4K WEB-DL」，分享者起的原名常是文件名或广告，放进备注）；RE0 另填大小 / 分辨率 / 片源 / 字幕类型与语言，备注 `shareOutRemark` 一项一行（片名、季集、视频、音频、字幕、文件、原分享名，没认出的行不写）；癫影没有备注字段，画质全在 `tag` 串里。结果一站一行记进 `ShareOut` 表（同站同分享码发成过就不再发；**两站都不收重复链接**：分享码以前是从 RE0 / 癫影拿到的——用户把站上解锁的链接复制下来再贴进链接转存——两站都不发，`shareOutFromSite` 翻本地历史认：来源链接记着站名「RE0 / 影巢 / 癫影」，订阅的站名在 `SubAttempt.Source`；只有限流 / 5xx / 网络错误按失败 30 分钟后重试、最多 3 次，其余站点拒收与缺必要信息都按 `errShareOutSkip` 记跳过、不重试，提示里带「已存在 / 重复」的统一写成「站上已经有这条分享」（`shareOutRefusal`，RE0 的重复可能是 200 + `success:false`），整理记录列表经 `recordLinks` → `attachShareOuts` 带出来。RE0 两条通道：OpenAPI `POST /api/open/shares`（要 `write` 权限）；网页 `POST /api/customer/resources`（站点前端读来的，不是 Server Action），在已登录的页面里调，**必须借站点自己的 `signedFetch`**（2026-10-18 现场：普通 fetch 带着 Cookie 与 CSRF 照样 401；站点只对一小串白名单路径直接 fetch，其余 `/api/` 都要会话签名），按 webpack 工厂表 `.m` 里源码含 `signedFetch:()=>` 的模块 require 出来，401 且错误码不是字符串时照站点先 `/api/public/auth/refresh` 再重试一次，另把 Cookie `csrf_access_token` 放进 `X-CSRF-TOKEN`，影视关联用站内**数字**编号 `movie_id` / `tv_id`（打开 `/tmdb/{类型}/{编号}` 前端跳到 `/movie|tv/{uuid}`，再对这个详情页要一次 RSC，从分享按钮属性 `{"movieId":273,"tvId":""}` 里取，`re0StationNumOf`；传地址里的 uuid 回「参数错误」），发布前同站点表单先 `check/resource` 规范化链接与访问码，被拒时站点原文进日志。癫影 `POST /api/portal/share`（签名调用器），访问码、分辨率、文件格式、剧集集号必填，`tag` 串由 `dianTagString` 按站点 tag-*.js 的规则拼（`DT2|…`）；站点开着人机验证（`turnstile_enabled`）时跳过。任务中心「分享记录」页签（`shareoutrecords.go`：`GET /shareout/records` 按状态筛、带计数，`POST /shareout/records/:id/retry`；前端 `pages/tasks/ShareOutTab.vue`，没开分享也没有记录时不显示页签）只读本地记账、不请求两站；站上地址 `shareOutSiteURL` 按记账拼（RE0 有 slug 直达 `/resource/115/{slug}`，否则 `/tmdb/{类型}/{编号}`；癫影 `/r/{dianResourceKey}`）。手动重试认失败与跳过的（发成的不行；跳过的原因还在就再记一次跳过），不走定时那一轮（定时只看 8 天内的链接），判定照常、后台对这个站发一次、次数记满，再失败就停住不再自动重试。测试 `shareout_test.go` / `shareoutrecords_test.go`。**还没用真账号在两站上跑过**：RE0 网页登录后普通请求要不要别的请求头、癫影服务端对载荷的校验，都要实测一次 |
| **资源订阅** | `subscribe.go` `submissing.go` `subpick.go` `shareepisode.go` `subrun.go` `subsettle.go` `subapi.go` `botsub.go` | 以 TMDB 条目订阅（`Subscription` 一部一条、`SubAttempt` 试过的资源一条），设计见 `docs/115-station-notes/SUBSCRIBE-PLAN.md`。**缺什么零 115 请求**（`submissing.go`）：TMDB 范围内已播（播出 + `air_delay_hours`）− 台账已有（片目按目录名编号认，老目录用 `MediaLibrary` 落点反推）− 在路上（`inflight` 尝试挑中的集），在路上的不扣掉下一轮会重复找同一集。挑资源（`subpick.go`）：相关、**是订阅的这一部**（`subtwin.go`，2026-10-08 起：按片名搜 TMDB 剧集与电影找同名条目，6 小时缓存；订阅动画而标题写真人版的一律不要，有同名的要标题拿得出证据——区分形态的字样、只属于这一部的首播年份、超出同名那部总集数的集号——否则跳过；查同名失败本轮不提交。现场：订阅凡人修仙传 2020 动画挑中了 2025 真人版全集磁力，整理两部片名相等同分、包装目录的年份又推向订阅那部，真人版被洗版顶掉库里的 E01–E09，结算也看不出来——**整理那一环分不出同名，只能在挑资源时拦**，别删这道检查，也别拿「标题里有同名那部的年份」当反证：连载中的动画标题常写当年年份）、没试过（`useless` / `failed` 带 `RetryAt` 的到期可再试）、排除词、画质门槛 `RankLimit`、**资源优先级**（`subpriority.go`，2026-10-22 起，设计见 `docs/115-station-notes/SUB-WASH-PLAN.md`）：基础 / 跟随洗版策略 / 自定义三选一（`subscribeCfg.Priority`，订阅自己的存 `Subscription.Priority`，空 = 跟随订阅设置；「只要命中前 N 条」= 订阅设置的 `limit` 或订阅的 `RankLimit`），比较一律 `washCompare`，命中第 1 条算达标；跟随洗版策略时按订阅转进的库的分类方案先算分类再挑策略（`subWashStrategy`）。它取代了资源条件里的画质几项，资源条件只剩中字（新默认开）与体积；老配置由 `MigrateSubPriority` / `migrateSubPrioGlobal` 换成「自定义 + 前 1 条」，行为不变。「前 N 条」同样三态判（`subPriority.titleVerdict` / `fileOK`），别改成只看命中第几条，整季分享会被误杀。**订阅洗版**（`subwash.go`，`Subscription.Wash`，开关在订阅弹窗主表单）：库里已有、没达标的集，出现更好的 115 分享就转进来交给整理替换；补缺期间一直洗，补齐后再洗 `wash_days`（30）天（`WashUntil`，补齐那一轮定），全部达标或到期结束。只在「跟随洗版策略」（或基础 + 整理策略没写规则）且整理策略是 `replace` 时洗（`washReady`），自定义不洗——订阅和整理判得不一样，换进来的会被移进「已存在」。**库里那份的画质按整理记录里的原名认**（`subOrigNames`，fid → `Orig`），再叠媒体信息缓存，认不出分辨率的集不洗；**别改成看台账文件名**：模板没写画质时任何资源都「更好」，每轮换一次。只用分享、不下磁力；RE0 / 癫影要花积分的（2026-10-24 起）只在标题写明画质且明确比库里某一集好时解锁（`subWashTitleBeats`，经 `subPickCtx.PaidGate` 在预览之前拦），能预览的按文件再核一遍，单条上限 / 当天预算与补缺的自动解锁共用，补缺 + 洗版一个订阅一轮最多付费 1 条（`subRunner.canPay`）；洗版的尝试单独记账（`SubAttempt.Wash`，补缺时「里面没有缺的集」的合集正是洗版要看的），分享按集挑时只要比库里好的（`sharePickOpts.Beats`）。测试 `subwash_test.go`（含端到端）。原来的资源条件（`subcond.go`，2026-10-08 起：分辨率 / 质量 / 特效 / 编码 / 发布组 / 中字 / 单个视频大小，字段与写法同洗版规则（逗号命中任一、`!` 排除），但**不用 `matchField`**：它对名字做子串查找，DV 会命中 DVDRip；这里一个词要么包含在归一值里、要么在名字里前后不挨字母数字（`condTokenHit`）。界面是预设标签点选（`webui/src/components/subscribe/condPresets.ts`，一个标签对应一组同义写法，存的仍是逗号串），新增预设要确认后端能命中；全局默认 `subscribeCfg.Cond`，订阅自己的存 `Subscription.Cond`，空 = 跟随、`{}` = 自定义成不限。三态判：标题明确不符的丢；没写的分享留着，到 `pickShareEpisodes` 的 `Accept` 逐个文件严格判（文件名没写看资源标题，都没写算不符）；磁力与 RE0 要花积分的标题没写就不碰）、标题估计的季集范围（`resCoverageOf`）写明了却不含缺集的丢掉；离线整包只在估计能覆盖缺集时下；磁力挑不了集、115 离线配额按任务数扣，所以另有几道闸（2026-10-08 起）：离线策略 `offline_mode`（默认 `pack` 不下标题写着单集的磁力，`share_only` 不用离线，订阅自己可另设）、新集播出 `offline_wait_hours`（24）内只等分享（`subEval.MissingAir`）、一个订阅一轮最多一个离线任务、每月上限 `offline_monthly`（30，按 `SubAttempt` 记账，零 115 请求）、115 剩余配额低于 `offline_reserve`（10）就停（`offlineQuota115`，一个任务只查一次；`offlineGate`）；离线失败（115 报失败 / 3 天没整理记录 / 整理失败）**不自动重试**，再提交又扣一次配额。跳过的磁力写进上一轮结果（`subRunItem.Skipped`）。一轮试资源的节奏（2026-10-09 起）：按**集数**限（`max_eps_per_sub`，默认 50，分享按剩余额度截、留集号靠前的），候选从头试到尾、没用上不提前停，只有一轮最多试 `max_res_per_sub`（50）条的保险，两条之间冷却 `try_cooldown_sec`（5 秒 ±30%，P115StrgmSub 同款）；原来的「每个订阅最多试 3 条」按条数限，单集分享一轮只能补 3 集，别改回去；也别加「连续几条没用上就停」：排序和分享里有没有缺集无关，每轮之间隔着检查间隔，前面一长串没用的会把能用的拖上好几轮（参考项目都是试到底）。RE0：解锁过 / 免费的照常；要花积分的只在 ≤ `re0_unlock_max`、在每日预算内、能确认覆盖缺集、画质命中洗版规则时自动解锁（上限以内的先读 RE0 文件预览 `re0PreviewFiles`：只给文件名不给链接、不花积分，按 `pickShareEpisodes` 与真正转存同一套 `sharePickOpts` 核对缺集与资源条件，记在 `subCand.Exact`；预览不可用才退回按标题估计；超限的不预览，一个订阅一轮最多 5 条，本站缓存 6 小时），一个订阅一轮最多付费 1 条，报积分不足 / 未登录本轮停止付费（`re0StopUnlocking`）；超限的记一笔 `paid` 只通知一次。结算判入库（`ingested` / `partial`）推「补上了」（通知类型 `ingested`；头一次检查推「新订阅」，类型 `created`，放在首轮盘点之后好带上缺几集；`subscribeCfg.NotifyVer` 给老配置补新类型，新增通知类型照这个加）。分享**只转缺的那几集**（`shareWalk` 列树 → `pickShareEpisodes` 按 `parseVideoInDir` 认季集、同集按洗版排名挑一份、字幕跟着走、排除光盘与库里同 sha1 的）。提交一律先在转存目录建**包装目录**「片名 (年份) S01 ·订阅<id>-<序号>」（补 01.mkv 的上下文，也是整理记录认领来源链接的名字；**不写 `{tmdbid}`**，否则内容不对也会被强制归进订阅那部）；提交失败要收掉空包装目录（`dropWrapper`），留着守望者会反复整理它直到熔断。一轮里建的包装目录在任务结束前**压着不整理**（`subhold.go`，内存记账、重启即放行）：整理与守望者都跳过，任务结束统一放行、入队一次整理；原来边转边整理，一轮补 20 集推了 4 条入库通知。订阅提交的离线**只要秒传**（维护者定的）：提交后每 5 秒看一次包装目录，30 秒（`subOfflineWait`）还没东西就删掉离线任务（`offlineDeleteTask`，web `ac=task_del`）、收掉包装目录、记一笔不重试的失败，配额照样算这一轮的那一个离线、计入每月上限；中途被停止的不删。结算（`subsettle.go`）只看 `OrganizeRecord.LinkID`：认成别的条目 → `rejected` 拉黑，`awaiting` 不超时，分享 6 小时 / 离线 72 小时没有记录算失败，离线监视器报失败时 `subMarkOfflineFailed` 直接判。排期 `planSubNext`：提交后 30 分钟回来结算、等下一集播出（最多 7 天）；没找到按**固定间隔**（2026-10-09 起，`subCadence`）：缺集里有播出 3 天内的按 `fresh_interval_min`（60 分钟，不计入长期找不到），只缺老集按 `gap_interval_hours`（12 小时），补老集累计约两周没找到标 `stalled` 只通知一次、间隔不变。**别改回指数退避**：新集播出当天没找到就被排到一天到三天后，追更反而最慢；查一轮不发 115 请求（115 请求只在出现没试过的资源时才有），间隔只影响资源站。老配置排得太远的 `next_check_at` 启动与保存设置时由 `subClampNextChecks` 收回到补缺间隔内。候选排序在「覆盖缺集数」之后有一条 `Stale`：发布时间早于缺集里最早播出日的排后面（不丢，持续更新的分享内容会变）；别整体改成按时间倒序，补老集会让新发的低画质抢在高画质合集前面。RE0 按 TMDB 编号列资源，同名检查对它放行（同相关性）。**转存前先看分享**（`subpreview.go`，2026-10-09 起）：标题估不出范围的整季包（「剧名 4K」）原来按 0 集排在单集分享后面，被一条条单集抢先转；现在对排在前面的 `share_preview_max`（8）条直接给了链接的 115 分享先 `shareWalk`，用 `pickShareEpisodes` 按文件算出实际能补的集写进 `subCand.Exact` 再重排，一集都补不上的当场记 `useless`。列的结果按分享码缓存 1 小时（`walkShare`，出错也缓存），`tryShare` 直接用、不列第二次；只在剧集且缺 ≥2 集时预看。能补的集数一样时再比 `Reach`（补到的最靠后一集，越接近已播出越好）与 `Span`（资源里一共几集），排在「分享优先、免费优先」之后、洗版排名之前。调度器每分钟把到期的合成一个 `subscribe` 任务（`sub:auto` 取并集，在跑时不入队）。接口在 `subapi.go`：写操作只改订阅表，新建后马上入队查一轮，「立即搜索」一个订阅一个任务；详情的集格子与盘点同口径（`subEpisodeGrid`）。前端：并进影视转存页（2026-10-08），海报墙是 `pages/transfer/SubsTab.vue`，列表与详情抽屉状态共用 `composables/subscriptions.ts`（抽屉由 `MediaTransferPage.vue` 挂一份，`?sub=` 打开；找资源里点「已订阅」原地打开同一个）；旧地址 `/subscriptions` 在路由里 redirect（`?sub=` 带到订阅页签，`?tab=settings` 带到设置的订阅小节）；`components/subscribe/`（`SubscribeDialog` 新建 / 修改共用、`SubDetail` 抽屉）+ `pages/subscribe/SubSettings.vue`（按 `section` 只显示一个小节、保持挂载，保存存全部）；**新建只从「找资源」选定影片后点「订阅」**，订阅页签不另做 TMDB 搜索。整理记录页认 `?link_id=`（订阅详情跳过来）。机器人（`botsub.go`，走找资源同一套 `botFlow` 会话，`mode` 区分）：`/sub 片名`（企微「订阅 片名」）选片即订、默认值与网页弹窗一致（补缺集，在播的多季剧只订最新一季，`botSubDefaultForm`）；找资源的资源列表里 `s` 订阅这部；`/subs`（「我的订阅」）列表 → 单个订阅 `a` 立即搜索、`z` 暂停 / 恢复、`x` → `xx` 确认取消。新建一律走 `createSubscription`，与网页同一条路。测试 `submissing_test.go` / `shareepisode_test.go` / `subpick_test.go` / `subsettle_test.go` / `subrun_test.go`（端到端） |
| **通知** | `notify.go` `notify_extra.go` `medianotify.go` `wecombot*.go` `wecomcrypto.go` | 企微双向机器人（AES 验签）、TG / 飞书 / OneBot / QQ 官方、入库通知防抖聚合 |
| **演职人员补全** | `embypeople.go` `personname.go` | 「影视刮削」页「演职人员」页签的定时任务（2026-10-04 前在扩展功能里；`kind=person`，人物队列，零 115 请求）：按片目列 Emby 的 People，给缺头像 / 名字不是中文的演员、导演、编剧补 TMDB 头像、中文名、中文简介。**一律走 Emby API**（`POST /Items/{id}/Images/Primary` 与整条 DTO 回写 `POST /Items/{id}`，改过的字段加进 `LockedFields`）：Emby 的人物头像在它自己的 `<programdata>/metadata/people/首字母/姓名/`，还在库里记着图片标签，往目录里丢文件不生效。TMDB 人物 id 取人物自带的，否则拿片目 credits（剧集 `aggregate_credits`）**按名字精确对**，歧义就不补；**别改成 `/search/person` 按名字搜**（同名的人太多，认错一次就钉死）。中文名取 zh 翻译 → 中文原名 → 别名，OpenCC 繁转简。`PersonMeta` 缓存 TMDB 人物（刮削写 NFO 演员名也读它，与 Emby 改过的中文名对得上，否则重刮一次 Emby 又按英文名建新人物）；`EmbyPersonMark` 记账没补全的人物（查无结果 30 天、出错 1 天）。片目按加入时间排序，单次上限 / 停止 / 中断时把停下的片目序号存成续扫断点（setting `personfill_cursor`），下次从断点扫到末尾再绕回开头，看满一圈归零。头像、名字列表里就看得出，只缺中文简介的人物另按 id 批量读简介（`embyOverviews`）。**头像还是远程链接的也算缺**（`remotePrimary`，2026-10-14）：本站 NFO 给演员写了 TMDB `<thumb>`，Emby 建人物时只记链接、`ImageTags` 照样有 Primary，等用户打开详情页才自己去 TMDB 下；所以有头像标签的人物逐个读 `/Items/{id}/Images` 看路径（确认本地的进程内记住、不再查），远程的本站下好上传，TMDB 人物详情没头像就用链接里那张；记账暂缓的人物也查这一项。**读人物一律走列表查询 `/Items?Ids=`，别改回 `/Users/{uid}/Items/{id}`**：后者对没刷新过的人物会让 Emby 当场去 TMDB 拉元数据，连不上 TMDB 时卡到超时。中文名已被另一个人物条目占用时只补头像不改名。参考 MoviePilot 官方插件 personmeta（只读）。中日韩名字去掉中间的空白（`cleanPersonName`，「周 星驰」→「周星驰」，夹着拉丁字母的不动；缓存与 Emby 里已有的中文名同样处理）。**合并重复人物**（`merge_dups`，默认关，`embypeoplemerge.go`，2026-10-14）：同一个 TMDB 编号在 Emby 里有好几个人物条目时，只在全库扫描开头处理：留中文名 / 有头像的那个，组里名字都得是这个 TMDB 人物的原名或中文名，按用户读片目完整 DTO、把演职人员表里重复那项换成留下的、整条写回，**再查一遍确认重复条目没有片目挂着、留下的都挂上了**才 `DELETE`。思路来自神医助手 RefreshPersonTask，但它把重复的全删掉等 Emby 重建，连不上 TMDB 时重建出来什么都没有，**别改成那样**。人物的元数据目录按名字建，同名的两个人物共用一个，删一个就带走另一个的头像：同名的重复不合并，路径与别的人物重叠的不删（`personPathSafe`）。预览 `GET /personfill/dups` 只读 Emby。**刮削后补全**（`after_scrape`，默认关，`embypeoplescrape.go`）：刮削任务结束时把这次刮过的片目（自动刮削只算真写了东西的）按 TMDB 编号排一个人物任务（去重键 `personfill:scrape`，排着的取并集），先等 Emby 读完新 NFO（1 / 3 / 6 / 10 分钟各找一次，`AnyProviderIdEquals`），只看这几部、不动续扫断点，等满还查不到的留给定时任务。**同步到本地 NFO**（`nfo_sync`，默认开，`embypeoplenfo.go`，2026-10-13）：补全只改 Emby 数据库，直接读媒体目录的播放器看 NFO（英文名、TMDB 头像链接连不上）与 Emby 两个样；每部补完后把它的 NFO（电影 STRM 同名 / 剧集 tvshow.nfo）里 `<actor>` 的 `<name>`、`<director>`、`<credits>` 换成缓存中文名（只改这三处文本，其余原样），演员头像存 `.actors/名字.jpg`（Kodi 约定，优先取 Emby 那张、只在 Emby 有头像时取，一次任务最多 2000 张）。认名字只用缓存与本轮已取的演职员表，**别为改 NFO 多发 TMDB 请求**（定时任务每次扫全库）；Emby 刮削模式、台账里的网盘镜像 NFO、媒体库外路径都不碰；监控上传传过的 NFO 改写后记成已上传，免得网盘上同名两份。剧集 NFO 的演员 2026-10-13 起取 `aggregate_credits`（所有季，前 `nfoTVMaxCast`=50 位），与补全同一份名单。测试 `embypeople_test.go`（假 Emby + 假 TMDB 端到端）/ `embypeoplenfo_test.go` |
| **媒体信息补全** | `metafill.go` | 「影视刮削」页「媒体信息」页签的定时任务（2026-10-04 前在扩展功能里；`kind=metafill`，挂在刮削队列上，不拿 `taskMu`，扫描本身零 115 请求）：海报墙口径（`localTitlesSnapshot` + `grade`）挑出没刮全的片目，建一个刮削任务（只补缺失、不上传、不探测）；分页读 Emby 条目（`walkLocalEmby`，与卡片快照同一个遍历）挑出缺媒体信息的视频，**按条目点名**（`item:<id>`）建一个**自动规则**的探测任务（`probeJobParams.Auto`）。两道闸：单次上限（片目数 / 视频数，探测按条目点名所以上限是准的）；补刮记账 `MetaFillMark`（按片目 key 记当时缺什么，缺的没变就 30 天内不再刮）。探测不另记账，`EmbyExtractMark` 已经管着。建出的两个任务 id 写进结果的 `follow_jobs`，任务中心那一行两个都显示（`jobFollowsOf`）。前端 `webui/src/components/scrape/MetaFillPanel.vue`。测试 `metafill_test.go` |
| **媒体库表（V2）** | `libstore.go` `libapiv2.go` `catscheme.go` `catschemeapi.go` `catsimple.go` `catsimpleapi.go` `orgconfigv2.go` `xfertarget.go` `mediaroot.go` | 2026-10-20，设计见 `docs/115-station-notes/MULTI-ACCOUNT-V2-PLAN.md` §10。**一个库一条 `model.Library`**（名称 → 115 目录与账号 → 本地目录 → 转存/待整理文件夹 → 分类方案 → 刮削 → 上传到 115），是唯一事实来源；读库一律 `loadLibraries`（改完 `forgetLibraries`）。老配置（`full.cid` / `extra_libs` / `lib_opts` / `acct_libs` / `staging_libs`）第一次启动由 `MigrateLibrariesV2` 换算（网盘目录与本地路径不动，转存文件夹不自动设、提示用户重设，`libraries_v2_notice`）；之后页面（`/media-libs`）存完由 `writeLegacyLibConfigs` **写回**那几份老配置给还读它们的代码，老配置不再反向同步（`syncLibrariesFromLegacy` 只剩老的 `PUT /libraries` 用）。**批量添加**（`POST /media-libs/batch`，前端 `MediaLibBatchDialog.vue`）：选一个 115 上级目录、勾子目录，一个子目录一个库；转存文件夹只给路径的（界面默认 `/最近接收/库名`）由后端 `ensurePath` 找或建，先不带转存文件夹整组校验一遍再建目录，存一次全成或全不成，一次最多 `libBatchMax`（30）个。库的先后顺序不能再调（上下移动已删：第一个主号的库是「首个库」，老记录 `LibCid` 空认它，调顺序会让老记录换库）。**本地媒体根固定**：`mediaRoot()` = 环境变量 `MEDIA_ROOT`（默认 `/media`），界面只读；库的本地目录是它下面的相对路径、可以多级，认库按整段最长前缀（`libOfLocalRel` / `splitLibRel`）。换根时 `MigrateMediaRoot` 改写存着旧根绝对路径的记录（`UploadMark`、监控上传目录）；台账有内容而根下一个库目录都看不到时同步与整理停下并提醒（`mediaRootProblem`）。测试里 `mediaRootLegacyForTests`（`TestMain` 打开）让老测试照旧按 `full.local_path` 认根。**二级分类是多套分类方案**（`CategoryScheme`，`CategoryRule.SchemeID`）：库选一套，0 = 不分类（片目直接放库目录下，`categoryFlat`）；`library:` 键作废（建方案时去掉）。**简单模式**（2026-10-23，`catsimple.go`，照洗版简单设置的约定）：和专家模式是同一份 YAML，保存时由表单生成（`catSimpleForm.yaml`），读时 `catSimpleOf` 认回来（认不回来的——认不出的条件、顶层多了键、分类后面直接写值——`GET /category/schemes/:id` 回 `simple: null` + `simple_why`，只能在专家模式改；认回来后 `catSameRules` 再核一遍分类结果不变）。每个媒体类型一张有顺序的清单 + 固定在最后的「其余都放这里」（没写兜底的认回来时写明 `X/未分类`，同 `classifyTargetIn` 最后一档）；条件值是原样逗号串，类型 / 地区 / 原语言的点选标签在前端拼（`pages/organize/catSimple.ts`，盖不住的值留在「其他」里，所以排除写法也不丢）。「试一试」`POST /category/schemes/preview` 按没保存的规则算一部片进哪个目录（`catTry`，与整理同一个 `matchCategory`，没命中的原因 `categoryMissReason` 跟着它改）。保存（两种模式都是）时删掉 / 改名的分类原目录里已有片 → 409 `cat_changes`（`catDirChanges`，只看用这套方案的库的台账），确认后带 `confirm_cat_change`。前端 `SchemesTab.vue`（模式切换）+ `CategorySimple.vue` / `CategoryRuleRow.vue` / `CategoryTry.vue`，模板在 `catSimple.ts` 的 `CAT_TEMPLATES`（「默认」那个与 `defaultRules.ts` 分得一样，改一边要改另一边）。测试 `catsimple_test.go`。**整理按库跑**（`organizeRuns`）：放进哪个库的转存文件夹就整理进哪个库（`OrgConfig.PinLib`），用那个库所在账号的通道；校验不过的库（方案被删、账号没登录、工作目录建不出）这一轮不整理，转存文件夹里的条目各记一条失败（`stage=config`）、推一条消息，改好后重新整理。确认 / 忽略 / 重新整理回记录所在的库（`orgConfigForRecord`，新记录看 `OrganizeRecord.LibID`）。**转存进哪个库**（`xfertarget.go`）：一个要整理的库直接进；多个用「默认转存库」（setting `transfer_target`），没设就每次问（409 `need_lib`）；「转存到」沿用原字段、值写 `lib:<id>`；别的账号的目标写 `a{账号}:{cid}`，分享转存与离线提交据此换 Cookie。**监控上传按库**（`libupload.go`，2026-10-24）：开关是 `Library.Upload`（库弹窗「上传到 115」，新建默认关；`PUT` 不带 `upload` = 不改），取代全局 `monitor.enabled` 与单独的「监控上传」页（`/upload-download` 重定向到媒体库）。每分钟扫开着的库的本地目录（最近 24 小时、标准图片名 + `.nfo`、`UploadMark` 指纹），按相对路径对到这个库的 115 目录，用库所在账号的 Cookie（`cookieOf`）；别的账号**只走 Cookie 通道**（`uploadAs`：OpenAPI 授权的是主号），库根路径现查 `acctDirAbs`（不写 `PathCache`，进程内缓存 10 分钟）。每传一个前再看一次开关（`uploadLibOn`）。老的 `monitor.enabled` 由 `MigrateLibUpload`（排在 `MigrateLibrariesV2` 之后，只跑一次）换成主号各库开着、附加账号的关着。原来的 5 分钟兜底引擎 `uploadMetadataOnce` 自监控目录固定成媒体根后就没跑过，已删。测试 `libstore_test.go` / `libv2sync_test.go` / `orgconfigv2_test.go` / `libapiv2_test.go` / `xfertarget_test.go` / `libupload_test.go` |
| **多个媒体库** | `libroots.go` | **（V2 之前的口径：媒体库表还空的部署、以及还读老配置的代码按它走，新做法见上一行「媒体库表」；分类规则的 `library:`、转存子目录绑定在 V2 下不再起作用）** 主库 `full.cid` 之外可挂**附加媒体库** `full.extra_libs`（2026-10-12）。本地天生分库（STRM 第一层是库的**本地目录名**），所以硬约束只有「本地目录名不能重名（大小写不敏感）、库之间与工作区互不包含」（`checkWorkspaceSlots` / `planLibrariesSave` 保存时拦，`resolveLibRoots` / `resolveIncrLibs` 运行时再拦）。**本地目录名**（`liblocal.go`，2026-10-20，设计见 `docs/115-station-notes/MULTI-ACCOUNT-PLAN.md` §3 / §12）：存 `lib_opts.libs[cid].local`（不放 `full`，同样怕整存整取），空 = 老行为现读 115 目录名；启动时与保存 `full` 后 `FreezeLibLocalNames` 按缓存 / 显示路径补上（零 115 请求，候选里优先台账在用的那个）；台账里有内容后不能改（`PUT /libraries` 的 `locals`，`planLibLocals`）；115 上改名只影响显示（`libRoot.PanName`）。**取本地第一层一律经 `libDirName` / `libLocalName`，别再拿 `get115DirInfo` 的名字直接拼本地路径**。**同步**：全量逐库跑（`fullSyncLib`，失效 STRM 只标本库前缀，有库失败不标事件窗口）；增量用 `incrLibSet`（`at` / `dirRel` / `localRelOf`，两个库根的 base 都是 `""`，合并遍历目标要比库）；深删按台账第一段分库找目录（`groupRowsByLib`）；海报墙上传按库名找库（`libRootNamed`），监控上传 V2 起按 `Library` 本身（见上一行）。**整理**：分类规则 YAML 写 `library: 库的本地目录名`（或 cid，`CategoryRule.Library`，不是匹配条件、不影响兜底），`classifyTarget` 带回它，`orgCtx.classify` 用 `pickRuleLib` 换成 cid 并切库（`enterLib` → `orgSink.useLib`；每个条目识别前先 `enterLib("")` 复位，别沿用上一个条目的库）。之后 `ensurePath` 一律用 `ctx.libCid()`，洗版 / 撞名用 `ctx.libName()` 作台账前缀（`washScanner.inLib`、`tryWashReplace` 的 libName），`ctx.libAbs` 也换成目标库的。**认不出的库报错停下，不退回主库**（保存分类规则时 `checkRuleLibraries` 先拦一道）。整理记录 `OrganizeRecord.LibCid`：主库记空（老记录同口径），附加库记 cid，`orgSink.note` 在有落点时盖上；重新整理按它认旧位置（`libRelIn`），按规则算新库，库变了不算原地刷新；记录的库已移出配置时拒绝重新整理（`recordLibOK`）。网盘文件页的 `libredo` / 指定季集给记录填片目所在库，指定季集要求规则的库与片目现在的库一致。跨库的同一份文件（sha1）按洗版策略处理，replace 就是让位、挪进规则指定的库。读库清单一律 `fullSyncCfg.libCids()`，别再自己 Unmarshal `full` 取 cid。**移出配置的库**（或 115 上改了名的库）本地 STRM 与台账没人管：`libleft.go` 列出台账第一段既不是配置库名、也不是分类目录第一段（老台账不带库名）的残留，「账号与媒体库」上提示，用户点了才入队清理（kind `libclean`，只删本地与台账、网盘不动；删台账用转义的 LIKE，库名带 `_` 别误删别的库，所以没复用 `removeSyncedDir`）；各库名认不出时不下结论。界面叫「附加媒体库」。**按来源分库**（`stagingsub.go`，2026-10-13）：转存目录下一层的子目录可以绑一个库（setting `staging_libs`：`[{cid,name,lib}]`，lib 存 cid；不塞进 `share`，那个 key 两个页面整存整取），选库优先级 **来源子目录 > 规则 `library` > 主库**（MoviePilot `get_dir` 同序），分类目录照规则算、只换库。整理时绑定子目录**是独立的扫描根，不是容器目录**（`expandStagingEntries` 展开里面一层、`stagingWork` 用 `withPending` 换扫描根；当容器的话「4K」会成为识别的路径上下文）；选库认 `cfg.Pending`（`sourceLibOf`），所以确认入库（`rcfg.Pending = rec.SourceCid`）与网盘文件页整理子目录里的条目都自动沿用来源。子目录本身进 `workspaceRootCids` / `orgProtectedCids`（不当条目、不被空目录清理），网盘文件页角色 `stagesub`。按来源定的记录打 `OrganizeRecord.LibBySource`，重新整理（`redoLibOf`）留在原库、不按规则改库（子目录事后可能解绑）；没入过库的记录看 `SourceCid` 还绑不绑。绑定的库移出配置时报错停下，不退回。守望者与离线秒传试探**不把绑定子目录本身算作条目**（`countStagingUnheld` / `stagingTopCount`），否则空子目录让守望者一路计数到熔断；代价是每个绑定子目录每分钟多列一次。提交：`resSubmitReq.Subdir` → `stagingTarget`（只认绑定表，RE0 解锁之前就校验）；订阅 `Subscription.Subdir`（空跟随 `subscribeCfg.Subdir`，`root` = 转存目录本身），包装目录建在 `targetOf(sub)` 下，解绑的退回转存目录。认领来源链接不受影响（按 fid / 顶层条目名）。机器人提交不选子目录。前端（`StagingLibsCard.vue` 已于 2026-10-23 删除）`SubdirPicker.vue`（找资源 / 链接转存的「转存到」）、`composables/stagingSubs.ts`。测试 `libroots_test.go` / `libleft_test.go` / `stagingsub_test.go`。**库卡片**（2026-10-15，设计见 `docs/115-station-notes/MULTI-LIB-SIMPLIFY-PLAN.md`；前端 `components/accounts/LibraryCards.vue`，挂在「账号与媒体库 → 媒体库」，只有一个库时只留一行「添加媒体库」。默认库的 115 目录仍在上面「媒体库位置」里改，两边都动 `full`，卡片存完由 `onLibsSaved` 把 `extra_libs` 同步回去，别让整存整取冲掉。转存入口只在卡片上开，「影视转存 → 设置」不再列出，「转存到」下拉写库名 `stagingSubLabel`）：`GET /libraries` 汇总每个库的位置、落进来的分类规则（只读）、转存入口、Emby 覆盖，零 115 请求（`libraries.go`）；`PUT /libraries` 一次保存附加库与转存入口，移除库时把指向它的 `library:` 改回默认库、解绑入口、订阅退回转存目录（`librariessave.go`，校验全在纯函数 `planLibrariesSave`）。**分类归属只在二级分类里改**，后端只在移除库 / 库改名时用 `rewriteCategoryLibrary` 改 YAML（`categorylib.go`：只动 `library` 键，改完前后解析对比，不一致就拒绝）。二级分类大纲在多库时标出每条规则落进哪个库（`pages/organize/ruleDest.ts`，认库口径与 `pickRuleLib` 一致，改一边要改另一边）；保存时换了库、原库里已有片的分类返回 409 + `lib_changes`，`YamlRuleTab` 确认后带 `confirm_lib_change` 重发（开关 `categoryLibConfirm`，已开）。库在 115 上改了名、规则里还写着旧名时，`POST /libraries/fix-rename` 一键换成新名（`librariesfix.go`：只认服务端算出的修复——配置里存的旧显示路径的目录名 == 规则里写的值、缓存里是新名——请求只带旧名，认不出的不猜）。测试 `categorylib_test.go` / `libraries_test.go` / `librariesfix_test.go`，`librariessave_http_test.go` 用真 SQLite + 真 setting.yaml 走 HTTP 测移除库与改名修复。**每个库由谁整理**（`libopts.go`，2026-10-15，设计见 `docs/115-station-notes/LIBRARY-ROLES-PLAN.md`）：setting `lib_opts`（按库 cid，**不塞进 `full`**），「本站整理」（默认，老部署零变化）或「我自己整理」（`self`）。自己整理的库本站不往里放东西：`pickRuleLib` / `selfLibGuard` 拒绝规则指向它，转存入口不能绑它（`planLibrariesSave` / `inboxSelfLibErr`），`sourceLibOf` / `recordLibOK` 拒绝整理与重新整理，网盘文件页里只能看（`rowActionsOf`）；它仍在 `OtherLibs` 里当保护子树（`OrgConfig.SelfLibs` 另记）。片目层级用户声明（`Depth` 1–3），**不推断**：`libCategoryLayout` 改成结构体（`cats` 分类表 + `self` 库名→层级），`titleOf` 首段是自己整理的库时走 `selfTitleOf`，不套分类表；声明时 `GET /libraries/preview` 按台账给预览（`libpreview.go`，标出像季目录 / 里面还有好几部片的）。改成自己整理时指向它的规则回默认库、入口解绑（同移除库）。**「默认放这里」与首个库是两回事**（阶段 3）：`lib_opts.fallback` 记默认库，经 `libOpts.fallbackOf` 归一（没设 / 被移除 / 改成自己整理 → 第一个本站整理的库；自然默认不存），`pickRuleLib` 认 `libRoot.Default`、整理认 `OrgConfig.Fallback`（`ruleLibOf`），网盘文件页认 `ruleLibCid`。首个库（`full.cid`、`OrganizeRecord.LibCid` 记空）照旧只管位置，可以是自己整理的。**一个本站整理的库都没有时整理线停用**（`errNoOrganizeLib` / `noOrganizeLibNow`）：`loadOrgConfig` 报错，定时整理与转存整理的后台任务按空转收掉（`noOrganizeLibOutcome`），守望者与订阅调度不动，手动的报原因。每个库上一次全量的结果存 setting `lib_sync_stats`（`libsyncstat.go`，只由全量写，按库 key），`GET /libraries` 的 `last_sync` 给库卡片显示。总览多库时带 `libs`（`dashLibStats`，台账片目数）；二级分类 / 洗版 / 自动整理页顶部「作用于」在 `components/LibScope.vue`（数据同 `/scrape/config` 的 `libs`，带 `mode` / `default`）。海报墙 `/local/titles` 带 `libs`，多库时顶部按库切换。测试 `libopts_test.go` |
| **多个 115 账号** | `account115.go` `acctlibs.go` `incracct.go` | **2026-10-20 改按 V2 做**（`docs/115-station-notes/MULTI-ACCOUNT-V2-PLAN.md` §10，取代 MULTI-ACCOUNT-PLAN.md 里「附加账号只读」那套）：各账号平等，都能整理、生成 STRM，几个账号的库可以放在同一个本地上级下进同一个 Emby 库（LitePan 同款）。**主号** = `Storage` 里 `type=115`、`Main=true` 的那一行，取主号一律 `mainStorage115` / `loadMainStorage115`；按账号办事走 `cookieOf(acct)` / `opsFor(acct)`（`acctMain = 0`），写操作不再拦。**别的账号不写 `PathCache`**（按路径查时不分账号，两个账号都有「/影视」会串）：整理里查路径走 `orgDirAbs`（主号走缓存、别的账号 `acctDirAbs` 现查），`rememberDirAt` 只记主号，新增查路径的地方照这个分。台账：`SyncedFile.FileID` 是原始 fid、`Account` 列记账号，`ledgerFidTaken` 拦住覆盖别的账号的同 fid 行（fid 跨账号是否唯一没验证过）；直链 `/d/{账号}~{pickcode}`（`encodePlayID`）。**增量**（`incracct.go`）：挂着库的别的账号跟在主号那一轮后面、同一把 `taskMu` 里各跑一轮（60 秒一次），同一个 `executeIncrementalSyncWith`、只换按账号的 `realIncrDeps`（`acct`，路径一轮内存缓存、游标等 key 带 `@{账号}`，`acctRuntimeKey`）；`SyncEvent.Account` 分开、别的账号 EventID 带 `a{账号}:` 前缀；`acctLibSyncTick` 每 6 小时补全量兜底。**洗版 / sha1 去重按账号**（`washScanner.acct`、`ledgerAcctSQL`），深删按账号分组用各自的通道。已存在 / 冗余设置上一套、每个账号同一路径下各一份（`accountWorkspace`，cid 记 setting `acct_ws`）。订阅只进主号的库（包装目录、等秒传、删离线任务、查配额还只走主号）；离线任务完成监视只看主号，别的账号靠守望者。2026-10-22 起不再藏在 `STATION_MULTI_ACCOUNT` 后面（维护者要求直接开放），附加账号与建库时选账号都直接显示；**仍没用真的两个账号验证过**（fid 跨账号是否唯一、风控按账号还是按 IP 算）。测试 `account115_test.go` / `acctlibs_test.go` / `incracct_test.go` |
| **备份恢复** | `backup.go` | 「系统配置 → 备份恢复」（2026-10-17）。备份包 = 配置目录第一层全部文件 + `VACUUM INTO` 数据库快照 + `manifest.json`，存 `DATA_DIR/backups`（setting `backup.dir` 可改），文件名带时间与来源（手动 / `-auto` / `-prerestore`），下载 / 删除 / 恢复接口只认这个形态（防 `../`）。定时备份挂在 `StartSyncScheduler` 的分钟调度上（`backupTick`，cron 命中后台跑，不进任务队列、不碰 115），按 `keep` 只清定时的那几份。**恢复不在运行中替换文件**（库开着、setting.yaml 有内存缓存）：接口只校验（条目只许 `config/<文件>`、`data/115-station.db`、manifest；数据库过 `quick_check`）并解到 `DATA_DIR/restore-pending`，暂存前先打一份 `prerestore`；下次启动时 `main.go` 在读账号、开库之前调 `ApplyPendingRestore` 换进去（只覆盖包里有的配置文件、数据库连同 -wal/-shm 一起换，失败改名 `restore-failed-*` 不重撞），结果写 `.restore-result.json` 给界面看。网页「立即重启」走 `SetRestartHook`，与 SIGTERM 同一套优雅退出、退出码 1，靠容器重启策略拉起；**这不是自更新**（§6.7），别借它加拉镜像。前端 `webui/src/pages/settings/BackupTab.vue`。测试 `backup_test.go` |
| **上手清单** | `setup.go` | 总览顶部的新手清单（2026-10-19）：`GET /setup/status` 按顺序判 7 步（登录 115 / 选媒体库 / 工作目录 / TMDB / 全量同步 / Emby（可选）/ 整理第一部（可选）），判定写在纯函数 `planSetup`，**零 115 请求**（用户名只看容量缓存）；Emby 一项读一次媒体库列表按路径映射判覆盖（`embyPathSuggestion`），按配置做键缓存一分钟。「不再显示」存 setting `setup`（`POST /setup/hide`），界面上没有重新打开的入口（账号菜单里的那项 2026-10-19 按维护者要求删了）。前端 `components/dashboard/SetupChecklist.vue`：**只放跳转，不放配置表单**（多一个编辑入口就多一处整存整取覆盖），唯一例外是调同一个接口的「一键创建工作目录」；step key 与各页 `?tab=` 对应，改名要两边改。测试 `setup_test.go` |
| **其他** | `dashboard.go` `medialib.go` `offline.go` `dllink.go` `covergen.go` `checkin115.go` `rbclean.go` `dirclean.go` `imgcache.go` `update.go` | 仪表盘、**媒体库台账校准**、离线下载、**来源链接**（整理记录的「来源」）、媒体库封面生成、115 签到、**定时清理 115 回收站**（`rbclean.go`，2026-10-25，扩展功能：只彻底删除 N 天前进回收站的条目，读 `/rb` 列表按每条 `dtime` 挑、带 tid 每批 1000 条调 `/rb/secret_del`；**永远不发不带 tid 的请求**（那是整个清空，空目录清理 / 深度删除都靠回收站兜底），保留至少 1 天；执行时间全局一个，开关 / 保留天数 / 安全密钥按账号（setting `rbclean` 的 `accounts`，key 是 Storage 行 id，新加的账号默认不清），只走 Cookie；参考 p115strmhelper 的整个清空与 p115client `batch_recyclebin_clean`）、**定时清理 115 离线任务**（`offclean.go`，2026-10-25，扩展功能：115 离线个别磁力会「下载失败」或卡在百分之几不动；订阅只等 30 秒、等不到删任务，但删失败只打日志，网页 / 机器人提交的从来不删。按 cron 翻完离线列表（`fetchLixianTasksPage` 按 `page_count` 翻页），删 status -1 的与排队 / 下载中且 `add_time` 超过 N 小时（默认 24，0 = 不删）的，完成的不动；默认只动本站提交的（info_hash / 原始链接对上 `DownloadLink` 或离线归属标记），可改成全部。`task_del` 一律 `flag=0` 只删任务。删掉的若对应订阅还挂着的离线尝试，`subMarkOfflineReason` 判失败、不再用这条链接。账号按 Storage 行 id 开，只走 Cookie）、**定时清空 115 目录**（`dirclean.go`，2026-10-25，扩展功能：setting `dirclean` 存多个目录 `{account,cid,path}`（account 同 `Library.Account`，主号 0），到点列出每个目录的直接子项整批删进回收站（`ops.deleteFiles` → `/rb/delete`），目录本身留着；参考 p115strmhelper「清空最近接收」（它只认那一个系统目录、不设防）。守卫：网盘根不许；媒体库目录、转存/待整理文件夹（含绑定库的子目录）本身、里面、上级都拒绝，已存在 / 冗余本身可清、上级拒绝（`checkDirCleanTarget`，上级靠逐个读受保护目录的祖先链判，读不出就不删）；直接子项里出现受保护目录也整个不删；保存时就核一遍（`dirCleanPrecheck`）。只走 Cookie）、**定时清空阿里云盘目录 / 清理阿里云盘回收站**（`alidirclean.go` / `alirbclean.go`，2026-10-25，扩展功能，照上面两个 115 的做；账号共用阿里云盘秒传的 `PanSource`，接口在 `aliyun.go` 的 `listDirIn` / `listRecycle` / `batchByID`，两个功能经 `aliCleanSide` 注入、测试 `aliclean_test.go` 用假盘。清空目录：目录按路径存（资源盘 / 备份盘二选一），保存时逐级按名字查出 `file_id`，执行时再查一遍、对不上（改名 / 删了重建）就不删；网盘根、秒传临时目录 `aliTempRoot`（只在秒传用的那个盘）不许，同层同名认不准不删，子项 `parent_file_id` 不对整个不删；删进回收站（batch `/recyclebin/trash`）。回收站：资源盘、备份盘都清，`keep_days` 0 = 整个清空（阿里云盘上没有东西靠它的回收站兜底，所以不像 115 至少 1 天），也按条目 id 彻底删（batch `/file/delete`），不调 `/v2/recyclebin/clear`；保留 N 天时 `trashed_at` 读不出的不删。**都还没用真账号跑过**：`trashed_at` 字段名、batch 子请求的返回，第一次先看预览与日志 `[阿里回收站]` / `[阿里清空目录]`）、**界面图片缓存**（`/tmdb/img` `/poster` `/embyimg` `/local/poster` 共用：落盘 `DATA_DIR/imgcache`、同图并发合并、失败记 5 分钟、60 天未用自动清；拉不到图回 404 让前端 `PosterImage.vue` 显示占位，别再回透明 GIF。界面上的海报一律用 `PosterImage`，TMDB 尺寸按显示宽度约两倍挑）、**更新检测**（只提示不自更新，见 §6.7） |

### 数据模型（`internal/model/model.go`）

22 个实体，关键的几个：`Storage`（网盘账号凭据）、`StrmFile`、`SyncTask` / `SyncEvent` / `SyncedFile`（同步台账）、
`CategoryRule` / `ScrapeRule`（YAML 规则，洗版保存在 `type=wash_config`）、`Setting`（键值配置）、
`MediaLibrary`、`UploadMark`、`OrganizeRecord`（整理流水，一次动作一条）、`EventSuppress`（整理自产事件抑制）、
`PathCache`（115 目录 id → 网盘绝对路径）、`DownloadLink`（来源链接，见下）。

> `MediaLibrary` 与 `OrganizeRecord` 不是一回事：前者「一部影视一条」（去重 upsert，仪表盘用），
> 后者「一次整理动作一条」且失败与未识别同样留痕（记录页与「重新整理」用）。

> `DownloadLink`（`dllink.go`）是来源链接：磁力/ed2k/HTTP 离线与 115 分享转存提交时落一行，
> 整理时 `orgSink.note` 用 `dlLinkMatch` 认领，把链接 id 记在 `OrganizeRecord.LinkID` 上
> （一条链接可对应多条记录），整理记录页据此显示原始链接与提交来源。
> **它没有独立页面**：原来的「上传下载 → 下载记录」已并进整理记录。下载/转存失败的反馈
> 不靠翻记录 —— 同步失败由提交处当场回复（网页提示 / 机器人回复），
> 离线任务的异步失败由 `StartOfflineTaskMonitor` 推「✗ 离线下载失败」。新增提交入口时两头都要有。
>
> ⚠️ **认领产物不许新增任何 115 请求**，只能用已经在手的数据：离线走监视器既有的 30 秒轮询
> （摘 `file_id` 与任务名），分享走 `/share/snap` 返回里的顶层条目名（转存后 115 保留原名）。
> 认领因此是两级的：fid 精确、名字兜底，都对不上就让那条整理记录没有来源，不要为了配上
> 去加一次列目录 —— 这条约束是需求方明确提的。
>
> **新增任何「提交链接 → 内容落进转存目录」的通道，记得一起 `dlLinkRecord`**，
> 否则那条路进来的内容在整理记录上永远认不出来源。

> ⚠️ **`PathCache` 是有失效要求的缓存，不是普通的读缓存。** 目录被改名/移动/删除后
> 必须失效或重定位对应子树，否则「已搬进冗余的目录」会被永久算成还在媒体库里 ——
> 整理的保护子树守卫失效、增量给冗余内容生成 STRM。
> 钩子挂在 `pan115Ops` 的 `moveFiles` / `rename` / `renameBatch` / `deleteFiles` 上
> （`open115.go`，就在 `markSuppressed` 旁边）。**新增任何会改动网盘目录结构的写操作，
> 都要记得一起挂上** `forgetDirSubtree`。
>
> 它还是**网盘删目录时认位置的主要依据**（2026-10-10 起）：`delete_file` 事件的父目录靠不住，
> 增量先按被删目录自己的 id 查缓存（`lookupDirAbsAnyAge`，不看保鲜期，名字对不上不用），
> 查不到、事件父目录也用不上时读一次回收站列表（`recyclebin115.go`，每条带原父目录 cid，一轮最多一次），
> 再不行才退回按名字找 —— 「Season 0」这种名字库里有几百个，按名字认不出。
> 所以整理的 `ensurePath` / `mkdir` 与同步遍历（`walkCtl.panAbs`、快速模式的目录表）都顺手把目录记进去，
> 只用缓存里已有的父目录位置，不为此多发 115 请求。

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
打 `v*` tag 时镜像推完再由 `release` job 建 GitHub Release —— 界面的更新检测读的就是它（§6.7）。

发版：

```bash
git tag v26.10.9 && git push origin v26.10.9
```

版本号按发版日期写：`v{年后两位}.{月}.{日}`，月、日不补零（2026-10-09 → `v26.10.9`）。
同一天再发就加序号：第二次 `v26.10.9-2`、第三次 `-3`……先 `git fetch --tags` 看当天到第几了。
序号版也是正式版，照常提示用户更新（同一天发几次就提示几次）；`-` 后面带字母的（`v26.10.9-rc1`）才是预发布、不提示。
这条规则在 CI 的 `release` job（`*-*[!0-9]*` 才加 `--prerelease`）与 `update.go` 的 `parseVersion`（纯数字后缀认成同日序号）各写了一份，改一边要改另一边。
版本号不写在任何文件里，git tag 是唯一来源（`checkout` 要 `fetch-depth: 0`，否则 `git describe` 看不到 tag）。

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
- 颜色只能从设计令牌取，组件里写死色值必然漏暗色模式。**颜色的唯一来源是 HeroUI 主题变量**
  （`--accent` / `--surface` / `--muted` / `--foreground` …，明暗由它按 `html.dark` 切换）；
  `main.css` 里的 `--c-*` 只是给老样式用的别名，新代码直接写 HeroUI 变量。
  圆角用 `--r-sm/lg/xl/card`，**不要**写 `--radius-sm/lg/xl`——那几个名字被 HeroUI 的 `@theme` 占用、刻度也不同。
  配色是「影院风」（2026-09-30）：`main.css` 在 HeroUI 默认主题上只改色相——暗色（默认）琥珀橙，亮色两套，
  暖橙与清爽青蓝（`html[data-palette='fresh']:not(.dark)`，由 `stores/theme.ts` 与 `index.html` 首帧脚本挂上）。
  「电影」「剧集」标签一律用 `components/MediaTypeChip.vue`（专用色 `--media-movie` 紫 / `--media-tv` 蓝，在 `main.css`），
  别再拿 `HChip` 的 accent / warning 自己拼：暗色下琥珀橙和 warning 黄几乎分不出，还会被读成状态。
  压在海报背景图上的区域（总览横幅、横幅上方透明的顶栏）加全局类 `.on-dark`，前景一律浅色，是唯一允许写死浅色的地方。
  **背景剧照 / 毛玻璃**（2026-10-23，参考 MoviePilot 玻璃主题）：`layouts/AppBackdrop.vue` 在整站背后固定铺一张剧照（复用登录页的 `/auth/wallpapers`，每分钟换一张），有图时挂 `html.has-wall`：
  页面底与 `body` 透明，`.card` / 侧栏 / 顶栏 / 页签条 / 提示条半透明 + `backdrop-filter`，`--surface-secondary` / `tertiary` 改成前景色的淡淡一层（规则都在 `main.css`「背景剧照」一节）。
  玻璃要背后有东西才看得见：原来只给浮层加模糊、页面底是实色，等于没有。主题菜单里「背景剧照」可关，「换一张背景」手动切下一张（`theme.wallSkip`，切完重新计时）；没剧照（没配 TMDB）或系统要求减少透明度时不挂 `has-wall`，全站照旧实色。
  新加的「自己带底色、又不在卡片里」的块要在 `html.has-wall` 下补模糊，否则背后的画面清清楚楚压在字底下。
  Vue scoped 样式里**别写 `:global(html.has-wall) .x`**：它编译成只剩 `html.has-wall`，`.x` 被丢掉；直接写 `html.has-wall .x`（作用域属性只加在最后一段）。
  **毛玻璃**（2026-10-23）：顶栏、手机底栏、悬停浮出的侧栏、手机抽屉、菜单 / 下拉 / 气泡、弹窗、提示条用 `main.css` 的
  `--glass-bg`（浮层）/ `--glass-bg-bar`（贴边的栏）+ `--glass-blur` + `--glass-border` / `--glass-hi`，别各写各的模糊值；
  列表、卡片、表单不加（背后是纯色，看不出还耗性能）。系统要求减少透明度或不支持 `backdrop-filter` 时令牌自动退回实色。
  液态玻璃（SVG 位移滤镜折射）比较过、没用：Safari / Firefox 出不来折射，压在海报上字也难读。
  侧栏桌面端是 72px 图标栏、悬停浮出、图钉固定（`AppSidebar.vue` 的 `docked` / `pinned`）：展开状态由脚本判定，
  **两种状态下布局必须一致、只动宽度与透明度**，否则展开动画第一帧会跳位。
- **界面一律用 `webui/src/components/hero/`**（`HButton` / `HInput` / `HSelect` / `HModal` / `HTabs` …）：
  HeroUI 的 BEM 类负责外观，Reka UI 负责交互；Reka 的状态属性与 HeroUI 选择器之间的桥在
  `styles/hero-adapter.css`。要用 HeroUI 新组件时先在 `main.css` 按需 `@import` 它的 CSS（全量 400KB+，我们打单文件）。
  Naive UI 已于 2026-09 整体移除，**不要再加回来**。弹提示 / 确认框走 `useFeedback()`（`message.*` / `dialog.confirm`）。
  浮层（Popover / Select / Modal 等）在 Portal 里，外面还包着 Reka 的定位 wrapper，调用方的 scoped 样式选不中面板本身——
  面板样式写在 hero 组件的非 scoped 块里，或用全局类名。页面私有类名别和 HeroUI 块名（`.card` / `.chip` / `.tag` / `.input` …）
  或 Tailwind 工具类（`.outline`）同名。
  模板里用到的组件必须 import：vue-tsc 对未导入的组件**不报错**，只会原样渲染成自定义元素（整块内容铺在页面上）。
- **功能页顶部的一行状态条用 `components/ui/StatusStrip.vue`**（Strm 管理 `StrmOverview`、自动整理 `OrganizeOverview`）：只看不做，每段一个跳转链接，别往上加执行按钮（维护者要求）。自动整理的流水线（基础配置「运行方式」）是 `pages/organize/PipelineFlow.vue`，每步写当前设置、点了跳过去改，同样只读。
- 手机端断点 ≤720px：底部导航（`layouts/MobileTabBar.vue`）接管导航，内容区底部要给它留 `--tabbar-h`。
- **不要再做「高级设置」折叠**（2026-10-23 按维护者要求整体删掉：`AdvancedSection` 组件、`FieldRow` 的「非默认」圆点、`useSetting` 的 `changed` 一起删了）：选项一律平铺显示，藏起来的选项用户找不到，改过的更难排查。
- **API Key / Secret / token 一律用 `SecretInput` 组件**，不要直接写 `<input type="password">`——
  浏览器会把本站保存的管理员密码自动填进去（见 `webui/src/utils/autofill.ts`）。

---

## 6. 雷区

1. **115 风控**：所有对 `webapi.115.com` / `proapi.115.com` 的请求必须经过 `throttle115()`。
   读接口默认 1s 间隔、写接口 ≥3s。新增 115 调用时别绕过节流器。
2. **不要恢复被移除的网盘**：`removedCloudResource()`（`internal/api/pansou.go`）是本 fork 的核心改动点，
   按 `cloud_type` 标签 + 链接域名双重判定过滤 123 / 夸克 / 阿里。新增资源站接入时记得接上这个过滤。
   `upload115.go` 里的「阿里云 OSS」是 115 上传协议本身，**不是**阿里云盘，不要误删。
   **例外：阿里云盘分享秒传进 115**（`ali2115.go`，2026-10-24 维护者要求）：只是「搬进来」的来源，
   不挂载、不列目录、不生成 STRM、不播放；只收用户自己贴的链接（`resolveResourceLink` 在 `removedCloudResource` 之前认），
   资源站搜到的阿里云盘链接照旧丢掉。别借它把阿里云盘做成存储。
3. **优雅退出**：SIGTERM 后需要约 3 秒收尾（停 worker、冲刷通知队列）。直接杀进程会留下
   「115 已搬移、台账未写」的中间态，破坏后续去重与洗版判定。compose 的 `stop_grace_period` 必须 ≥ 该值。
4. **反代头不可信**：`r.SetTrustedProxies(nil)` 是有意为之——信任 XFF 会让登录防爆破被轮换头部绕过。
   若确实要部署在反代之后，显式把反代 IP 加进白名单，不要改回信任全部。
5. **凭据不入库**：`.gitignore` 已排除 `.localtest/`、`data/`、`*.db`、`*.log`。
   任何 115 Cookie、TMDB key、机器人密钥都只能落在 `/config` 或数据库，不进源码。
6. **别用 `gin.Default()`**：它自带的访问日志会让每个 HTTP 请求刷一行，实时日志页会被淹没。
   另：代码里的 `orphan*`（`orphan115.go`、`detect_orphans`、`/sync/orphans`）在界面和日志里一律叫
   **「失效 STRM」**，改这块时别把两套词混进用户可见的文案。
7. **只做更新检测，没有应用内自更新**：自更新链路（`selfupdate.go`、`update-finish` 子命令、Docker socket、
   企微「更 新」菜单）已整条删除 —— 远程更新要往容器里挂 `/var/run/docker.sock`，后台被攻破一次就等于
   宿主机 root。用户更新走 `docker compose pull && docker compose up -d`，**不要再把自更新加回来**
   （Watchtower 之类的外部方案也别往本站里接）。
   2026-09-30 起有**更新检测**（`update.go`，维护者明确只要到这一层）：版本号 = CI 里 `git describe --tags`
   注入的 `main.Version`（正式版 `v26.10.9`，同日序号版 `v26.10.9-2`，两版之间的 master 构建 `v26.10.9-3-gabc1234`，本地构建 `dev`；`parseVersion` 先比「年.月.日」三段，同一天再比序号，`v26.10.9-4` 比 `v26.10.9` 新，别按 semver 把 `-4` 当预发布），
   后台每 12 小时读一次 GitHub `releases/latest`（走全局代理），界面「系统配置 → 版本更新」与侧栏小点提示，
   可选推一次通知（`update_notified` 记账，同一版本只推一次）。dev / 裸提交号构建不做新旧比较。
   **发版 = 推一个 `v*` tag**：CI 出镜像后 `release` job 自动建 Release（`--generate-notes`）；同日序号版（`-2` `-3`）是正式版，`-` 后带字母的（`-rc1`）才按预发布发、不会提示给用户。
8. **整理与增量同步不再重叠**：整理是一条自带落盘的完整流水线（识别 → 搬移 → 写 STRM →
   刷 Emby → 本轮片目入刮削队列），产物**不经过**生活事件。整理用的 `pan115Ops` 打开了 `suppress`，
   自己做的每一次 move/rename/delete 以及新建目录（`mkdir` / `ensurePath`，否则 `new_folder` 会触发整目录递归遍历）都登记进 `EventSuppress`，绕回来时被增量同步 pop 掉跳过
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
    - 原生 `library.deleted` 也可能来自扫库清理，不能证明用户主动删除。仅接受 Movie / Episode / Season / Series；Folder、库容器及空/未知类型一律跳过：只记一行 `○` 日志，**不记拦截**（删一部片后 Emby 必然跟一条空片目的 Folder 事件，报成拦截会被读成删除失败）；拦截记录只留给越界校验失败的事件。
    - **一次删除只推一条消息**（`embydelnotify.go`）：标题按条目类型写（`🗑️ Emby 删除 · 电影 / 剧集 / 季 / 单集`），Folder 类目录条目不推；同一条目的几条事件（神医 deep.delete + 原生 library.deleted）按条目 id 聚合，等各自的深删跑完再发，深删结果（删了几个网盘文件 / 拦截原因 / 失败）写在同一条里。深删那边**不再单独推**「深度删除」「被拦下」（`runDeepDelete` 只给非 Emby 入口推），新增推送别绕开这里。本站让 Emby 删条目时打自产标记、回来的事件不推；**例外是 115 上真删了**（增量的删除事件，`notifyEmbyPanDeleted` 打 `pan` 标记）：精确路径命中时推「🗑️ 网盘删除 · 类型」；Series / Season 事件还认领它**下面**的网盘删除（`takeEmbyPanDeleted`：Emby 4.10 一次删几集只推一条 Series 事件「已移除了 X 中的 N 项」，2026-10-09 现场按精确路径对不上、一条没推），认领后降成普通回声标记，一份网盘删除只推一次，之后顺手收掉的上级季 / 剧集按回声不推；网盘上改名 / 移动清掉的旧位置照旧不推。电影/单集精确匹配 STRM；剧/季目录须通过台账布局验证，不能无条件展开前缀。
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

    另：本站删本地 STRM 后通知 Emby 用的 `embyDeleteItems`（`DELETE /Items/{Id}`）**同样会删盘上的文件**，
    而且对独占目录的影片删的是整个目录。护栏两条：被删路径确实不存在；文件型条目所在目录已经空了
    （`dirHasEntries`）。2026-09-29 重新整理原地改名，Emby 把刚写好的新名 STRM 连目录一起删了，就是缺了后一条。

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
    - 抢不到锁的状态必须说清**被谁占着、占了多久**：`taskMu.Describe()` / `Holder()`，队列面板与 `/tasks` 的 `lock` 字段据此显示「正在等谁」。
      原来的 `busyErr()` / `logBusy()` / `manualAcquireWait` / `organizeAcquireWait` 随入队改造删除 —— 手动与后台入口都不再在请求里等锁。
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
    - **任务队列的 worker 也是这把锁的使用者**（`taskqueue.go`）：它排队时登记等待，增量照常让路；
      反过来每跑完一个任务，若增量已超过两个周期没跑，worker 放锁且不登记，等增量跑完一轮再继续
      （`waitIncrWindow`，时间戳由 `runIncrPollTick` 的 `markIncrRun` 打）。否则一批几十条的重新整理会把增量整段饿死。
      **新增手动入口一律做成入队**（`enqueueJob` + `queuedReply`，执行器注册进 `jobExecutors`），别再在 HTTP 请求里 `Acquire` 同步跑。
      后台触发（定时整理、定时全量、转存 / 离线完成、转存守望者）同样入队；**只有增量轮询（`TryLockPolite`）与 Emby 事件深删（`deepdelemby.go` 自带重试）仍直接拿锁**。
      此前的 `organizeMissed` 每分钟补跑、转存触发「等 90 秒拿不到就放弃」、守望者「抢不到锁冷却 5 分钟」三套补救逻辑已删除：入队之后不存在错过。
    - 测试：`synclock_test.go`（锁与让路）、`taskqueue_test.go`（入队去重、顺序、结果、取消、重启中断、让路窗口）、`walkctl_test.go`（深度上限与中断）、
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
    - **谁更好**（`washquality.go` 的 `washCompare`，2026-10-20 起，维护者定的）：先比规则分（命中 `priority_level` 第几条，越靠上越高，没命中最低；MoviePilot pri_order / P115StrgmSub 同样「严格更高才换」），
      同分再按默认画质 分辨率 → 片源 → HDR → 视频编码 先后比（MediaSync115 `_score_video_file` 同序，不比体积）；没配规则只有后一层（原来是一律新替旧）。
      命中的那条里写了的字段最多按命中的值算（写 `HDR10+,HDR` 时 DV.HDR 按 HDR 算），逗号先后不参与；分辨率 / 片源 / 编码有一边没写不比这一项，HDR 没写算 SDR。
      全平不换（否则两份差不多的来回互顶、每次删一份网盘文件）。现场：规则写 `resource_pix: "2160p,1080p"`，库里 1080p WEB-DL 与新来的 2160p DV WEB-DL 同命中一条，原来平局不换。
      日志里的理由（「都命中第 3 条规则，分辨率：2160p > 1080p」）也出自它，改比法时一起改。
      **体积**（2026-10-23，维护者要的「4K 优先留小体积」）：规则可写 `resource_size`（命中条件 `<30G` / `5G-30G`）与 `size_prefer`（small / large：都命中这一条时**先比体积**再比默认画质，相差不到 5% 算一样大）。知道体积的地方走 `washCompareFiles`：整理洗版（新文件取 115 列出的大小、库内取 `SyncedFile.Size`）、订阅按集挑分享里的文件（`sharePickOpts.Compare` / `Beats` = `subPriority.compareFiles`）、订阅洗版和库里那份比；规则 1 写了 `size_prefer` 时命中第 1 条也不算洗完（`subPriority.settled`），按标题筛时同档的不下结论（`subWashTitleHopeless`）。只有名字的地方（找资源排名、订阅候选按标题排序、同集撞名）走 `washCompare`，体积两项不看。**订阅**（候选排序、分享里同一集几份挑哪份）2026-10-22 起走 `subPriority`（`subpriority.go`，就是 `washCompare`），**达标**（命中第 1 条规则）的排最前，都不达标先补集数再比画质（维护者定的；设计见 `docs/115-station-notes/SUB-WASH-PLAN.md`）；找资源页的排名与同集多份撞名（`resWashRank` / `washRankIn`）仍只看命中第几条。测试 `washquality_test.go` / `subpriority_test.go`。
    - **画质偏好 `prefer`（「简单设置」，2026-10-23，`washprefer.go` / `washapi.go`，设计见 `docs/115-station-notes/WASH-SIMPLIFY-PLAN.md`）**：分档规则「一条都没命中算最差」连默认配置都写反了（带 DV 的 4K、4K WEB-DL 都输给 1080p），所以默认改成偏好：不分档，一条比较线 不要的 → 分辨率 → DV（只在 `dv: prefer` 时）→ 片源 → HDR → 编码 → 偏好发布组 → 体积。策略写了 `prefer` 时 `normalizeWashStrategy` 把 `priority_level` 换成 `tiers()` 编出来的两档规则，**规则上挂着偏好（`washRule.prefer`）**，`washCompareFiles` / `washTierOf` 见到它改走偏好，订阅「命中前 N 条」、找资源「洗版首选」照旧按编出来的规则字段判，别把这个指针去掉。**简单设置与专家模式是同一份 YAML**：表单保存时后端生成 YAML（`washSimpleForm.yaml`），读时 `simpleFormOf` 认回来，认不回来只能在专家模式改，前端别另存表单。界面「排序预览 / 试一试 / 体检」是 `POST /scrape/wash/preview`，纯计算、判定走同一个 `decideWash`。没改过老默认的部署启动时由 `MigrateWashSimple` 换成新默认（按策略结构比，不看注释）。`max_size` / `min_size` 现在按名字的意思补 `size_prefer`。`ParseResourceInfo` 认发布组前会把最后一个「.」后面当扩展名去掉，没扩展名的名字认不出发布组，偏好发布组因此按词在名字里出现认（`teamHit`）。测试 `washprefer_test.go`。
    - **强制覆盖**（2026-10-23，`forceWashStrategy`）：机器人「强制覆盖 <115 分享链接>」（TG 也认 `/force`）转存时在来源链接上记 `DownloadLink.Force`，整理这批内容时（`orgCtx.forceFor` 按条目名 / 顶层条目名 `orgCtx.top` 认，与 `dlLinkMatch` 同口径，零 115 请求）不看配置的洗版策略：同一集（电影是目标标题目录里）的版本连同字幕全部让位，去向固定 115 回收站；新集照常入库，目标目录里已是同一份文件（sha1）的仍按已存在。识别、人工确认、同名停下、同次撞名这些照旧拦。只收分享：磁力的产物要等离线监视器回填才认得出来源。标记必须与链接同一次写入（`dlLinkRecordOpt`），否则守望者可能在中间按普通洗版整理掉。
    - **规则里一个词怎样算命中**（`matchField`，2026-10-22 起）走 `condTokenHit`：包含在解析出的归一值里，或在名字里单独出现（前后不挨字母数字）。原来是整名子串，`DV` 命中 `DVDRip`、`!TS` 排掉 `Ghosts`；订阅与整理同一套，别只改一边（两边判得不一样，订阅转进来的会被整理移进「已存在」）。
    - 测试：`washbatch_test.go`（批量、分组、去重、缓存、失效行）、
      `wash_regression_test.go` / `washflow_test.go` / `wash_recycle_test.go`。

14. **人工确认：停在识别之后、任何网盘写之前**（`orgconfirm.go`，开关是 `org-basic.manual_confirm`）：
    打开后 `processDir` / `processSingleFile` 识别完（含没识别出来的）只登记一条
    `status=awaiting` 的整理记录，文件原地留在待整理/转存目录，不搬、不改名、不查重、不洗版。
    - 确认入库**复用** `processDir` / `processSingleFile` 本体（`orgCtx.forced` 跳过识别），
      不要另写一条入库路径——洗版、去重、落盘、刮削必须和自动整理同一套。
    - 结果写回原来那条待确认记录（`orgSink.reuse`），不另起一行。
    - 后续每轮整理按 fid 跳过待确认条目（`loadAwaiting` / `dropHeld`），散文件的待确认记录
      挂着同前缀的其他集，这些 fid 同样算；转存守望者用 `countUnheld` 判断目录是否还有活，
      否则只剩待确认条目时会被当成「整理后仍未清空」反复重试直到熔断。
    - 开关关掉后，下一轮自动整理接手这些条目（`adoptAwaiting`），结果同样写回原记录。
    - 新增识别之后的分支时，记得在 `ctx.holdable()` 为真时先停下，别在停之前发网盘写请求。
    - **AI 判定停下的待确认是另一种**（`OrganizeRecord.HoldAI`，由 `ctx.aiHold` 按
      「AI 增强识别 → 判定后」的 off / auto / force 与分数线决定）：开关关着时**也不许**被
      `adoptAwaiting` 接手 —— 接手就是重新识别、再调一次模型、又停回来，每轮都烧一次模型调用。
      `dropHeld` 与 `processEntry` 两处都认 `ref.ai`，改这块别只改一处。
    - **同名停下的待确认是第三种**（`OrganizeRecord.HoldTwin`，2026-10-08 起）：`choose` 第一关里片名相等、得分也一样的
      不止一条（年份没帮上忙），记在 `TmdbMedia.Twins`（没海报也没简介的空壳不算），`ctx.twinHold` 不管「人工确认」开关都停，
      同样 sticky（`awaitingRef.twin`）。开关 `org-basic.no_twin_hold`（反着存，默认开）。**这种记录确认 / 改指定 / 重新整理都不写识别记忆**：
      名字里没年份，`recogKey` 是「片名|」，记下来另一部同名的内容会被永远认成这一部；所以 `HoldTwin` 确认之后也不清。
      网盘文件页的片目自动整理（`filelibrary.go`）与海报墙按目录名刮削（`localscrape.go`）碰到同名直接报错让用户指定，
      同步后自动刮削（`recognizeStrict`）不认。现场：凡人修仙传 2020 动画 / 2025 真人剧，真人版被认成动画顶掉了库里的集。
      注意它挡不住「名字里带了年份、年份却是假的」：订阅的包装目录名就带订阅那部的年份，所以订阅在挑资源时另有一道（`subtwin.go`）。
    - 测试：`orgconfirm_test.go`、`aiflow_test.go`、`twinhold_test.go`。

15. **STRM 文件名不带视频扩展名**（`strmname.go`，2026-09-28 起）：`xxx.mkv` → `xxx.strm`，
    这样网盘上同基名的 `xxx.nfo` / `xxx-thumb.jpg` / `xxx.chs.ass` 落到本地正好被 Emby 配对
    （Emby 把 STRM 的扩展名换掉去找配套文件）。「保留文件后缀」`keep_ext` 只管直链内容，不管文件名。
    - **别再手拼 `name + ".strm"`**：写入走 `writeStrm`（返回实际写成的相对路径，台账与 Emby 通知一律用它），
      推算走 `strmNameOf` / `strmRelOf`，按视频名反查本地产物走 `strmRelCandidates`（新旧两种都认）。
    - 同目录同基名的两个视频（`X.mkv` / `X.mp4`）后来的退回旧写法 `X.mp4.strm`（`strmNameFor` 按台账裁决，
      同一批写入由 `applySyncResults` 的 `claimed` 去重）；已落盘的沿用台账里的名字，不会来回变。
      因此**台账视频行剥掉 `.strm` 不一定带视频扩展名**，认视频看 `Kind == "video"`，别用 `classifyFile`。
    - 按名兜底删除时，新写法的路径可能属于同基名的另一个视频，删前过 `strmOwnedByOther`。
    - 存量由 `strmmigrate.go` 在 `SetupRoutes` 里、任何后台任务启动之前一次性迁移（Setting `migrate.strm_noext.v2`；
      v1 只改台账，本地从备份拷回旧名文件后就迁不动了）：先把台账旧名行改新名，再**以台账为准对账本地**
      （`reconcileStrmArtifacts`）——旧名 STRM / 配套文件改新名，新名已有就删旧名那份；台账里有的（网盘镜像）不碰。
      有失败不打标记下次重试；本地一个 STRM 都看不到（挂载未就绪）时拒绝迁移。迁完一小时再对账一遍：
      Emby 会替还没清掉的旧条目按旧路径补存 `xxx.mkv-thumb.jpg`（2026-09-28 现场）。
      迁完 6 小时内，迁移动过的路径的 Emby 入库 / 原生删除事件按回声处理（`strmMigrateEcho`：不推通知、不深删）。
    - 附属文件跟着视频改名时，名字里残留的视频扩展名段要去掉（`trimVideoExtLead`：`xxx.mkv-thumb.jpg` → `新名-thumb.jpg`）。
    - 测试：`strmname_test.go`、`strmmigrate_test.go`、`strmwrite_test.go`。

16. **刮削单独一条队列，不拿 `taskMu`**（`taskqueue.go` 的 `scrapeLane`、`localscrape.go`、`scrapecore.go`，2026-09-28 起）：
    此前整理后刮削在整理任务里当场跑，一部几百集的综艺刮完才放锁（每集剧照），整理 / 同步全在排队。
    - 两个入口（整理后 `flushScrape`、海报墙勾选）都是 `scrape` 任务，执行器只有 `execScrapeJob` 一个。
      「开始刮削」全库（`/scrape/run`）已于 2026-09-29 删除，**别加回来**：开着 Emby 提前探测时一次全库就是成千上万次 115 直链请求。
      海报墙开着「轨道探测」且所选视频 > 100 个时，前端确认两次后带 `confirm_probe`，后端 `probeConfirmNeeded` 再守一道（409）。
      整理后刮削去重键 `auto`，还没开始的几轮用 `jobSpec.Merge` 取并集（默认的「同键覆盖」会丢掉上一轮的片目）。
    - 与整理并行的冲突**靠刮削自己收拾，不加锁**：刮削不建目录（`fileScrapeWriter.put` 遇到不存在的目录返回 `errMetaDirGone`，
      别再加 `MkdirAll`，那会在旧位置造出只有 NFO 的空壳）；每刮完一部 `scrapeCompensate` 核对 STRM 还在不在，
      不在就收回这次写下的文件并收空目录。没用片目锁是因为删改本地的入口有十几处（增量拿着 `taskMu` 删），让它们等刮削又会卡住主队列。
    - **整理后要刮的，Emby 刷新交给刮削任务**（`flushScrape` 先于 `flushRefresh`，参数 `EmbyRefresh` / `EmbyVerify`）：刮完只刷一次，
      刮削出错 / 被停 / 什么都没写也照刷（`execScrapeJob` 开头的 defer）。只在刮削队列空闲时交接（`scrapeLaneIdle`），前面排着别的刮削就整理当场刷。
      2026-09-28 现场：整理刷一次、11 秒后刮完又刷一次，两分钟后 Emby 连目录条目都没建。
    - **增量同步后自动刮削**（2026-10-03 起；2026-10-15 起按库：只刮「自动刮新片」的库，原开关 `scrape.auto_after_sync` 只用于换算，见模块地图「刮削方式」）：增量本轮**新写**的 STRM（`applyStats.Written` 与零遍历直推里 `wrote` 的，改名回声不算）
      按 `titleOf` 归片目，`enqueueSyncScrape` 入同一个执行器，去重键 `sync`（与整理后的 `auto` 分开，`isAutoScrapeKey` 两个都认：不另建探测任务、空转不留行），Emby 刷新照整理那套交接。
      一轮最多 `syncScrapeMaxTitles`（50）部，分类目录外的不刮。**全量不接**（首次全量就是整库刮削），存量交给媒体信息补全。
      目录名没有 TMDB 编号时走 `recognizeStrict`：只认 `choose` 第一关（标题 / 原名相等），不走识别记忆与 AI —— 同步来的目录是用户自己命名的，认错一次 NFO 就钉进 Emby（维护者明确要求）。测试 `syncscrape_test.go`。
    - 刮削期间**不许调 `beginTask` / `endTask` / `setJobProgress`**（那是 `taskMu` 持有者的全局状态），进度走 `scrapeLane.set` / `setSub`。
    - 刮削不探测轨道、NFO 不写 streamdetails：Emby / Jellyfin 导入 NFO 不读它，播放时自己探测、还会把 NFO 整份重写。
      原来的 ffprobe「媒体补全」（缺画质信息时探测改名）与 NFO 轨道探测已于 2026-09-29 一并删除，镜像也不再带 ffmpeg，别加回来。
      界面上的「轨道探测」（`scrape.probe_streams` / 刮削任务的 `Probe`）现在控制 **Emby 提前探测**（`embyextract.go`）：
      入库确认（`embyVerifyIngest`）后与手动探测任务里，把路径排进单 worker 队列，对 Emby 里还缺媒体信息（视频 + 音轨不足两条，LitePan 同判据）
      的 Movie / Episode / Video 逐个 `POST /Items/{id}/PlaybackInfo`，一次一个、间隔 3 秒 —— 每次都经 302 取一次 115 直链。
      直接打 Emby 本身，别走本站反代（会被直连改写 / 直链预取拦下）。
      **禁止重复探测**（维护者硬性要求，重复探测 = 重复取 115 直链）：同一条目会从多条路进队列（入库确认可能两次、
      目录与 `.strm` 两种路径），只靠「已有媒体信息就跳过」挡不住失败 / 超时的。所以按 Emby 条目 id 落库记账（`EmbyExtractMark`）：
      **发请求前**先记一次（手动的也记），成功删账；连续 3 个失败熔断暂停 30 分钟（手动的也等）。规则按入口分两种（`embyExtractAllowed` 的 `manual`）：
      **自动**（入库确认）同一条目最多 2 次、间隔 ≥24 小时，用完**永远**不再自动探（2026-09-29 起不再 30 天清账重来，维护者认为多此一举）；
      **手动**不看次数与 24 小时，只防抖 `embyExtractDebounce`（5 分钟）。
      自动入口**只探新入库的集**（2026-10-05 起，`embyExtractTitleTargets`）：入库确认的样本只有 3 个 `.strm`，回答不了「这一轮新进了哪几集」，
      所以 `writeStrm` 每真写出一个 STRM 就登记进内存（`embyfresh.go` 的 `markFreshStrm`，6 小时），入库确认按 `libCategoryLayout.titleOf` 归到片目后
      取走这个片目下登记的那些去探（`takeFreshStrms`），一个片目一个任务，同键排着的路径取并集。**别改回整部探**：往一部 1665 集、大部分没探过的番剧里
      加一集就排进去一千多集（2026-10-04 现场）。登记没有（重启丢了）时只探确认到的那个 `.strm`，目录不探；片目之上的目录**不自动探**；
      不属于任何片目的单个 `.strm` 只探它自己。重新整理 / 指定季集登记的手动探测要在归片目**之前**摘（`splitRedoProbes`），指定季集按文件登记、只探改过的几集。整理后自动刮削（`scrapeAutoDedupe`）不排队（入库确认已覆盖）。
      **手动刮削收尾的 Emby 刷新不走入库回查**（`scrapeEmbyRefresh` 的 `ingest`，只有整理交过来的刷新才算入库）：
      回查确认后会按全局开关自动探，弹窗里关掉的「轨道探测」会被它顶回来（2026-09-30 现场）。
      新增入口别绕过 `embyExtractClaim`。测试 `embyextract_test.go`。
      **「Emby 读不了这个文件」不计入熔断**：探测期间本站成功给过直链（`playbackLinksServed`）、Emby 却没给出轨道，是文件里有 Emby 内置 ffprobe 5.1 不支持的编码（2026-10-02 xHE-AAC 音轨），
      这时读一次 Emby 日志（`embyprobelog.go`，官方接口 `GET /System/Logs/embyserver.txt`，要管理员 API Key）按直链里的 pickcode 找出 ffprobe 报错，把哪条轨道、什么原因写进失败原因；
      前端 `probeErrAdvice` 按失败原因原文配说明，两边文案要一起改。熔断暂停在下一个请求前才等（`embyExtractPauseWait`），别改回原地等 —— 任务会卡住、停止也叫不醒。
      **定时补全建的探测任务是第三种**（`metafill.go`，`probeJobParams.Auto` → `queueEmbyExtractJob(id, true, …)`）：挂着任务 id（任务中心看得到、能停），
      放行却走自动规则（`embyExtractAutoJobs` 记着哪些任务是自动规则的，队列条目的 `manual` 只由手动任务决定）。
      **别改成手动规则**：定时每天都跑，按手动规则（只防抖）就是每天把探不成的条目再取一次 115 直链。
      用户在任务中心点「重试」这种任务时按手动规则（`RetryTaskJob` 清掉 `Auto`），和失败清单里的「重试」同口径。
      **多版本**（同一片目目录两个 `.strm`）在 Emby 里是一个条目、`MediaSources` 各一份：「有媒体信息」要每个版本都齐（`lackingSources`），
      探测时逐个缺的版本带 `MediaSourceId` 各发一次（不点名时 Emby 只探它自己挑的那个，另一个永远「未探测」），记账仍按条目记一笔；
      卡片快照因此必须带 `MediaSources` 字段，片目详情按版本拆行（`embyDetailsOf`）。测试 `embyextract_versions_test.go`。
      **2026-09-30 实测的 Emby 是另一种形态**：同目录两个 `.strm` 是两个独立的 Movie 条目（各带一个 `MediaSources`），界面上合成一部可切版本；
      对其中任何一个发 PlaybackInfo，返回的都是**整组**版本、主版本排第一。所以只要知道版本 id 就点名 `MediaSourceId`（单版本条目也点），
      结果一律按 id 认（`playbackSourceStreams`），**别再「单版本取第一个」**：那会拿早就探过的主版本轨道 0 秒报成功，缺的那个永远没探。
      多版本时 Emby 存图写 `视频名-poster.jpg` 并删掉 `poster.jpg`，海报墙与刮削的「已有」都要认它（`perVideoImage`）。
      Emby 在这种目录里**不用**目录级的 poster / fanart / clearlogo / landscape，所以刮削对多版本电影（`titleRun.multiVersion`）按视频名各写一份：
      本地已有的（目录级那张、别的版本那张）直接拷，都没有才下载（同一张只下一次），各版本都齐了再收掉目录级的（台账里有的网盘镜像不删）；
      海报墙同口径（`perVersionArt`：每个版本都有才算有）。测试 `scrapemulti_test.go`。
      **光盘结构（ISO / BDMV / VIDEO_TS）不探**，同样按版本判（`probeSources` / `discSource`：一个 ISO 版本一个 mkv 版本时 mkv 照探）。
      STRM 名不带扩展名之后（§6.15）文件名上认不出 ISO，所以 **ISO 的直链不看「保留文件后缀」一律带 `.iso`**（`writeStrmNamed`，qmediasync 同款），
      判断时除了 Emby 给的路径还读一次本地 STRM 里的直链（Emby 记的直链要等它重扫才更新）。存量由 `strmiso.go` 启动时一次性补：
      直链里带原文件名的本地改写；`pick_code` 且关了保留后缀的本地认不出，入队一次全量同步（固定快速模式，不可用时自动降级），全量对「还没带 `.iso` 的 ISO」无视「跳过已存在」照写。测试 `strmiso_test.go`。
    - **手动探测一律是任务**（`embyprobejob.go`，kind=`probe`，第三条队列 `probeLane`，不拿 `taskMu`、不占刮削队列）：片目详情「提前探测」、
      海报墙刮削勾「轨道探测」（刮完另建任务，不再挂在刮削任务上）、重新整理（带刮削且开着探测：`registerRedoProbe` 登记片目，
      入库确认到它时 `splitRedoProbes` 从自动入口摘出来建任务；原地刷新没新 STRM 的直接建）、任务中心失败清单「重试」（`item:<id>` 点名条目）。
      执行器只把路径带任务 id 排进同一个 worker 然后等结果（别另起第二个探测者），停止时 `cancelEmbyExtractJob` 摘掉排着的。
      每个条目的结果写 `TaskJob.Probe`（`embyprobereport.go`），任务状态：全失败 `failed`、部分失败 `partial`。
      防抖期内重试任务后端直接 409 说清几点能试（`probeRetryReadyAt`），前端倒计时。**没有定时重试**，文案别写成「会自动重试」。
      入库回查查到 10 分钟，再晚的由 Emby 入库 webhook 补确认（`embyLateIngestHit`）。入库确认排的自动探测（2026-10-04 起）也建任务：一个片目一个（只含新入库的集）、`Source=ingest`、按自动规则放行（`enqueueIngestProbes`，与定时补全同一套 `Auto`），一个都没请求就按 `Idle` 删行不进历史；**别改回直接塞 worker**，一部上千集的番剧要探一个多小时，没任务就看不到进度、停不下来。探测失败的条目另有任务中心的「Emby 探测」页签（`GET /tasks/probe`，列记账里所有没成功的条目，可逐条 / 全部手动重试；条目在 Emby 里没了顺手删账）。**「忽略」只打 `EmbyExtractMark.IgnoredAt`、不删账**（删了自动次数清零，下次入库确认又要请求两次）：忽略的不再列出、不再自动探，手动请求时清掉标记（`embyExtractClaim`），再失败回到清单；`POST /tasks/probe/ignore` / `unignore`。
    - 任务状态 `partial`（部分失败）：执行器返回 `jobOutcome.Partial`。刮削有出错的产物或没刮成的片目（含中途片目被挪走）就是部分失败；停止优先于它。可重试，保留 30 天。
    - 占位剧照（`scrape.skip_shared_stills`，默认开）**只在同一季内**判：同季 ≥3 集共用 still_path 或内容 sha1 相同。
    - 测试：`scrapelane_test.go`（不等锁、分队列排位、合并、不建目录、事后收拾、占位剧照按季）。

17. **同一次整理里撞名：交给用户选，不替用户去重**（`orgdup.go`，2026-10-06 起）：
    同一集的粤语 / 英语两份，命名模板不带语言就改出同一个名字；115 遇到同名会把后到的悄悄改成 `xxx(1).mkv`，
    记录 / 台账 / STRM 就和网盘对不上。所以 `processDir` 在洗版逐份判定之后、**任何改名搬移之前**按「落点目录 / 新名」分组（`settleCollisions`）：
    - sha1 相同 = 同一份文件，不问，多余的移冗余；
    - 不同的文件撞名 → 这几份连同字幕 / 集 NFO 原地不动，登记一条 `HoldDup` 的待确认记录（`DupGroups`：区别词、洗版排名、推荐），
      同条目其他集照常入库，**源目录不收拾**（有残留会被连同内容搬进冗余）。洗版判输的照旧进「已存在」，不会被问。
    - 推荐只在洗版策略分得出高下时给；分不出（多半只差语言）就不推荐，**别改成默认替用户挑**（维护者明确要求）。
    - 用户的选择（`DupChoice`：fid → keep / drop / A..Z，`POST /organize/records/:id/dup`）走「确认入库」的执行器，
      `orgCtx.dupChoice` 让 `processDir` 放行：不要的移冗余，都留的改名 `xxx#A.mkv` / `xxx#B.mkv`（推荐的那份是 A，其余按体积）。
      只有留下的那份（几份）去让旧版让位；几份都留时让位的台账行去重。新增网页 / 机器人入口一律调 `submitDupChoice`。
    - 字母记在 `orgRecordFile.Variant`，重新整理按 fid 沿用（`planRedoLayoutWith`）；`parseFileName` 剥掉结尾的 `#A`；
      STRM 直链 `?/文件名` 里的 `#` 转义成 `%23`。
    - `HoldDup` 和 `HoldAI` 一样是 `sticky`：「人工确认」开关关着也不被 `adoptAwaiting` 接手；普通「确认入库」与批量提交拒收没选过的。
    - 通知与机器人（`orgdupbot.go`）：停下时 `notifyDupHold` 推一条，TG 带按钮（Chat ID 是个人私聊才带：回调只认配置的私聊），
      企微写回复指令「多份 <编号> <选择>」（TG 私聊同样认，`/dup`），飞书 / QQ 只能指向网页。
      TG 按钮数据 `d:<记录 id>:<组|*>:<操作>` **认数据库里的记录，别改成找资源那种 10 分钟内存会话**：几个小时后点也得管用；
      多组逐组点的进度只在内存（`dupBotPicks`），点全了才落库入队。**谁先选算谁的**：机器人与超时 `submitDupChoice(…, override=false)`
      只认还没选过的，网页可以改选。超时 `org-basic.dup_auto_hours`（默认 0 = 一直等）+ `dup_auto_action`（keep_all / recommend），
      每分钟由调度器的 `dupAutoTick` 查，处理了推一条通知。
    - 重新整理同一套（`planRedoLayoutOpt`）：先算出每个视频的落点再 `settleCollisions`；**早就同名并排、不需要改名的放行**
      （维护者定的，`strict` 只给库内体检用），要改名才撞上的返回 `redoDupError`：任务失败，`holdRedoDup` 把各组记到记录上、
      这次要整理成的条目记进暂存指定；用户在记录上选完，`submitDupChoice` 对非待确认的记录入「重新整理」队列，
      执行时 `po.choice` 生效：不要的（含字幕 / 集 NFO）移冗余、本地产物第 3 步按 fid 收掉，都留的按 `#A` `#B` 改名。
    - **和库里已有的撞名**（新文件改名后与库内某份同名、不是同一份文件、洗版也没把它换掉）同样停下来问（维护者定的）：
      `libraryClashes` 按台账的 STRM 名比（网盘文件名台账没存；新旧两种 STRM 写法都认，这次洗版让位的不算），
      查到的库内那份以 `InLibrary` 成员进组。选「只留新的」→ `dropLibraryCopies` 走 `applyWashPlans` 让位（去向固定冗余），
      「都留」→ 库内那份不动、占着 A，新的从 `#B` 起；「只留库里的」→ 新的移冗余。散文件（`processSingleFile`）也走这一套：
      同一集几份散文件是一个一个整理的，后一个就撞上库里刚进的那份。同前缀兄弟（`organizeIdentifiedFile`）还没接。
    - **库内同集多份体检**（`orgdupscan.go`，整理记录页「库内同集多份」）：只读台账、零 115 请求，按「目录 + 视频基名」分组
      （STRM 新旧写法、115 自动加的 `(1)` 都归一，`#A` `#B` 已分好的不算），找出早就并排躺在库里的几份。
      整理记录里两份都登记了的可以「处理」：`strictDup` 严格重新整理一次（同名并排的也停下来），之后在记录上选；没记录的引导到网盘文件页对片目「整理」。
    - 测试 `orgdup_test.go` / `orgdupbot_test.go` / `redo_dupname_test.go` / `orgdupscan_test.go`。

18. **RE0 网页登录（CloakBrowser）**（`re0browser.go` / `re0web.go`，2026-10-14 起）：
    RE0 不再开放 OpenAPI 申请，新用户只能用账号密码在浏览器里真实登录（站点做了会话绑定，接口不能脱离浏览器调）。
    页面上的做法参考 p115strmhelper `helper/hdhive/browser.py`（只读，Go 重写），下载 / 验签 / 启动参数对照 npm `cloakbrowser` 包装层（MIT）。
    - **浏览器不进镜像**：CloakBrowser 二进制「free to use, no redistribution」，由用户的容器从官方地址下载到 `DATA_DIR/cloakbrowser`。
      运行镜像因此换成 debian-slim（它只有 glibc 构建），Dockerfile 里装的是它要的系统库。**别把浏览器打进镜像**。
    - **不反复下载**（维护者要求）：装好写 `.verified` 标记；启动、保存 RE0 配置、打开 RE0 来源时 `ensureCloak` 只在没装时下（幂等、下载中不重入）；
      中断留 `.part` 带 Range 续传；版本号写死（`cloakVersionX64` / `Arm64`），改版本前先核对 GitHub 上真有这个 release，换版本后旧目录自动删。
      校验一律先验 SHA256SUMS 的 Ed25519 签名与 `version=` 行，再核 sha256，**别加「跳过校验」**。
    - 访问 GitHub 走全局代理，配了 GitHub Token（setting `github`，「系统配置 → 代理」）就走 API 下载（`github.go`，更新检测也用它）。
    - 浏览器常驻、空闲 10 分钟关，**用户数据目录与指纹种子都持久化**，只有被跳回 `/login` 才登录（插件每次都重登，订阅每轮都查会很慢、也容易触发风控）；
      换账号清掉用户数据目录（`re0ResetProfile`）。所有页面操作串行在 `re0OpMu` 上。点按钮一律 rod 的 Click（站点校验 isTrusted）。
    - **过 Cloudflare 的关键是别让 rod 改指纹**（2026-10-14 实测）：`rod.New().NoDefaultDevice()` 必须带着 —— rod 默认给页面套「笔记本」设备，
      UA 被改成 Mac 上的 Chrome 114，和 CloakBrowser 的 Windows / Chrome 146 指纹打架，验证框点了也永远重置；去掉后点一次、约 10 秒就过。
      rod 那串默认参数（关站点隔离、NetworkServiceInProcess 等）也删掉了，别加 `--disable-gpu`。
      验证框在封闭 Shadow DOM 里，Playwright 的 `get_by_role` 都看不见（官方包装层自己也过不了这一步），要按无障碍树找、按坐标点；
      开着站点隔离时它是独立 target，只能 `TargetAttachToTarget` + `PageFromSession` 发裸鼠标事件（`PageFromTarget` 对 iframe 报错）。
    - 站点没登录时是页面加载完才在前端跳 `/login`，判断登录态要等（`re0AwaitAuth`），别一打开就下结论。
    - 每次失败把截图与 HTML 存到 `DATA_DIR/cloakbrowser/debug`（留最近 20 个文件），排查先看这里。
      `CLOAKBROWSER_BINARY_PATH` 可以指定现成的浏览器（同官方包装层），调试用，不下载。
    - 网页通道**也有文件预览**（2026-10-22，`re0webpreview.go`；此前误以为没有，订阅一律不付费解锁）：资源页解锁前就显示文件列表，数据来自 `GET /api/customer/resources/{slug}/file-list`，在已登录的资源页里借站点的 `signedFetch` 调（`re0WebSignedJS`，与发布分享同一个），返回按 OpenAPI file-list 的字段认，订阅据此走和 OpenAPI 一样的付费解锁判定。**没读到预览（浏览器 / 登录 / 返回格式认不出）一律包成 `errRe0NoPreview`，订阅跳过、不退回按标题判**：看不到文件就不花积分（维护者定的），网页通道出错多半是站点改版、是整体性的，退回标题判会悄悄开始花积分。返回格式还没用真账号核对过，第一次跑看日志 `[RE0网页] 文件预览读到 N 个文件` 或返回原文。
    - 只做每日签到，不开放赌狗签到（同 OpenAPI 时的口径）。
    - 测试 `re0web_test.go`（RSC 解析、签到提示、分享链接、通道选择）/ `re0browser_test.go`（解压、装好判定、种子）；签名校验用的是 `testdata/cloak/` 里的真清单。
      页面流程没有自动化测试，站点改版后从日志 `[RE0网页]` 看卡在哪一步。
    - **癫影共用这个浏览器**（`dianweb.go`）：进程、用户数据目录、`re0OpMu` 都是同一份，但不点页面——打开首页后注入 `dianSignJS`，
      用站点自己的签名（`/auth/browser-challenge` 拿 proof、`/auth/browser-session` 登记 ECDSA 公钥、每个请求签 `portal-browser-request/v1`）直接调 `/api/portal/*`。
      私钥读写站点自己的那条 IndexedDB（`portal-browser-security-v1`），**别改成每次新生成**：会话可能绑定这把钥匙。
      站点前有 Cloudflare WAF：Go 直接发请求连静态 JS 都会 403，无头 UA 带 HeadlessChrome 时站点自己也回 `browser required`，所以必须走浏览器。
      站点的 OpenAPI 只给审批过的开发者（封包 SDK），普通用户用不了。站点改版先跑 `dian_live_test.go`，再看站点前端 `/assets/index-*.js` 里的接口。

---

## 7. 常见任务入口

| 我要… | 从哪看起 |
|---|---|
| 加一个 API 接口 | `internal/api/routes.go` 的 `SetupRoutes` |
| 加一张表 | `internal/model/model.go` + `InitDB` 的 AutoMigrate |
| 加一个环境变量 | `internal/config/config.go` 的 `Load()` |
| 改文件名识别/解析 | 片名/年份/季集在 `tmdb.go` 的 `parseFileName`，画质串在 `resource.go`，TMDB 候选校验在 `tmdbmatch.go`。**搜索结果不许再「取第一条」**：每条候选都要过 `choose` 的片名校验（标题/原名相等 → 别名译名相等 → 包含且年份相符），对不上就判未识别。改动前后跑 `recognize_corpus_test.go`，语料在 `testdata/recognize_corpus.txt`（解析）与 `testdata/recognize_match.json`（假 TMDB + 期望条目），新踩到的坑样本往里加。文件名只有集号时季号是缺省的 1（`SeasonGuessed`），目录名上明写的季号要经 `applySeasonHint` 盖过它，新增按文件解析季号的地方记得一起调用（目录里的每一集统一走 `parseVideoInDir`：替换规则 → 所在子目录季号 → 条目季号）。路径上下文合并在 `recognizepath.go`；目录条目里每一集的落点在 `orgseason.go`（`episodeParses` 每集只解析一次，洗版判定 / 改名 / 按季分组搬移 / 落盘同读这一份，**别再退回「按主视频算一个季目录整包搬」**，那会把多季合集全挤进一季）；NFO / 图片按剧 / 季 / 集三级分落点（`placeMeta` / `metaRel`：集名.nfo、集名-thumb.jpg 跟视频，season.nfo 进季目录，tvshow.nfo 与 poster 进标题目录，重新整理同口径），`orgSink.commit` 的附属文件一律落在传入的 mediaRel，剧级的由调用方以 rootRel 单独提交，本地与网盘必须同布局；容器目录（剧名/Season N）直属的剧级元数据由 `adoptContainerMeta` 在子条目处理完后搬进标题目录；全剧连续编号的集号换算（`S07E166` → `S07E22`）在 `absepisode.go`，参考项目都不自动换，这里只在 TMDB 各季集数能坐实时才换，判定条件见文件头，测试 `absepisode_test.go` / `orgseason_test.go`；季范围 `S01-S07` / `第1-7季` 是合集标志、不是第 1 季；双集文件（`S04E01-02` / `E01-E02` / `第1-2集`）记在 `ParsedName.EpisodeEnd`，只认相邻两集（MoviePilot 同口径），改名写成 `S04E01-E02`、集 NFO 一个文件放两段 `<episodedetails>`，洗版与单集文件互不替换，要展开集号用 `episodeList()`；识别记忆在 `recogmemory.go`（`RecognizeMemory` 表，**只在人工改指定时写**，别改成自动识别也写，会把一次误识别固化下去；管理界面是识别规则页下方的 `webui/src/components/organize/RecognizeMemoryCard.vue`）。其余测试 `recognize_test.go` / `paren_test.go` / `eprange_test.go` |
| 改重命名模板变量 | `internal/api/rename.go`（变量体系与 CMS 对齐） |
| 改洗版规则 | `internal/api/wash.go` + `model.InitDefaultWashConfig`；画质偏好 / 简单设置 / 预览接口在 `washprefer.go` / `washapi.go`，前端 `webui/src/pages/organize/WashTab.vue` + `WashPreview.vue`（专家模式仍是 `YamlRuleTab`）；两份比谁好在 `washquality.go`（§6.13）；默认 YAML 首次写入，已有配置（含空配置）不覆盖，保存后立即失效缓存。**判定（`decideWash`）与执行（`applyWashPlans`）是分开的**，改之前先读 §6.13 |
| 接一个新资源站 | 站点的配置 / 登录 / 底层搜索照 `re0.go` 或 `mukaku.go` 写；再在 `transferhub.go` 的 `resSources` 加一个适配（`ready` 说明为什么不能用、`search` 返回 `ResourceItem`，归一与过滤由 `resNormalize` 统一做）；前端只加一张配置卡片 `webui/src/components/transfer/sources/*.vue` 挂进 `pages/transfer/SettingsTab.vue`（`SOURCES` 加一项），搜索与提交不用改 |
| 改资源订阅 | 先读模块地图「资源订阅」一行与 `docs/115-station-notes/SUBSCRIBE-PLAN.md`；缺集在 `submissing.go`、挑资源在 `subpick.go`、分享按集挑在 `shareepisode.go`、执行在 `subrun.go`、结算排期在 `subsettle.go`、接口在 `subapi.go`；前端 `pages/transfer/SubsTab.vue` + `composables/subscriptions.ts` + `components/subscribe/` |
| 加一个通知通道 | `internal/api/notify_extra.go` |
| 改前端页面 | `webui/src/pages/` 下对应的页面组件；路由表在 `webui/src/router/index.ts` |
| 改总览面板 | `internal/api/dashboard.go`（数据）+ `webui/src/pages/DashboardPage.vue`（界面）。**Emby 计数别再改回不带 `IncludeItemTypes`**，见 §6.11；台账校准在 `internal/api/medialib.go` + `webui/src/components/dashboard/CalibrateModal.vue` |
| 改任务中心 | `webui/src/pages/TasksPage.vue`（页签容器，`?job=` 打开任务详情；探测状态在这里统一轮询，给角标和两个页签用）+ `webui/src/pages/tasks/`（`JobsTab.vue` 进行中：概况 / 进行中 / 最近结束 5 条；`HistoryTab.vue` 历史：按天分组、筛选、分页，打开时认一次 `?status=`；`ProbeTab.vue` Emby 探测失败清单：重试 / 忽略；`ShareOutTab.vue` 分享记录（转存后分享到 RE0 / 癫影）：按状态筛、失败 / 跳过的手动重试、发成的在站上打开；`HistoryRow.vue` 两处共用的已结束任务行；`JobDetail.vue` 详情弹窗）。页签值 `jobs` 沿用旧名，别改（收藏的 `?tab=jobs` 要能打开）；状态 / 类型 / 来源文案在 `webui/src/utils/jobStatus.ts`。后端 `internal/api/taskhistory.go`。顶栏轮询的 `GET /tasks` 别拿来查历史 |
| 改整理记录页 | `webui/src/pages/tasks/RecordsTab.vue`（在任务中心的「整理记录」页签；原在自动整理页，旧地址 `/organize?tab=records` 由 `organize` 路由的 `beforeEnter` 重定向，别删）+ `webui/src/components/organize/RedoDialog.vue`（TMDB 搜索复用 `/tmdb/search`；`mode=confirm` 时用于待确认条目改指定）。筛选栏角标、页签角标与侧栏 / 手机底栏「任务中心」上的待确认角标共用 `stores/recordStats.ts`（布局层在队列任务结束时刷新）。`?job_id=` 只看某个任务涉及的记录。**那一行上有两个删除按钮**：「深度删除」删网盘真文件，垃圾桶图标只删记录，改动时别把两者的文案/样式拉近 |
| 改 Strm 管理页（`/sync`） | `webui/src/pages/SyncPage.vue`：顶部一行状态条 `strm/StrmOverview.vue`（增量 / 全量 / 失效 STRM / 深度删除 四段，零 115 请求；别再做成大卡片，维护者嫌占地方）+ 下面四个配置页签（`webui/src/pages/strm/`：配置 / 全量 / 增量 / 深度删除）。状态条只看不做（维护者要求，别往上加执行按钮）；「开始全量」「立即同步」在各自页签里。全量 / 增量 / 深度删除三个页签一律「状态 → 记录 → 设置」从上往下（2026-10-23）：顶部一行概况 `components/strm/TabSummary.vue`（执行按钮放在它右边），中间是记录（全量：各库结果 + 最近全量；增量：最近事件 + 最近轮次 + 默认收起、有异常自动展开的「诊断」；深删：删除记录占主栏），设置放最下面 / 右栏，逻辑在 `strm/useStrmRuns.ts`（未保存确认、快速模式可用性）。全量配置与深度删除配置由 SyncPage 各持有一份传下去，总览和页签共用 |
| 改同步定时 | `internal/api/cron.go`：三条线 —— 自动整理 cron（`incr.cron`）、增量独立轮询（`incr.interval_sec`，默认 30 秒）、全量 cron（服务于失效 STRM 检测）。三者共用 `taskMu`（见 §6.12）；整理与全量的 cron 命中时**只入任务队列**（`runScheduledTick` / `runScheduledFullSync`，后台优先级、各自去重），由 worker 排队执行，不存在「错过」。**`incr.cron` 与 `incr.interval_sec` 同一个 setting key，界面却分在两个页面上**（cron 在「自动整理 → 基础配置」，间隔在「Strm 管理 → 增量同步」）：历史上两件事绑在一条 cron 上，增量拆成独立轮询后 key 没动。前端两侧都要走 `webui/src/composables/incrSetting.ts` 的 `patchIncrCfg` 只改自己那个字段，整存整取会互相覆盖 |
| 改整理落盘 / 刮削触发 | `internal/api/orgstrm.go` 的 `orgSink`（`commit` / `flushScrape` / `flushRefresh`）；`flushScrape` 只入刮削队列 |
| 改演职人员补全（Emby 人物头像 / 中文名） | `internal/api/embypeople.go`（配置、执行器、Emby 读写、按名字对号 `matchCredit`、记账）+ `personname.go`（中文名挑选、繁转简、`PersonMeta` 缓存）；前端 `webui/src/components/scrape/PersonFillPanel.vue`。NFO 演员带 `type` / `thumb` / `tmdbid`、名字用缓存的中文名，在 `scrape.go` 的 `nfoActorsOf` / `nfoCrewNames` |
| 改影视刮削页（刮削配置 / 轨道探测 / 两个补全任务的配置） | `webui/src/pages/ScrapePage.vue`（三个页签：`scrape/ScrapeTab.vue` 刮削、`scrape/MediaTab.vue` 轨道探测 + 媒体信息补全、演职人员补全）+ `components/scrape/`。刮削配置（setting `scrape`）整存整取，读写一律走 `pages/scrape/scrapeConfig.ts` 的 `useScrapeConfig`；「轨道探测」`probe_streams` 存在刮削配置里但显示在「媒体信息」页签。2026-10-04 从「自动整理」拎出来：自动整理的「影视刮削」页签（`organize/ScrapeSummary.vue`）**只读、不放能改的开关**，改要跳过来 —— 两页各存一份会互相覆盖（`incr.cron` 同款坑） |
| 改刮削本身（NFO / 图片 / 日志 / 占位剧照 / 探测） | `internal/api/scrapecore.go`（`titleRun`：逐产物日志、片目内进度、`episodeStills`）+ `scrape.go`（配置、NFO 结构、接口）+ `localscrape.go`（执行器、`scrapeCompensate`）。先读 §6.16 |
| 改整理记录 / 重新整理 | `internal/api/orgrecord.go`；路径推导在纯函数 `planRedoLayout`、原地刷新判定在 `isInPlaceRedo`，配套测试 `orgrecord_test.go`。**没改过名的文件（记录里无 `Orig`，即未识别 / 失败的）必须走 `redoParseVideo` → `parseVideoInDir`**，和正常整理同一套（替换规则 → 子目录季号 → 条目目录季号，子目录取自 `orgRecordFile.Dir`）；只用 `parseFileName` 的话各季同名的 E01.mkv 会撞名。记录里的文件若已出现在**更新的**整理记录里（用户挪走另行整理了），重新整理要剔除（`dropClaimedFiles`），否则会把它们搬回来、还按 fid 删掉别处刚生成的 STRM；带集号的剧集里没集号的视频进特别篇、保持原名（`specialsRel`，与正常整理同口径）。它们先按 TMDB 第 0 季集名对号（`specialmatch.go` 的 `matchSpecialsTmdb`，思路同 Sonarr 的 ParseSpecialEpisodeTitle：中文集名再英文集名，泛称 / 占位 / 太短 / 剧名本身 / 年份差一年以上 / 平手 / 两个文件抢一集都不认），认出的记成 S00Exx 照集模板改名；第 0 季带集号（含明写的 S00Exx）一律进特别篇目录（`isSpecialEpisode`），目录季号与主视频季号都不能盖过它。散文件（单独转存的特别篇）在 `processSingleFile` 里同样对号，散文件与同前缀兄弟的落点统一走 `singleFileTarget`。换了标题目录时，本地旧标题目录里刮削写的 NFO / 图片不在台账里，由 `purgeStaleTitleDir` 收掉（还有 STRM / 台账行就不动），整棵收干净才按目录通知 Emby 一次；删 STRM 顺带删集 NFO / 剧照见 `removeStrmCompanions`。重新整理也洗版（2026-10-19，`planRedoWash`）：落点上同集的别的版本输给这次的文件就让位，记录自己的文件不算库内版本；只让位、判输的不移「已存在」（用户点名要入库），指定季集自己判过不重复。**改 `redoOrganize` 前先读它的步骤注释**：算布局 → 动网盘 → 删旧本地产物 → 落盘，这个顺序是有来由的，破坏性动作必须排在计算之后 |
| 改网盘文件页（整理 / 移动所选） | 后端 `internal/api/filebrowser.go`（列目录、定位、入队参数）/ `fileorganize.go` / `filelibrary.go`；前端 `webui/src/pages/FilesPage.vue` + `webui/src/components/files/`。新增会写网盘的分支记得走 `ops.*`（抑制、缓存失效） |
| 改海报墙（片目卡片 / 手动刮削 / 片目详情） | 后端 `internal/api/locallib.go`（列表、状态、海报 / 背景图缩略图）/ `localscrape.go`（入队与执行、产物出口）/ `localdetail.go`（详情、Emby 媒体信息与探测状态）；前端 `webui/src/pages/LocalFilesPage.vue`（筛选与批量栏固定、海报墙单独滚动；手机退回整页滚动 + 底部浮条）+ `webui/src/components/local/`（`ScrapeDialog` / `TitleDetail` 抽屉 / `DetailFiles` / `DetailEmby`），轨道文案在 `webui/src/utils/mediaInfo.ts`。上传只在用户这一次勾了才能用 `upload115FileConsented` |
| 改工作目录配置 | `internal/api/workspace.go`：媒体库 / 转存/待整理 / 已存在 / 冗余四个目录分存在 `full` / `share` / `org-basic` 三个 setting 里。**转存与待整理是同一个目录**（2026-10-12，`stagingdir.go`）：只存 `share.folder`，`loadOrgConfig` 拿它当扫描根，`org-basic.pending` 不再读、`SaveSetting` 时剥掉；老配置由启动时的 `MigrateStagingDir` 合并（只配了待整理 → 搬进 `share`；两个都配了且不同 → 以转存为准，旧待整理记进 setting `staging_legacy`，「账号与媒体库 → 媒体库」顶上（`components/accounts/StagingLegacyAlert.vue`）提示一次，不动网盘）。转存触发 / 守望者与定时整理扫同一个目录，靠 `taskMu` 先后跑、后跑的空转，别为此加去重。媒体库表（V2）下它不再能在界面上改（每个库各设转存文件夹，`composables/stagingDir.ts` 已删）；网盘文件页里它的角色叫 `pending`。`SaveSetting` 保存这三个 key 时经 `guardWorkspaceSetting` 校验互不包含（只查改动过的槽位、每目录至多一次祖先链请求，cid 未变零请求）；**已存在 / 冗余只在「自动整理 → 基础配置」显示和改**（只有整理用得上；媒体库页签不再显示，新手清单也跳到那里），一键创建（基础配置与新手清单）走 `InitWorkspaceDirs`，在网盘根建 `/StrmStation/{转存,已存在,冗余}`，只补未配置的。测试 `workspace_test.go` / `stagingdir_test.go` |
| 改空目录清理 | `internal/api/emptydir.go` 的 `pruneEmptyDirTree` / `pruneOrMove`；守卫见 §6.8 |
| 改深度删除 | `internal/api/deepdel.go`：执行在 `runDeepDelete`、事件范围在 `deepDelEventRows`、守卫在 `checkLibRoots`、网盘空目录在 `pruneDeepDelDirs`；Emby 事件那条线在 `deepdelemby.go`。**先读 §6.10 再动**，配套测试 `deepdel_test.go` / `deepdelemby_test.go`；整体设计与 Emby 事件的真实载荷见 `docs/115-station-notes/DEEP-DELETE-PLAN.md` |
| 想知道旧版某功能怎么做的 | 查 Git 历史中的 `web/`；现役实现在 `webui/` |
| 查某个 Emby 接口怎么调 | `docs/emby-api/ENDPOINTS.md` 按路径 grep，再点进 `services/` 看参数、`schemas/` 看返回结构；查询经验与雷区在 `README.md` 和 `guides/browsing.md`。规范不是全集（如 `/Items` 的 `IsVirtualItem` 没列但可用），新踩的坑补进 `_gen/notes.txt` 后重新生成 |
| 查某个 115 接口怎么调 | `docs/115-station-notes/REFERENCES.md` 的「115 接口实现」，再到 `p115client/client.py` 或 `115driver/pkg/driver/` 里 grep |
| 做同步/整理类功能 | `docs/115-station-notes/REFERENCES.md` 的「STRM 同步类项目」，里面有五个项目的策略对比 |
| 动增量同步任何一环 | 先读 `docs/115-station-notes/INCR-SYNC-UPGRADE.md` —— 2026-09 那轮改造的完整记录：每处改动的原因、与其他项目的逐项对比、踩过的坑、当时验证到什么程度。`§0 速查` 里有文件职责表、新增配置项、以及「改造自己引入的两笔债」是怎么还的 |
| 增量同步没反应 / 要排查 | 界面「Strm 管理 → 增量同步 → 事件流状态」卡片（门禁、通道、游标、上一轮结果、积压量、**当前任务锁**、**重放检测**），或直接打 `GET /sync/incr-status`；「测试事件流」按钮是纯读探针，随便点 |
| 「一直在轮询 / 反复扫同样的目录」 | 日志按轮次号 `[同步#N]` 对比相邻两轮：内容一样就是重放。看「本轮账单」那行的结尾（消费了没有、为什么没消费）与「回退遍历 N 个目录 ← 哪条事件带来的」清单。机制见 §6.12，代码在 `incrtrace.go` |
| 「整理/转存半天不动」 | 顶栏「任务队列」面板（排第几、在等谁、后台任务在跑什么），或 `GET /tasks`；`GET /sync/incr-status` 的 `task_lock`：谁占着、占了多久、谁在排队。定时整理与转存触发的整理都在队列里，排着就是在等锁（面板上写明在等谁）；守望者因连续未清空而熔断时日志有 `[守望] ⚠ 连续 N 次未能清空转存目录`。机制见 §6.12 |

---

## 8. 前端（`webui/`）

`web/` 的原生实现已被 `webui/`（Vue 3 + TypeScript + Vite）**整体替换**，
动因是原生版本没有暗色模式、228 个内联 `onclick` 导致无法安全重构，且视觉停留在早期
企业后台风格。13 个页面全部迁移完成。2026-09 组件层从 Naive UI 换成 HeroUI v3 样式 + Reka UI，
并补齐手机端（≤720px 底部导航、弹窗变底部抽屉、表单单列）。

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
