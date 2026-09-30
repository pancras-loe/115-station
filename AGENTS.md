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
| 前端（现役） | Vue 3 + TypeScript + Vite（`webui/`），外观走 HeroUI v3 的样式包 `@heroui/styles`，交互走 Reka UI，详见 [webui/README.md](webui/README.md) |
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
| **整理流水线** | `organize.go` `org115.go` `orgstrm.go` `orgrecord.go` `emptydir.go` `resource.go` `rename.go` `wash.go` `scrape.go` `tmdb.go` `airecognize.go` | 识别 → 分类 → 洗版 → 重命名 → 搬移 → **写 STRM / 下附属 → 刮削 → 刷 Emby**（一条龙，见 §6.8）；`resource.go` 是文件名结构化解析的核心，`orgstrm.go` 是落盘出口，`orgrecord.go` 是整理记录与「重新整理」，`airecognize.go` 是模型接口与两个提示词（改写片名 / 从候选里挑），`airecogflow.go` 是 TMDB 全部搜索策略都落空后的 AI 这一环（改写 → 搜 → 挑、打分 `aiScore`、要不要停下 `aiHoldReason`；界面「AI 增强识别」） |
| **任务队列** | `taskqueue.go` `taskjobs.go` `taskjobsync.go` `taskstage.go` `taskprogress.go` `taskhistory.go` | **四条队列**：主队列（下面这些）串行在 `taskMu` 上；**刮削队列**只跑 `kind=scrape`（外加几十秒就扫完的 `metafill`）、**探测队列**只跑 `kind=probe`、**人物队列**只跑 `kind=person`（演职人员补全），都不拿 `taskMu`（见 §6.16）。进度按队列各存一份（`jobLane`，主队列沿用 `setJobProgress` 等包级函数，刮削显式用 `scrapeLane`），刮削还有第二级进度 `progress.sub`。**所有手动任务**（重新整理 / 确认入库 / 忽略 / 深度删除 / 手动整理 / 全量 / 手动增量 / 机器人「整理」「同步」）**入队立即返回（202）**；**后台任务**（定时整理 / 定时全量 / 转存与离线完成触发的 `transfer` / 转存守望者）也只入队，优先级 1（排队中手动优先，运行中不抢占），同类去重（`organize` / `full` / `transfer`），空转轮次（`jobOutcome.Idle`）跑完删行、同因重复失败只留最新一条。常驻 worker 串行执行（每个任务单独拿放 `taskMu`），`TaskJob` 表存状态与历史（仍不进队列的只剩增量轮询与 Emby 事件深删，后者在 `endTask` 时补一行 `kind=background`；取代原来内存里的 `recentRuns`）；结构化进度 `setJobProgress`（旧的 `SetTaskProgress` 同时写进当前任务）；前端是顶栏 `TaskQueuePanel.vue`（只看当前）+ `stores/queue.ts`，完整的历史、筛选与任务详情在**任务中心** `/tasks`（`taskhistory.go` 的 `GET /tasks/history` 与扩充后的 `GET /tasks/:id`；`OrganizeRecord.JobID` 记「最近一次处理它的任务」，任务与记录据此互相跳转，老记录为 0 不回填）。整理记录可先**暂存指定**（`OrganizeRecord.Pending*`，`PUT /organize/records/:id/pending`），勾选后 `POST /organize/records/submit` 统一入队（`taskstage.go` 的 `planSubmit` 决定怎么拆）。设计见 `docs/115-station-notes/TASK-QUEUE-PLAN.md` |
| **网盘文件页** | `filebrowser.go` `fileorganize.go` `filelibrary.go` | 浏览 115 目录树（`GET /files/115`），每行「整理 / 移动」，勾选后批量移动，都入任务队列（`orgpick` / `libredo` / `filemove`）。**媒体库里只有片目目录（当前二级分类目录的下一层，`libCategoryLayout.isTitleRel`）能整理和移动**：整理不走新文件流水线（洗版会撞上自己），而是现场列文件建一条记录交给 `redoOrganize`（`libredo`）；移动只能到 冗余 / 已存在 / 待整理，移出媒体库时 `cleanupMovedTitle` **先删台账再删本地、最后通知 Emby**（反过来深度删除会按台账把刚移走的网盘文件删掉），并清空记录的 `target_cid`（否则之后重新整理被判原地刷新）。整理复用 `processEntry`，媒体库内的条目拒收（走重新整理）。**刮削不在这一页**（2026-09 挪到本地文件页）。测试 `filebrowser_test.go` |
| **本地文件页** | `locallib.go` `localscrape.go` `localdetail.go` `localemby.go` | 本地媒体库的片目卡片墙（`GET /local/titles`）：一张卡片 = 台账里一个片目（`scanLedgerTitles`），状态与详情抽屉同口径（`inspectLocalTitleDetail` + `grade`：片目级 / 季 / 每集 NFO、海报、背景图都在才算刮全，剧照 / 季海报只进 `Soft` 提示），**零 115 请求**，列表 30 秒缓存（刮削任务结束时 `forgetLocalTitles`）。卡片上的「未探测 N」来自 `localemby.go`：**打开页面时**（`GET /local/titles/emby-stats`）分页读一次 Emby 全库媒体流、按路径归到片目，一分钟内复用，没有后台定时拉取。海报缩略图走公开路由 `GET /local/poster`（`<img>` 带不了登录态）：列表按 key 签 HMAC（JWT 密钥），`underRoot` 防 `../`，缩成 400px 宽 JPEG 放内存缓存。刮削 `POST /local/scrape` 入队（`scrape`，参数在 `jobParams.Local`），核心是 `scrape.go` 的 `scrapeTitleMeta` + `fileScrapeWriter`，产物写本地媒体库。「上传到网盘」只管这一次，走 `upload115FileConsented`（不看监控上传总开关）；**没勾时不登记上传指纹**，传不传照常由监控上传决定。形态参考 LitePan 的海报墙（PolyForm Noncommercial，只看思路），但片目边界与类型取台账，不在目录里写标记文件。**卡片只放要紧的，细节进详情抽屉**（`?title=` 打开）：「刮削文件」`GET /local/titles/detail` 列出每个产物在不在（片目级 / 季 / 每集，季目录判定复用 `scrapeSeasonDirs`，零 115 请求）；「媒体信息」`GET /local/titles/emby` 显示 Emby 的音视频字幕轨道与每个条目的提前探测状态（`embyProbeStateOf`，与 `embyExtractAllowed` 同口径，把「为什么没探」说出来）；`POST /local/titles/probe` 单片目提前探测，走同一条队列与记账，不开后门。测试 `locallib_test.go` / `localscrape_test.go` / `localdetail_test.go` |
| **播放链路** | `proxy.go` `embyproxy.go` `embylibrary.go` `emby_notify.go` `embyextract.go` | 302 代理、Emby 反代与建库、入库后让 Emby 提前探测媒体信息 |
| **影视转存** | `transferhub.go` `transfertg.go` `guanying.go` `pansou.go` `mukaku.go` `re0.go` `tgsearch.go` `tgsub.go` | 按影片聚合各资源站（2026-09-30 起）：选定 TMDB 条目后前端对每个来源各发一次 `GET /transfer/resources/:source`（慢的盘搜不拖住别的），后端统一成 `ResourceItem`：画质标签（`ParseResourceInfo`）、相关性（`resRelevant`：片名 / 原名 / TMDB 别名被标题包含，电影再看年份）、洗版偏好排名（`resWashRank`，命中洗版策略第几条规则）、提交过没有（`DownloadLink.Hash`，零 115 请求）。提交一律 `POST /transfer/submit` → `submitResource`，里面才调 `shareReceiveCore` / `offlineSubmitCore`（它们自己登记来源链接），**别另起提交通道**。RE0 解锁花积分，只接受带 `confirm=true` 的请求，机器人不走解锁。机器人找资源（TG 与企微共用 `botflow.go`：`botFind` / `botAct` 管会话，渲染成与通道无关的 `botView`，TG 编辑原消息、企微发文字）也走 `searchResources` / `botSubmitResource`：`/search` 全部来源服务端并发（`botResourcesAll`，40 秒不等、`resDedupe` 去重），`/gy` `/wp` 单来源；每页 8 条、`0` 自动择优（`bestIndex`）、提交后会话保留、同一条只提交一次（`botFlow.sent`）。第五个来源「TG 频道」（`transfertg.go`）复用 `tgsearch.go` 抓公开网页 `t.me/s/频道?q=`，频道清单在 setting `tgsearch` 的 `channels`（TG 关键词订阅也回落到它，别另起 key），并发 3、最多 20 个频道，全部抓不到才报错。四个站点文件只剩配置 / 登录与底层搜索函数；前端 `webui/src/pages/MediaTransferPage.vue`（找资源 / 来源设置 / 链接转存三个页签，旧 `?tab=gy` 等带到来源设置，`/upload-download?tab=download` 带到链接转存）。链接转存页一次可贴多个链接，按链接形态抓（`composables/transferLinks.ts`，ed2k 文件名可能带空格、115 分享文案一段话里带访问码），逐个 `POST /transfer/submit`；转存目录的设置也在这一页。「转存后整理」开关已去掉：守望者每分钟接管转存目录，关了也会整理。测试 `transferhub_test.go`；另有 TG 抓取与关键词订阅（`tgsearch.go` / `tgsub.go`） |
| **通知** | `notify.go` `notify_extra.go` `medianotify.go` `wecombot*.go` `wecomcrypto.go` | 企微双向机器人（AES 验签）、TG / 飞书 / OneBot / QQ 官方、入库通知防抖聚合 |
| **演职人员补全** | `embypeople.go` `personname.go` | 扩展功能里的定时任务（`kind=person`，人物队列，零 115 请求）：按片目列 Emby 的 People，给缺头像 / 名字不是中文的演员、导演、编剧补 TMDB 头像、中文名、中文简介。**一律走 Emby API**（`POST /Items/{id}/Images/Primary` 与整条 DTO 回写 `POST /Items/{id}`，改过的字段加进 `LockedFields`）：Emby 的人物头像在它自己的 `<programdata>/metadata/people/首字母/姓名/`，还在库里记着图片标签，往目录里丢文件不生效。TMDB 人物 id 取人物自带的，否则拿片目 credits（剧集 `aggregate_credits`）**按名字精确对**，歧义就不补；**别改成 `/search/person` 按名字搜**（同名的人太多，认错一次就钉死）。中文名取 zh 翻译 → 中文原名 → 别名，OpenCC 繁转简。`PersonMeta` 缓存 TMDB 人物（刮削写 NFO 演员名也读它，与 Emby 改过的中文名对得上，否则重刮一次 Emby 又按英文名建新人物）；`EmbyPersonMark` 记账没补全的人物（查无结果 30 天、出错 1 天）。片目按加入时间排序，单次上限 / 停止 / 中断时把停下的片目序号存成续扫断点（setting `personfill_cursor`），下次从断点扫到末尾再绕回开头，看满一圈归零。头像、名字列表里就看得出，只缺中文简介的人物另按 id 批量读简介（`embyOverviews`）。**读人物一律走列表查询 `/Items?Ids=`，别改回 `/Users/{uid}/Items/{id}`**：后者对没刷新过的人物会让 Emby 当场去 TMDB 拉元数据，连不上 TMDB 时卡到超时。中文名已被另一个人物条目占用时只补头像不改名。参考 MoviePilot 官方插件 personmeta（只读）。测试 `embypeople_test.go`（假 Emby + 假 TMDB 端到端） |
| **媒体信息补全** | `metafill.go` | 扩展功能里的定时任务（`kind=metafill`，挂在刮削队列上，不拿 `taskMu`，扫描本身零 115 请求）：本地文件页口径（`localTitlesSnapshot` + `grade`）挑出没刮全的片目，建一个刮削任务（只补缺失、不上传、不探测）；分页读 Emby 条目（`walkLocalEmby`，与卡片快照同一个遍历）挑出缺媒体信息的视频，**按条目点名**（`item:<id>`）建一个**自动规则**的探测任务（`probeJobParams.Auto`）。两道闸：单次上限（片目数 / 视频数，探测按条目点名所以上限是准的）；补刮记账 `MetaFillMark`（按片目 key 记当时缺什么，缺的没变就 30 天内不再刮）。探测不另记账，`EmbyExtractMark` 已经管着。建出的两个任务 id 写进结果的 `follow_jobs`，任务中心那一行两个都显示（`jobFollowsOf`）。前端 `webui/src/components/plugins/MetaFillModal.vue`。测试 `metafill_test.go` |
| **其他** | `dashboard.go` `medialib.go` `offline.go` `dllink.go` `covergen.go` `checkin115.go` `imgcache.go` | 仪表盘、**媒体库台账校准**、离线下载、**来源链接**（整理记录的「来源」）、媒体库封面生成、115 签到、**界面图片缓存**（`/tmdb/img` `/poster` `/embyimg` `/local/poster` 共用：落盘 `DATA_DIR/imgcache`、同图并发合并、失败记 5 分钟、60 天未用自动清；拉不到图回 404 让前端 `PosterImage.vue` 显示占位，别再回透明 GIF。界面上的海报一律用 `PosterImage`，TMDB 尺寸按显示宽度约两倍挑） |

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
> 不靠翻记录 —— 同步失败由提交处当场回复（网页提示 / 机器人回复 / TG 订阅推通知），
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
- 颜色只能从设计令牌取，组件里写死色值必然漏暗色模式。**颜色的唯一来源是 HeroUI 主题变量**
  （`--accent` / `--surface` / `--muted` / `--foreground` …，明暗由它按 `html.dark` 切换）；
  `main.css` 里的 `--c-*` 只是给老样式用的别名，新代码直接写 HeroUI 变量。
  圆角用 `--r-sm/lg/xl/card`，**不要**写 `--radius-sm/lg/xl`——那几个名字被 HeroUI 的 `@theme` 占用、刻度也不同。
- **界面一律用 `webui/src/components/hero/`**（`HButton` / `HInput` / `HSelect` / `HModal` / `HTabs` …）：
  HeroUI 的 BEM 类负责外观，Reka UI 负责交互；Reka 的状态属性与 HeroUI 选择器之间的桥在
  `styles/hero-adapter.css`。要用 HeroUI 新组件时先在 `main.css` 按需 `@import` 它的 CSS（全量 400KB+，我们打单文件）。
  Naive UI 已于 2026-09 整体移除，**不要再加回来**。弹提示 / 确认框走 `useFeedback()`（`message.*` / `dialog.confirm`）。
  浮层（Popover / Select / Modal 等）在 Portal 里，外面还包着 Reka 的定位 wrapper，调用方的 scoped 样式选不中面板本身——
  面板样式写在 hero 组件的非 scoped 块里，或用全局类名。页面私有类名别和 HeroUI 块名（`.card` / `.chip` / `.tag` / `.input` …）
  或 Tailwind 工具类（`.outline`）同名。
  模板里用到的组件必须 import：vue-tsc 对未导入的组件**不报错**，只会原样渲染成自定义元素（整块内容铺在页面上）。
- 手机端断点 ≤720px：底部导航（`layouts/MobileTabBar.vue`）接管导航，内容区底部要给它留 `--tabbar-h`。
- **API Key / Secret / token 一律用 `SecretInput` 组件**，不要直接写 `<input type="password">`——
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
    - **一次删除只推一条消息**（`embydelnotify.go`）：标题按条目类型写（`🗑️ Emby 删除 · 电影 / 剧集 / 季 / 单集`），Folder 类目录条目不推；同一条目的几条事件（神医 deep.delete + 原生 library.deleted）按条目 id 聚合，等各自的深删跑完再发，深删结果（删了几个网盘文件 / 拦截原因 / 失败）写在同一条里。深删那边**不再单独推**「深度删除」「被拦下」（`runDeepDelete` 只给非 Emby 入口推），新增推送别绕开这里。电影/单集精确匹配 STRM；剧/季目录须通过台账布局验证，不能无条件展开前缀。
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
    - 测试：`orgconfirm_test.go`、`aiflow_test.go`。

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
    - 两个入口（整理后 `flushScrape`、本地文件页勾选）都是 `scrape` 任务，执行器只有 `execScrapeJob` 一个。
      「开始刮削」全库（`/scrape/run`）已于 2026-09-29 删除，**别加回来**：开着 Emby 提前探测时一次全库就是成千上万次 115 直链请求。
      本地文件页开着「轨道探测」且所选视频 > 100 个时，前端确认两次后带 `confirm_probe`，后端 `probeConfirmNeeded` 再守一道（409）。
      整理后刮削去重键 `auto`，还没开始的几轮用 `jobSpec.Merge` 取并集（默认的「同键覆盖」会丢掉上一轮的片目）。
    - 与整理并行的冲突**靠刮削自己收拾，不加锁**：刮削不建目录（`fileScrapeWriter.put` 遇到不存在的目录返回 `errMetaDirGone`，
      别再加 `MkdirAll`，那会在旧位置造出只有 NFO 的空壳）；每刮完一部 `scrapeCompensate` 核对 STRM 还在不在，
      不在就收回这次写下的文件并收空目录。没用片目锁是因为删改本地的入口有十几处（增量拿着 `taskMu` 删），让它们等刮削又会卡住主队列。
    - **整理后要刮的，Emby 刷新交给刮削任务**（`flushScrape` 先于 `flushRefresh`，参数 `EmbyRefresh` / `EmbyVerify`）：刮完只刷一次，
      刮削出错 / 被停 / 什么都没写也照刷（`execScrapeJob` 开头的 defer）。只在刮削队列空闲时交接（`scrapeLaneIdle`），前面排着别的刮削就整理当场刷。
      2026-09-28 现场：整理刷一次、11 秒后刮完又刷一次，两分钟后 Emby 连目录条目都没建。
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
      自动入口排的一律是**片目目录**（`embyExtractTitleTargets`，按 `libCategoryLayout.titleOf` 归）：入库确认的样本只有 3 个 `.strm`，
      直接排就只探 3 集；没点名时回查的是刷新目标（全量的同步根、迁移时的整个媒体库），直接排就是递归探整片库 ——
      所以片目之上的目录**不自动探**，不属于任何片目的单个 `.strm` 只探它自己。整理后自动刮削（`scrapeAutoDedupe`）不排队（入库确认已覆盖）。
      **手动刮削收尾的 Emby 刷新不走入库回查**（`scrapeEmbyRefresh` 的 `ingest`，只有整理交过来的刷新才算入库）：
      回查确认后会按全局开关自动探，弹窗里关掉的「轨道探测」会被它顶回来（2026-09-30 现场）。
      新增入口别绕过 `embyExtractClaim`。测试 `embyextract_test.go`。
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
      多版本时 Emby 存图写 `视频名-poster.jpg` 并删掉 `poster.jpg`，本地文件页与刮削的「已有」都要认它（`perVideoImage`）。
      **光盘结构（ISO / BDMV / VIDEO_TS）不探**，同样按版本判（`probeSources` / `discSource`：一个 ISO 版本一个 mkv 版本时 mkv 照探）。
      STRM 名不带扩展名之后（§6.15）文件名上认不出 ISO，所以 **ISO 的直链不看「保留文件后缀」一律带 `.iso`**（`writeStrmNamed`，qmediasync 同款），
      判断时除了 Emby 给的路径还读一次本地 STRM 里的直链（Emby 记的直链要等它重扫才更新）。存量由 `strmiso.go` 启动时一次性补：
      直链里带原文件名的本地改写；`pick_code` 且关了保留后缀的本地认不出，入队一次全量同步（固定快速模式，不可用时自动降级），全量对「还没带 `.iso` 的 ISO」无视「跳过已存在」照写。测试 `strmiso_test.go`。
    - **手动探测一律是任务**（`embyprobejob.go`，kind=`probe`，第三条队列 `probeLane`，不拿 `taskMu`、不占刮削队列）：片目详情「提前探测」、
      本地文件页刮削勾「轨道探测」（刮完另建任务，不再挂在刮削任务上）、重新整理（带刮削且开着探测：`registerRedoProbe` 登记片目，
      入库确认到它时 `splitRedoProbes` 从自动入口摘出来建任务；原地刷新没新 STRM 的直接建）、任务中心失败清单「重试」（`item:<id>` 点名条目）。
      执行器只把路径带任务 id 排进同一个 worker 然后等结果（别另起第二个探测者），停止时 `cancelEmbyExtractJob` 摘掉排着的。
      每个条目的结果写 `TaskJob.Probe`（`embyprobereport.go`），任务状态：全失败 `failed`、部分失败 `partial`。
      防抖期内重试任务后端直接 409 说清几点能试（`probeRetryReadyAt`），前端倒计时。**没有定时重试**，文案别写成「会自动重试」。
      入库确认排的自动探测没有任务可挂，靠任务中心的「Emby 探测」页签（`GET /tasks/probe`，列记账里所有没成功的条目，可逐条 / 全部手动重试；条目在 Emby 里没了顺手删账）。**「忽略」只打 `EmbyExtractMark.IgnoredAt`、不删账**（删了自动次数清零，下次入库确认又要请求两次）：忽略的不再列出、不再自动探，手动请求时清掉标记（`embyExtractClaim`），再失败回到清单；`POST /tasks/probe/ignore` / `unignore`。
    - 任务状态 `partial`（部分失败）：执行器返回 `jobOutcome.Partial`。刮削有出错的产物或没刮成的片目（含中途片目被挪走）就是部分失败；停止优先于它。可重试，保留 30 天。
    - 占位剧照（`scrape.skip_shared_stills`，默认开）**只在同一季内**判：同季 ≥3 集共用 still_path 或内容 sha1 相同。
    - 测试：`scrapelane_test.go`（不等锁、分队列排位、合并、不建目录、事后收拾、占位剧照按季）。

---

## 7. 常见任务入口

| 我要… | 从哪看起 |
|---|---|
| 加一个 API 接口 | `internal/api/routes.go` 的 `SetupRoutes` |
| 加一张表 | `internal/model/model.go` + `InitDB` 的 AutoMigrate |
| 加一个环境变量 | `internal/config/config.go` 的 `Load()` |
| 改文件名识别/解析 | 片名/年份/季集在 `tmdb.go` 的 `parseFileName`，画质串在 `resource.go`，TMDB 候选校验在 `tmdbmatch.go`。**搜索结果不许再「取第一条」**：每条候选都要过 `choose` 的片名校验（标题/原名相等 → 别名译名相等 → 包含且年份相符），对不上就判未识别。改动前后跑 `recognize_corpus_test.go`，语料在 `testdata/recognize_corpus.txt`（解析）与 `testdata/recognize_match.json`（假 TMDB + 期望条目），新踩到的坑样本往里加。文件名只有集号时季号是缺省的 1（`SeasonGuessed`），目录名上明写的季号要经 `applySeasonHint` 盖过它，新增按文件解析季号的地方记得一起调用（目录里的每一集统一走 `parseVideoInDir`：替换规则 → 所在子目录季号 → 条目季号）。路径上下文合并在 `recognizepath.go`；目录条目里每一集的落点在 `orgseason.go`（`episodeParses` 每集只解析一次，洗版判定 / 改名 / 按季分组搬移 / 落盘同读这一份，**别再退回「按主视频算一个季目录整包搬」**，那会把多季合集全挤进一季）；NFO / 图片按剧 / 季 / 集三级分落点（`placeMeta` / `metaRel`：集名.nfo、集名-thumb.jpg 跟视频，season.nfo 进季目录，tvshow.nfo 与 poster 进标题目录，重新整理同口径），`orgSink.commit` 的附属文件一律落在传入的 mediaRel，剧级的由调用方以 rootRel 单独提交，本地与网盘必须同布局；容器目录（剧名/Season N）直属的剧级元数据由 `adoptContainerMeta` 在子条目处理完后搬进标题目录；全剧连续编号的集号换算（`S07E166` → `S07E22`）在 `absepisode.go`，参考项目都不自动换，这里只在 TMDB 各季集数能坐实时才换，判定条件见文件头，测试 `absepisode_test.go` / `orgseason_test.go`；季范围 `S01-S07` / `第1-7季` 是合集标志、不是第 1 季；双集文件（`S04E01-02` / `E01-E02` / `第1-2集`）记在 `ParsedName.EpisodeEnd`，只认相邻两集（MoviePilot 同口径），改名写成 `S04E01-E02`、集 NFO 一个文件放两段 `<episodedetails>`，洗版与单集文件互不替换，要展开集号用 `episodeList()`；识别记忆在 `recogmemory.go`（`RecognizeMemory` 表，**只在人工改指定时写**，别改成自动识别也写，会把一次误识别固化下去；管理界面是识别规则页下方的 `webui/src/components/organize/RecognizeMemoryCard.vue`）。其余测试 `recognize_test.go` / `paren_test.go` / `eprange_test.go` |
| 改重命名模板变量 | `internal/api/rename.go`（变量体系与 CMS 对齐） |
| 改洗版规则 | `internal/api/wash.go` + `model.InitDefaultWashConfig`；默认 YAML 首次写入，已有配置（含空配置）不覆盖，保存后立即失效缓存。**判定（`decideWash`）与执行（`applyWashPlans`）是分开的**，改之前先读 §6.13 |
| 接一个新资源站 | 站点的配置 / 登录 / 底层搜索照 `re0.go` 或 `mukaku.go` 写；再在 `transferhub.go` 的 `resSources` 加一个适配（`ready` 说明为什么不能用、`search` 返回 `ResourceItem`，归一与过滤由 `resNormalize` 统一做）；前端只加一张配置卡片 `webui/src/components/transfer/sources/*.vue` 挂进 `SourcesTab.vue`，搜索与提交不用改 |
| 加一个通知通道 | `internal/api/notify_extra.go` |
| 改前端页面 | `webui/src/pages/` 下对应的页面组件；路由表在 `webui/src/router/index.ts` |
| 改总览面板 | `internal/api/dashboard.go`（数据）+ `webui/src/pages/DashboardPage.vue`（界面）。**Emby 计数别再改回不带 `IncludeItemTypes`**，见 §6.11；台账校准在 `internal/api/medialib.go` + `webui/src/components/dashboard/CalibrateModal.vue` |
| 改任务中心 | `webui/src/pages/TasksPage.vue`（页签容器，`?job=` 打开任务详情；探测状态在这里统一轮询，给角标和两个页签用）+ `webui/src/pages/tasks/`（`JobsTab.vue` 进行中：概况 / 进行中 / 最近结束 5 条；`HistoryTab.vue` 历史：按天分组、筛选、分页，打开时认一次 `?status=`；`ProbeTab.vue` Emby 探测失败清单：重试 / 忽略；`HistoryRow.vue` 两处共用的已结束任务行；`JobDetail.vue` 详情弹窗）。页签值 `jobs` 沿用旧名，别改（收藏的 `?tab=jobs` 要能打开）；状态 / 类型 / 来源文案在 `webui/src/utils/jobStatus.ts`。后端 `internal/api/taskhistory.go`。顶栏轮询的 `GET /tasks` 别拿来查历史 |
| 改整理记录页 | `webui/src/pages/tasks/RecordsTab.vue`（在任务中心的「整理记录」页签；原在自动整理页，旧地址 `/organize?tab=records` 由 `organize` 路由的 `beforeEnter` 重定向，别删）+ `webui/src/components/organize/RedoDialog.vue`（TMDB 搜索复用 `/tmdb/search`；`mode=confirm` 时用于待确认条目改指定）。筛选栏角标、页签角标与侧栏 / 手机底栏「任务中心」上的待确认角标共用 `stores/recordStats.ts`（布局层在队列任务结束时刷新）。`?job_id=` 只看某个任务涉及的记录。**那一行上有两个删除按钮**：「深度删除」删网盘真文件，垃圾桶图标只删记录，改动时别把两者的文案/样式拉近 |
| 改 Strm 管理页（`/sync`） | `webui/src/pages/SyncPage.vue` 是页签容器，四个页签在 `webui/src/pages/strm/`（配置 / 全量 / 增量 / 深度删除） |
| 改同步定时 | `internal/api/cron.go`：三条线 —— 自动整理 cron（`incr.cron`）、增量独立轮询（`incr.interval_sec`，默认 30 秒）、全量 cron（服务于失效 STRM 检测）。三者共用 `taskMu`（见 §6.12）；整理与全量的 cron 命中时**只入任务队列**（`runScheduledTick` / `runScheduledFullSync`，后台优先级、各自去重），由 worker 排队执行，不存在「错过」。**`incr.cron` 与 `incr.interval_sec` 同一个 setting key，界面却分在两个页面上**（cron 在「自动整理 → 基础配置」，间隔在「Strm 管理 → 增量同步」）：历史上两件事绑在一条 cron 上，增量拆成独立轮询后 key 没动。前端两侧都要走 `webui/src/composables/incrSetting.ts` 的 `patchIncrCfg` 只改自己那个字段，整存整取会互相覆盖 |
| 改整理落盘 / 刮削触发 | `internal/api/orgstrm.go` 的 `orgSink`（`commit` / `flushScrape` / `flushRefresh`）；`flushScrape` 只入刮削队列 |
| 改演职人员补全（Emby 人物头像 / 中文名） | `internal/api/embypeople.go`（配置、执行器、Emby 读写、按名字对号 `matchCredit`、记账）+ `personname.go`（中文名挑选、繁转简、`PersonMeta` 缓存）；前端 `webui/src/components/plugins/PersonFillModal.vue`。NFO 演员带 `type` / `thumb` / `tmdbid`、名字用缓存的中文名，在 `scrape.go` 的 `nfoActorsOf` / `nfoCrewNames` |
| 改刮削本身（NFO / 图片 / 日志 / 占位剧照 / 探测） | `internal/api/scrapecore.go`（`titleRun`：逐产物日志、片目内进度、`episodeStills`）+ `scrape.go`（配置、NFO 结构、接口）+ `localscrape.go`（执行器、`scrapeCompensate`）。先读 §6.16 |
| 改整理记录 / 重新整理 | `internal/api/orgrecord.go`；路径推导在纯函数 `planRedoLayout`、原地刷新判定在 `isInPlaceRedo`，配套测试 `orgrecord_test.go`。**没改过名的文件（记录里无 `Orig`，即未识别 / 失败的）必须走 `redoParseVideo` → `parseVideoInDir`**，和正常整理同一套（替换规则 → 子目录季号 → 条目目录季号，子目录取自 `orgRecordFile.Dir`）；只用 `parseFileName` 的话各季同名的 E01.mkv 会撞名。记录里的文件若已出现在**更新的**整理记录里（用户挪走另行整理了），重新整理要剔除（`dropClaimedFiles`），否则会把它们搬回来、还按 fid 删掉别处刚生成的 STRM；带集号的剧集里没集号的视频进特别篇、保持原名（`specialsRel`，与正常整理同口径）。**改 `redoOrganize` 前先读它的步骤注释**：算布局 → 动网盘 → 删旧本地产物 → 落盘，这个顺序是有来由的，破坏性动作必须排在计算之后 |
| 改网盘文件页（整理 / 移动所选） | 后端 `internal/api/filebrowser.go`（列目录、定位、入队参数）/ `fileorganize.go` / `filelibrary.go`；前端 `webui/src/pages/FilesPage.vue` + `webui/src/components/files/`。新增会写网盘的分支记得走 `ops.*`（抑制、缓存失效） |
| 改本地文件页（片目卡片 / 手动刮削 / 片目详情） | 后端 `internal/api/locallib.go`（列表、状态、海报 / 背景图缩略图）/ `localscrape.go`（入队与执行、产物出口）/ `localdetail.go`（详情、Emby 媒体信息与探测状态）；前端 `webui/src/pages/LocalFilesPage.vue`（筛选与批量栏固定、海报墙单独滚动；手机退回整页滚动 + 底部浮条）+ `webui/src/components/local/`（`ScrapeDialog` / `TitleDetail` 抽屉 / `DetailFiles` / `DetailEmby`），轨道文案在 `webui/src/utils/mediaInfo.ts`。上传只在用户这一次勾了才能用 `upload115FileConsented` |
| 改工作目录配置 | `internal/api/workspace.go`：媒体库 / 转存 / 待整理 / 已存在 / 冗余五个目录分存在 `full` / `share` / `org-basic` 三个 setting 里，`SaveSetting` 保存这三个 key 时经 `guardWorkspaceSetting` 校验互不包含（只查改动过的槽位、每目录至多一次祖先链请求，cid 未变零请求）；「账号与媒体库」页的一键创建走 `InitWorkspaceDirs`，在网盘根建 `/StrmStation/{转存,待整理,已存在,冗余}`，只补未配置的。测试 `workspace_test.go` |
| 改空目录清理 | `internal/api/emptydir.go` 的 `pruneEmptyDirTree` / `pruneOrMove`；守卫见 §6.8 |
| 改深度删除 | `internal/api/deepdel.go`：执行在 `runDeepDelete`、事件范围在 `deepDelEventRows`、守卫在 `checkLibRoots`、网盘空目录在 `pruneDeepDelDirs`；Emby 事件那条线在 `deepdelemby.go`。**先读 §6.10 再动**，配套测试 `deepdel_test.go` / `deepdelemby_test.go`；整体设计与 Emby 事件的真实载荷见 `docs/115-station-notes/DEEP-DELETE-PLAN.md` |
| 想知道旧版某功能怎么做的 | 查 Git 历史中的 `web/`；现役实现在 `webui/` |
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
