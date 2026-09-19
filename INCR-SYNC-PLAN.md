# INCR-SYNC-PLAN.md — 增量同步改造方案

面向实施。对比对象是 `D:\Code\` 下的 p115client / p115strmhelper / openStrm / qmediasync /
MediaSync115 / SmartStrm（清单见 [REFERENCES.md](REFERENCES.md)）。

**本文是一次性的实施计划，全部阶段落地后即可删除。**

---

## 1. 背景

增量同步（[`internal/api/incr115.go`](internal/api/incr115.go)，1041 行）目前一个文件同时干四件事：
拉生活事件、解析网盘路径、判作用域、落盘。对照其他项目逐项体检后，发现 1 个正确性 bug、
1 个功能缺失、以及一批请求量与健壮性上的差距。

改造主线：**把「拉事件」和「解析路径」抽成独立的、可注入可测试的模块**，`incr115.go` 只留编排。

> ⚠️ 现状 `incr115.go` 里**只有 `insertSyncEvents` 一个函数有测试**
> （而且还写在 `full115_test.go` 里）。事件消费主流程、路径解析、`removeSyncedItem`
> 的四级兜底全部裸奔。这是本次改造要一并补上的。

> ⚠️ **动手前先读 [§12 对既有链路的影响](#12-对既有链路的影响)**。
> 其中 ①（PathCache 陈旧 × 整理搬移目录）与 ②（`fullSyncMu` 竞争 × 30 秒轮询）
> 是本次改造**自己引入的债**，不是可选优化 —— ① 会让整理的保护子树守卫失效、
> 让增量给冗余内容生成 STRM；② 会让自动整理整轮被跳过。
> 它们已分别落到 §5.4、阶段 5「抑制检查的位置」、阶段 7「错过即补」三处。

---

## 2. 现状盘点

### 2.1 术语先对齐

「增量同步」在各项目里不是一回事，对比时必须分开看：

- **本项目 / openStrm**：增量 = 消费 115 生活事件（`behavior/detail`）
- **p115strmhelper**：增量同步 = 导出目录树做树差集；生活事件是另一个独立模块「监控生活事件」
- **qmediasync / MediaSync115 / SmartStrm**：没有生活事件，增量 = 重新遍历但只处理新增（或目录快照哈希比对）

### 2.2 各项目「增量」策略

| 项目 | 实现原理 | 感知删除 | 感知改名/移动 | 请求量级 | 实时性 |
|---|---|---|---|---|---|
| **115-Station** | 生活事件流 → 事件落库去重 → 定向重遍历受影响目录 / pick_code 零遍历直推 | ✅ | 移动 ✅ / 目录改名只重建不搬旧树 | 每轮 ~34 次拉取 + 每个新目录 depth 次 `get_info` | cron，默认 `*/10 8-23` |
| **p115strmhelper·增量同步** | `export_dir` 导出网盘目录树 → 与本地树差集 | ✅（可选，带阈值保护） | ❌（表现为删+增） | O(1) 导出 + 轮询 | 手动 / 定时 |
| **p115strmhelper·监控生活事件** | 生活事件流 → 直接对本地文件增删改名 | ✅ | ✅（本地目录直接 rename） | 每轮 1~2 次 | 常驻循环，10~15s |
| **openStrm** | 生活事件流 → 统一 ChangeEvent → handlers | ✅ | ✅（path_cache 里有旧路径） | 每轮 1~2 次 | 常驻循环，默认 15s |
| **qmediasync** | 全量递归列文件，跳过已存在 | ❌ | ❌（README 明说感知不了改名） | 同全量 | 定时 / TG 指令 |
| **MediaSync115** | 根目录快照哈希，二级目录逐棵比对，变了才下钻 | ✅ | ✅（整棵重扫） | O(变动子树) | 定时，最小 3 分钟 |
| **SmartStrm** | 闭源，README 称 crontab 增量生成 + 同步删除 | ? | ? | ? | 定时 |

### 2.3 生活事件监控实现细节（p115client 为基准）

| 维度 | **115-Station（现状）** | p115strmhelper | openStrm | p115client |
|---|---|---|---|---|
| 端点 | 固定 `proapi.115.com/android/behavior/detail` | ios ⇄ web 互为兜底 | ios ⇄ web 互为兜底 | app / web 双通道 |
| 405 风控降级 | ❌ 无 | ✅ 连续 3 次 405 → 24h 固定 webapi | ✅ 同左 | 注释建议 web 顶上 |
| **开启「115 生活」开关** | ❌ 从不调用 | ✅ `life_show` | ✅ `enableLifeCalendar` + 真拉一条验证 | `life_show` |
| 游标 | ❌ 无，靠 DB 去重「拉到没有新事件为止」 | `from_id` + `from_time`，持久化 | 同左，按账号存 | `id <= from_id` 即停 |
| 单页条数 | 固定 **30** | 首批 64（有游标）/ 1000（无） | 同左 | 同左 |
| 翻页冷却 | 走全局读节流 ≤1s | `cooldown=2s` | `cooldownMs=2000` | 建议 app 通道 ≥5s |
| 浏览/标星事件（3,4,7,8,9,10,19） | ❌ 全部落库再走 default 分支 | 拉取时跳过 | 拉取时跳过（仍占去重位） | `IGNORE_BEHAVIOR_TYPES` |
| 同一 file_id 只取最新 | ❌ | ✅ | ✅ | `yield_latest` |
| **cid → 路径解析** | 逐级 `files/get_info`，**每层一次请求** | 网盘目录 DB 镜像 + `idpathcacher` | 内存 LRU → `path_cache` 表 → **一次请求取整条祖先链** | `get_ancestors` |
| 路径缓存失效 | 目录改名时**整表清空**，TTL 10 分钟 | DB 镜像按子树更新 | `repathSubtree` / `dropSubtree` 前缀级 | — |
| 目录改名处理 | 重遍历父目录，旧名子树留给孤儿清理 | 直接 rename 本地目录 | 同左 | — |
| 自产事件抑制 | ✅ `EventSuppress` 表 | ✅ `pantransfercacher` | ✅ `findOwnOperation` | — |
| 事件持久化 + 断点续传 | ✅ `SyncEvent` 表（**这块比谁都强，保留**） | ✅ life_event 表 | ✅ life_events 表 | — |
| 监控状态/探针 UI | ❌ | ✅ | ✅（probe / 状态 / 日志） | — |
| 运行方式 | 与「自动整理」共用一条 cron，最细 1 分钟 | 常驻线程 | 常驻循环，可按账号 | — |

---

## 3. 总体设计

```
internal/api/
  life115.go   ← 新增：生活事件拉取层（端点/游标/分页/过滤/405 降级/开关门禁）
  panpath.go   ← 新增：cid → 网盘绝对路径解析器（LRU + PathCache 表 + 一次请求取祖先链）
  incr115.go   ← 瘦身：只剩「消费事件 → 落盘 → 记账」
  cron.go      ← 增量从 cron 拆出，独立轮询
  files115.go  ← 加 httpStatusError（405 才能被识别）
internal/model/model.go ← SyncEvent 加 2 列 + 新增 PathCache 表
```

**遵守项目既定约定：不写兼容层、不写数据迁移。**
新列由 `AutoMigrate` 自动加；旧行 `pick_code` 为空自然回退目录遍历；`PathCache` 空表自然冷启动；
`incr-cursor` 不存在时按现状收敛一次再写入。

### 3.1 阶段 0 ｜可注入接口地基（**最先做**）— ✅ 已完成（2026-09-19）

> 落地情况：新增 [`incrdeps.go`](internal/api/incrdeps.go)（`incrDeps` 接口 + `realIncrDeps`
> 逐行转发实现）；`executeIncrementalSync` 拆成薄封装 + `executeIncrementalSyncWith(d, p)`；
> `removeSyncedItem` 改收 `incrDeps` 替代 `cookie` + `memo`；
> 新增 [`incr_flow_test.go`](internal/api/incr_flow_test.go)（5 个主流程用例）。
>
> 与原计划的差异：**数据库和本地文件系统没有抽进接口**。仓库既有测试
> （`suppress_test` / `orgstrm_test`）本来就用真的内存 SQLite 和临时目录，
> 真实现比桩更能暴露 upsert、整树删除这类最容易出错的地方。
> 只有「打 115 / 读写配置 / 发通知」走桩。
>
> 阶段 1 的遗留项已消除：`TestIncrSkipsAlreadyAppliedEvents` 覆盖了调用方那半边，
> 并做过变异验证 —— 把 `pending = append(pending, fresh...)` 改回 `batch...`
> 该用例立刻以 `Deleted=1`（旧文件被误删）失败。

`executeIncrementalSync` 现在直接调 115 和本地磁盘，整个函数无法单测 ——
这也是这块代码至今只有一个纯函数有测试的根本原因。
后面每个阶段的测试都要踩在这块地基上，所以先做。

照搬仓库已有的模式（[`emptydir.go:31`](internal/api/emptydir.go:31) 的 `dirIO`，
`*pan115Ops` 直接满足它）：

```go
// incrDeps 增量同步对 115 与本地磁盘的全部外部依赖。
// 生产实现由 pan115Ops + 真实解析器组合而成，测试传桩，
// executeIncrementalSync 由此可以整体单测
type incrDeps interface {
	// 事件（阶段 3 的 lifeFetcher 实现）
	fetchEvents(opt lifeFetchOpts) ([]lifeEvent, error)
	// 路径（阶段 2 的 panpath 实现）
	resolveDirAbs(cid string) (string, error)
	resolveDirAbsFresh(cid string) (string, error)   // 安全关键路径用，不吃缓存
	lookupCachedAbs(cid string) (string, bool)
	// 目录遍历与落盘
	walkDir(cid, basePath string, videos, assets *[]remoteFile, f *syncFilter) error
	writeStrmFile(localPath, domain, format string, keepExt, skipExist bool, f remoteFile) error
	downloadAsset(f remoteFile, localPath string) error
}

// 主流程改成接受依赖；原名保留为薄封装，HTTP 与调度入口不用改
func (h *Handler) executeIncrementalSyncWith(d incrDeps, p incrParams) (*incrSummary, error)
func (h *Handler) executeIncrementalSync(p incrParams) (*incrSummary, error)  // 组装真实 deps 后转调
```

解锁的测试能力（后面各阶段都会用到）：

- 喂一串事件 → 断言本地文件树 + `SyncedFile` 台账 + `incrSummary` 三者一致
- 模拟 `walkDir` 失败 → 断言 `DirsSkipped > 0` 时事件保持 pending、游标不推进
- 模拟整理自产事件 → 断言 STRM 没被重复处理、但 `PathCache` 更新了（§12 ①）

**工作量** 2h ｜ **风险** 低（纯重构，行为不变，改完先跑一遍现有门禁）

---

## 4. 阶段 1 ｜事件重放 bug（P0）— ✅ 已完成（2026-09-19）

> 落地情况：`insertSyncEvents` 改为返回新插入的行；新增纯函数 `mergePendingEvents`
> 承担 stale × fresh 去重；`markEventsCoveredByFullSync` 改用同一个 helper
> （顺带从逐行 `Create` 变成批量）。新增 `incr_pending_test.go`（4 个用例），
> 并修正了 `full115_test.go` 里既有的 `TestInsertSyncEvents`（它用的是旧签名）。
>
> ⚠️ 遗留：调用方那半边的修复（`pending = append(pending, fresh...)`）**还没有直接测试**，
> 因为它长在 `executeIncrementalSync` 里，要等阶段 0 的可注入接口。
> 现在锁住的是让这个修复成立的那层契约。


### 问题

[`incr115.go:414`](internal/api/incr115.go:414)：`insertSyncEvents` 只返回**新插入条数**，
调用方却把**整页 30 条**都塞进了 `pending`：

```go
if n := insertSyncEvents(h.DB, batch); n > 0 {
    fresh += n
    for _, se := range batch { ... pending = append(pending, se) }   // 整页，不是新的那几条
}
```

只要一页里有 1 条新事件，同页其余已 applied 的历史事件会被重放。后果不只是浪费请求：
一条早已应用过的 `delete_file` 重放时台账行已经没了 → 落到
[`removeSyncedItem`](internal/api/incr115.go:883) 的第 2 级「路径推导」→
如果用户之后往同一路径重新上传了同名文件，**会被再删一次**。

另外 `stale` 查询（[`incr115.go:440`](internal/api/incr115.go:440)）与本轮 batch 还会互相重复。

### 改动

```go
// insertSyncEvents 批量插入生活事件，返回【真正新插入】的那些行。
// 之前只返回条数，调用方无法区分新旧，只能把整页塞进 pending 重放一遍
func insertSyncEvents(db *gorm.DB, batch []model.SyncEvent) []model.SyncEvent
```

调用点收敛成三行：

```go
fresh := insertSyncEvents(h.DB, batch)
sum.EventsFresh += len(fresh)
pending = append(pending, fresh...)
```

顺带：

- 删掉 [`incr115.go:426`](internal/api/incr115.go:426) 的空 `if fresh > 0 {}`
- `stale` 合并时按 `event_id` 去重
- [`markEventsCoveredByFullSync`](internal/api/full115.go:574) 改用同一个 helper
  （它现在是逐行 `Create`，千级事件千次事务，且是拉取逻辑的第二份拷贝）

### 测试 `incr_pending_test.go`

- 库里预置一条 applied 事件 → 喂含它 + 1 条新事件的 batch → 只返回新的那条
- batch 内重复 event_id 只返回一次
- stale 与 fresh 有交集时 `pending` 无重复

### 验收

一轮增量里 `len(pending)` == 本轮真正新增的事件数，不再等于页大小。

**工作量** 0.5h ｜ **风险** 无（纯收窄）

---

## 5. 阶段 2 ｜路径解析器（P0）

### 问题

[`get115RelPath`](internal/api/incr115.go:180) / [`absPathOf`](internal/api/incr115.go:813)
逐级爬 `files/get_info`，每层一次请求 × ≥1s 节流。`scopeOf` 对每条事件都要爬一次。

### 关键接口事实

`GET {webapi}/files?cid=<cid>&limit=1&hide_data=1&...` 的响应里带一个 `path` 数组，
**就是从根到该目录的完整祖先链**，一次请求全拿到（openStrm 的 `fetchAncestors` 生产在用）。

而我们的 [`fetch115FilesPage`](internal/api/files115.go:111) 已经在打这个接口，
只是解析时把 `path` 字段丢掉了 —— 加一个字段就能白捡。

### 5.1 新表

```go
// PathCache 115 目录 id → 网盘绝对路径。生活事件只带 parent_id，
// 路径要靠它还原；move/rename 的【旧】路径更是只能从这里拿
type PathCache struct {
	FileID    string    `gorm:"primaryKey;size:64"`
	ParentID  string    `gorm:"index;size:64"`
	Name      string    `gorm:"size:500"`
	Path      string    `gorm:"size:1000;index"` // 绝对路径，/ 开头
	UpdatedAt time.Time
}
```

### 5.2 `panpath.go` 对外面孔

```go
type ancestor struct{ cid, pid, name string }

// fetch115Ancestors 一次请求拿整条祖先链（根在最前，末项是 cid 自身）
func fetch115Ancestors(cookie, cid string) ([]ancestor, error)

// resolveDirAbs 三级回退：内存 LRU → PathCache 表 → 接口（并把每一级都写进缓存）
//
// ⚠️ 必须校验 path 末元素 cid == 请求的 cid（见 §5.5 探测 B）：
// 115 对不存在/已删除的 cid 不报错，而是静默按根目录处理并返回 HTTP 200 + state:true。
// 不校验就会把「已删除的父目录」解析成网盘根，路径推导指向媒体库根下的同名文件
func resolveDirAbs(cookie, cid string) (string, error)

// lookupCachedAbs 只查缓存不打接口 —— move/rename 找旧路径专用，查不到就是查不到
func lookupCachedAbs(cid string) (string, bool)

func rememberDirPaths(rows []model.PathCache)
func forgetPathsUnder(absPath string)          // 前缀失效（替代全表清空）
func repathSubtree(oldAbs, newAbs string)      // 目录改名：表内前缀替换
```

> ⚠️ 前缀匹配一律用 `prefix + "/"` 比较，避免 `/a/bc` 被 `/a/b` 误伤。这条要有专门测试。

### 5.3 老函数怎么处理

| 旧 | 新 |
|---|---|
| `absPathOf(cookie, cid, memo)` | 签名改成 `absPathOf(cookie, cid string) string`，内部转调 `resolveDirAbs`。**10 个调用点机械替换**（`dir.go` / `offline.go`×4 / `organize.go`×3 / `upload115.go`×2），它们本来就在传一次性的空 map |
| `get115RelPath(cookie, cid, rootCid, memo)` | 改成 `resolveDirAbs(cid)` + `libAbs` 前缀相减，不再爬链 |
| `get115DirInfo` | 保留签名，内部改用祖先链末项实现（6 个调用点顺带把缓存捂热）。⚠️ 仅对**目录** cid 有效 —— 现有调用点传的都是目录，加注释说明 |
| `dirInfo` / `memo map[string]dirInfo` | 按「删干净」约定整套删除 |
| `dirAbsCache` / `invalidateDirAbsCache` | 删除，由 `panpath.go` 两级缓存取代 |

> 注意：`absPathOf` 被整理、离线、上传、目录浏览共用，这次提速是**全仓库**受益，不止增量。

### 5.4 缓存失效钩子 —— **必做项**（背景见 §12 ①）

改成持久化 PathCache 之后，陈旧就是**永久的**。而整理会整目录搬移
（[`organize.go:1517`](internal/api/organize.go:1517) / [`1623`](internal/api/organize.go:1623) /
[`1644`](internal/api/organize.go:1644) 把目录搬进冗余/已存在，
[`emptydir.go:181`](internal/api/emptydir.go:181) 搬空目录），
目录一搬，它和整棵子树的绝对路径就变了。

现在没事，因为 `dirAbsCache` 只有 10 分钟 TTL，且 `get115RelPath` 压根不查缓存。
改造后**必须**补上三件事，否则等于亲手制造一个安全漏洞：

**（a）`ops` 层写操作后立即失效缓存。** 位置现成 —— `markSuppressed` 就在同一处：

```go
// open115.go：moveFiles / rename / renameBatch / deleteFiles
// 网盘侧路径已经变了，缓存必须跟上；不跟上就是下面两个安全判定拿着旧路径做决定
forgetPathsUnder(abs)   // 或按 fid 精确失效
```

**（b）安全关键路径不吃缓存**，改用强制刷新的 `resolveDirAbsFresh`：

| 调用点 | 拿错路径的后果 |
|---|---|
| `scopeOf`（[`incr115.go:464`](internal/api/incr115.go:464)） | 已搬进「冗余」的目录仍被判成 `library` → **增量给冗余内容生成 STRM** |
| `orgGuards.absOf`（[`organize.go:1187`](internal/api/organize.go:1187)） | 保护子树路径算错 → 守卫失效 → **把库内内容当待整理素材重排** |
| 增量熔断体检（`libAbs` / `excludedAbs`） | 工作区覆盖判定失准 → 熔断该响的时候不响 |

**（c）被抑制的事件仍要维护缓存** —— 见阶段 5 的「抑制检查的位置」。

### 5.5 前置真机验证 —— ✅ 已完成（2026-09-19）

结论已写进 [REFERENCES.md](REFERENCES.md) 第三节。对本阶段的三条硬约束：

| 探测 | 结果 | 对实现的约束 |
|---|---|---|
| A | ✅ `path` 共 5 级，字段 `cid`/`pid`/`name`，末元素就是请求的 cid；`hide_data=1` 不影响 `path`（1344 vs 1348 字节） | 按计划实现，取祖先链时带上 `hide_data=1` |
| A | 首元素是 `{"cid":"0","pid":"0","name":"根目录"}` | **拼路径时必须跳过根元素**，否则得到 `/根目录/影视/…` |
| B | ⚠️ **不存在的 cid 被静默当作根目录**：`HTTP 200` + `state:true` + `errNo:0`，响应体 `cid` 变 `0`、`path` 只剩根一级、`data` 返回根目录内容 | 见下方「B 带来的硬门槛」 |

#### B 带来的硬门槛（**必做**）

现在的代码用 `files/get_info`，对已删除目录会明确返回 `800001 目录不存在`，
[`incr115.go:663`](internal/api/incr115.go:663) 正是靠这个错误把事件按「已解决」跳过。
**换成 `files?cid=` 会丢掉这个能力** —— 这是本次改造里唯一一处「新接口不如旧接口」的地方。

所以 `resolveDirAbs` 必须自己补上判定：

```go
// 115 不报错，只能自己认：末元素 cid 对不上 = 目录已不存在
if len(chain) == 0 || chain[len(chain)-1].cid != cid {
    return "", errDirGone     // 调用方按「已解决」跳过，与现在处理 800001 的语义一致
}
if chain[len(chain)-1].aid != "1" {
    return "", errDirGone     // 回收站里的目录（aid 未实测，稳妥起见一并挡）
}
```

漏了这一步的后果是**删错文件**：一个已删除的父目录会解析成「网盘根」，
`removeSyncedItem` 的路径推导（[`incr115.go:896`](internal/api/incr115.go:896)）
就会去媒体库根下找同名文件删掉。阶段 6 收紧兜底范围**解决不了**这个——
因为此时推导出的范围本身就是错的。

`panpath_test.go` 必须有这条用例：桩返回「只有根一级的 path」→ 断言返回 `errDirGone` 而不是 `/`。

### 测试 `panpath_test.go`（桩掉 `fetch115Ancestors`，计数请求次数）

- 一条 4 层链 → 1 次请求，4 行缓存
- 同父目录的第二个 cid → 0 次请求
- `repathSubtree` / `forgetPathsUnder` 的前缀边界（`/a/bc` vs `/a/b`）
- 目录不存在 → 返回错误而不是空路径（空路径会让 `scopeOf` 判成 `unknown` 静默吞事件）

### 验收

一轮增量处理 20 条分布在 5 个目录的事件，路径相关请求从 ~40 次降到 ≤5 次。

**工作量** 4h（含 §5.4 的缓存钩子）｜ **风险** 中（改 10 个调用点，但都是同构替换；`absPathOf` 语义不变）

---

## 6. 阶段 3 ｜拉取层重写（P0 + P1）

### 问题

固定 30/页、固定 `proapi/android`、无游标、无降级、无门禁、冷却 1s
（与 [`incr115.go:27`](internal/api/incr115.go:27) 自称的「≥5 秒」自相矛盾）。

### 6.1 `life115.go` 骨架

```go
const (
	lifeAPIProapi = "https://proapi.115.com/android/behavior/detail"
	lifeAPIWeb    = "https://webapi.115.com/behavior/detail"
)

// lifeCursor 增量游标。id 单调递增，命中 id <= FromID 即整轮停止
type lifeCursor struct {
	FromID   string `json:"from_id"`
	FromTime int64  `json:"from_time"`
}

type lifeFetchOpts struct {
	Cursor      lifeCursor
	FirstBatch  int           // 有游标 64 / 无游标 1000（p115client 同款）
	MaxPages    int           // 默认 50，防无限翻
	Cooldown    time.Duration // 翻页冷却，默认 2s
	YieldLatest bool          // 同一 file_id 只留最新那条
}

// lifeFetcher 事件拉取器。load/save 注入便于单测，
// 生产由 h.newLifeFetcher 用 setting 读写实现
type lifeFetcher struct {
	cookie string
	load   func(key string) string
	save   func(key, val string)
}

func (h *Handler) newLifeFetcher(cookie string) *lifeFetcher

// fetch 单轮拉取：从最新往回翻到游标为止，返回倒序（新→旧）
func (f *lifeFetcher) fetch(opt lifeFetchOpts) ([]lifeEvent, error)

// enableLife 打开「115 生活」事件记录开关
// POST https://life.115.com/api/1.0/web/1.0/calendar/setoption  form: locus=1&open_life=1
//
// ⚠️ 响应结构是 {state, code, message, data}，不是 webapi 惯用的
// {state, errNo, error} —— 别套通用响应结构体（2026-09-19 实测，见 §14 探测 D）
func (f *lifeFetcher) enableLife() error

// gate 门禁自检：setoption + 真拉一条。
// ⚠️ setoption 对失效 cookie 也返回成功，光看它会漏掉「请重新登录」（openStrm 踩过这个坑）
func (f *lifeFetcher) gate() (ok bool, msg string)
```

### 6.1.1 双通道实测结论（2026-09-19，探测 C）

已写进 [REFERENCES.md](REFERENCES.md) 第三节。对本阶段的约束：

- ✅ **webapi 字段是 proapi 的超集**（多 `iv` / `parent_name`，无缺失），
  405 降级**可以共用一套解析** —— 探测脚本报的「字段不一致」是等值判定过严，不是问题
- ⚠️ **`data.count` 两通道类型不同**：proapi 是字符串 `"4"`，webapi 是数字 `4`。
  声明成 `int` 会让 proapi 的**整个响应**解析失败（[`incr115.go:112`](internal/api/incr115.go:112)
  的注释早就记过这个坑，现有代码是靠「干脆不解析 count」绕开的）。
  但阶段 3 的分页要靠 `offset >= count` 收敛，**必须解析**：

  ```go
  Count json.RawMessage `json:"count"`   // "4"（proapi）或 4（webapi），两种都要认
  ```

- `id` / `file_id` / `parent_id` 都是字符串返回，**Go 侧没有大整数精度问题**
  （openStrm 的 `parseJsonBigIntSafe` 是 JS 才需要，我们不用抄）
- `type` / `file_category` 是数字返回，现有 `fmt.Sprint` 归一化照常可用
- 🎁 **`parent_name` 是白送的缓存校验**（仅 webapi 通道）：事件自带父目录名，
  与 `PathCache` 里该 `parent_id` 的 `Name` 一比就知道缓存是否陈旧，
  不一致就强制 `resolveDirAbsFresh`。这是 §12 ① 的零成本第三道保险，
  建议在 webapi 通道下启用

### 6.2 405 降级

先给 `httpGet115Full` 一个可识别的错误类型（现在是 `fmt.Errorf("115 接口返回 HTTP %d")`，只能靠字符串猜）：

```go
// httpStatusError 115 返回的非 200，调用方按状态码决定降级/重试
type httpStatusError struct{ Code int }
func (e *httpStatusError) Error() string { return fmt.Sprintf("115 接口返回 HTTP %d", e.Code) }
```

策略照搬 openStrm / p115strmhelper：

- 默认 `proapi`；`errors.As` 到 405 → 当轮改打 `webapi` 重试
- 连续 3 次「proapi 405 而 webapi 正常」→ 24h 内固定 `webapi`
- 走 `webapi` 期间若它自己 405 → 立刻清除粘滞、回 `proapi`
- 状态存 setting `life-endpoint`：`{"ios_405":n,"web_until":unix}`

### 6.3 游标

- setting `incr-cursor` = `{"from_id":"...","from_time":...}`
- **推进时机与事件标 applied 严格绑定**：`DirsSkipped > 0` 整轮放弃时游标也不动
- [`markEventsCoveredByFullSync`](internal/api/full115.go:574) 跑完后把游标推到当时最新事件 id
- 游标缺失（首次/升级后）→ `FirstBatch=1000`，按 `p.Limit` 收敛，**行为与现状一致**；跑完写入游标
- DB 去重**保留**作为第二道保险：游标即使出错也不会导致重复落盘

### 6.4 冷却

`behavior/detail` 从全局读通道（≤1s）挪到独立的 2s 冷却通道，
参照 [`throttleFast`](internal/api/ratelimit.go:120) 的做法加 `throttleLife`。
同时把 [`incr115.go:27`](internal/api/incr115.go:27) 那句「≥5 秒」的注释改成与代码一致的说明。

### 6.5 门禁调用时机

不能每轮都打（那是白送的请求）：

- 进程启动后第一轮跑 `gate()`
- 之后连续 20 轮拉到 0 条事件（≈10 分钟）→ 复检一次
- 结果写进内存状态供阶段 8 的状态页展示

### 测试 `life_fetch_test.go`（注入分页桩，计数请求次数）

- 游标命中 `id <= FromID` 立即停，不再翻页
- 无游标首批 1000、有游标首批 64、后续页 1000
- ignore 类事件被丢弃但仍占 `file_id` 去重位（与 p115client 行为一致）
- `YieldLatest`：同一 file_id 的三条事件只出最新
- 405 → 降级 webapi；连续 3 次 → 粘滞 24h；webapi 也 405 → 回退并清粘滞
- `gate()`：setoption 成功但拉取返回登录失效 → 判失败

### 验收

日常一轮（有游标、无新事件）**1 次请求**；有 20 条新事件也是 1 次。从 34 次降到 1~2 次。

**工作量** 4h ｜ **风险** 中（webapi 版 `behavior/detail` 字段一致性需真机验证一次）

---

## 7. 阶段 4 ｜事件过滤与落库字段（P2）

```go
// lifeIgnoredType 浏览/标星类事件（3,4,7,8,9,10,19）：入库前就丢掉。
// 活跃账号里它们占绝对多数，落库只会拖慢去重查询、挤占 Limit 配额
func lifeIgnoredType(raw string) bool
```

数字与名称两种形态都要覆盖（`star_image` / `browse_video` / `folder_label` …）。
**过滤发生在入库前、去重位之后。**

`SyncEvent` 加两列：

```go
PickCode     string `json:"pick_code" gorm:"size:64"`      // 事件自带，有则零遍历直推
FileCategory string `json:"file_category" gorm:"size:4"`   // "0"=目录 "1"=文件
```

随即删掉 `pickByEvent` 这个只活一轮的内存 map（[`incr115.go:409`](internal/api/incr115.go:409)）——
上一轮中断残留的 stale 事件重新消费时 pick_code 已丢，零遍历优化白白失效。
`file_category` 让删除/移动不必再靠 `os.Stat` 猜是不是目录。

### 测试

过滤表覆盖 19 个类型码 + 名称形态；stale 事件带 pick_code 复活后仍走零遍历。

**工作量** 1h ｜ **风险** 低

---

## 8. 阶段 5 ｜目录改名与移动（P2）＋ 一个功能修复

### 顺手修掉的现存 bug

> 目录在 115 里被**移动**时，`evMove` 走 `removeSyncedItem(ledgerOnly=true)`，
> 而台账 `SyncedFile` 存的是**文件**行、按 file_id 索引，目录的 fid 根本不在里面
> → 返回 false → 只在新位置重建，**旧目录树永远留在本地**。
> 改名（[`incr115.go:571`](internal/api/incr115.go:571)）同理，代码注释里写着「交由后续清理功能」。

### 改动

```go
// relocateLocalDir 目录改名/移动：本地目录直接 rename + 台账前缀替换，零 115 请求。
// 旧路径只能来自 PathCache（事件里的 parent_id/file_name 都是【新】位置）
func (h *Handler) relocateLocalDir(oldRel, newRel, localRoot string) bool
```

流程：

1. `oldAbs, ok := lookupCachedAbs(ev.FileID)` —— 拿不到就回退现状（重遍历 + 孤儿检测），**不猜**
2. `newAbs := resolveDirAbs(ev.Cid) + "/" + ev.FileName`
3. 两者都在库内 → `os.Rename`；台账一条 SQL 换前缀：
   ```sql
   UPDATE synced_files SET rel_path = ? || substr(rel_path, ?) WHERE rel_path LIKE ?
   ```
4. `repathSubtree(oldAbs, newAbs)` + `forgetPathsUnder(oldAbs)`（替代现在的全表清空）
5. 目标已存在 / rename 失败 → 回退 `dirSet` 重遍历
6. 记 `noteShallow`，Emby 定向刷新能覆盖到

`evMove` 里按 `FileCategory == "0"` 分流到同一条路径。

### 抑制检查的位置 —— **必做项**（背景见 §12 ①）

现在整理自产事件在 [`incr115.go:500`](internal/api/incr115.go:500) 被 `peekSuppressed` 直接
`continue` 掉，**连缓存维护的机会都没有**。整理搬移目录产生的正是这类事件，
于是 PathCache 永远停在旧路径上。

改造后必须把抑制检查**挪到缓存维护之后**：

```
每条事件：
  1. 更新 PathCache（网盘侧的事实已经变了，与本地怎么处理无关）
  2. peekSuppressed 命中 → 跳过【本地文件动作】，continue
  3. 未命中 → 正常走落盘/删除/改名
```

一句话：**抑制跳过的是本地动作，不是缓存更新。** openStrm 的 `toChange` 在注释里
专门强调过这点（「缓存先更新：无论本地怎么处理，网盘侧的事实已经变了」）。

### 测试 `incr_rename_test.go`（真实临时目录 + 内存 DB）

- 目录改名：本地目录真被改名、子文件台账 `rel_path` 跟着走
- 嵌套两层的子目录台账也被正确换前缀
- 目标已存在时不覆盖、走回退
- 缓存里没有旧路径时不乱删任何东西

**工作量** 2h ｜ **风险** 中（动本地文件系统，测试必须扎实）

---

## 9. 阶段 6 ｜兜底收紧（P2）

[`incr115.go:969`](internal/api/incr115.go:969) 的第 4 级兜底 `filepath.WalkDir(localRoot, ...)`
扫全库、按裸文件名匹配后 `RemoveAll`。万级库是秒级 IO，且重复片名在媒体库里极常见。

**改动**：搜索范围从 `localRoot` 收窄到「本次事件推导出的库内相对目录」子树；
推不出范围就直接放弃并记一条日志（宁可漏删留给孤儿检测，也不能误删）。
阶段 2 做完之后，推导范围基本总是能拿到。

#### ⚠️ 前置修复：第 2 级路径推导漏了库名层（阶段 0 期间实测发现）

台账 `RelPath` 一律**带库名前缀**（`applySyncResults` 写的是
`path.Join(f.Path, …)`，而 `f.Path = path.Join(libName, base)`），
但 `removeSyncedItem` 第 2 级推导的是：

```go
base, _, _ := d.relPath(ev.Cid, rootCid)   // "剧集/X"，不含库名
rel := path.Join(base, ev.FileName)        // → <root>/剧集/X/片.mkv
```

实际文件在 `<root>/媒体库/剧集/X/片.mkv.strm`。**第 2 级因此永远不命中**，
连带它负责的「整目录删除」分支也是死的。

实测（临时探针，已删除）：台账为空时删一个库内文件，第 2 级推导的目标路径不存在，
文件最终是被**第 4 级全盘 WalkDir 按名搜索**删掉的。

> 所以顺序有硬约束：**必须先补上库名前缀，再收紧第 4 级**。
> 反过来做，台账启用前同步的历史文件与整目录删除会直接失去清理能力。

**测试**：库外 / 别的子树里的同名文件不被删。

**工作量** 1.5h（含库名前缀修复）｜ **风险** 低（纯收窄）

---

## 10. 阶段 7 ｜独立轮询（P2）

### 现状

增量和自动整理绑在同一条 cron（[`cron.go:236`](internal/api/cron.go:236)），
粒度最细 1 分钟、默认 `*/10 8-23`。

### 改动

- 新设置 `incr.interval_sec`，默认 **30**，最小 15，`0` = 关闭独立轮询（退回 cron 串联，保留逃生门）
- `StartSyncScheduler` 再起一个 goroutine：`ticker(interval)` → `runIncrementalTick()`
- 增量侧与 `fullSyncMu` 的关系：`TryLock` 失败就静默跳过本轮（整理/全量在跑时让路）
- 独立轮询开启时，`runScheduledTick` 只跑整理，不再串增量（否则重复）
- ⚠️ **整理侧的 `TryLock` 必须改成「错过即补」—— 必做项**（背景见 §12 ②）：
  [`cron.go:236`](internal/api/cron.go:236) 现在拿不到锁就整轮放弃。增量提频到 30 秒后，
  整理的 cron 命中时撞上一轮正在遍历大目录的增量，**整理就整轮被跳过、要等下一个 cron 周期**
  （默认配置下 10 分钟）。改法：置一个 pending 标记，增量那轮结束后立刻补跑整理；
  或给整理一个最长 N 分钟的等锁窗口。**不能沿用 TryLock 直接 return**
- ⚠️ **空转轮次不要 `beginTask`**：30 秒一次的 `beginTask("增量同步")` 会让前端「当前任务」闪烁，
  也会往运行历史里灌垃圾。只有真的有待处理事件时才 `beginTask`

### 风控面反而下降（可写进 UI 提示）

| | 拉取频率 | 每分钟请求数 |
|---|---|---|
| 现状 | 10 分钟 × 最多 34 次 | ~3.4 |
| 改造后 | 30 秒 × 1 次 | ~2.0 |

**测试**：`cron_test.go` 补 interval 合法性收敛（<15 取 15、0 关闭）；轮询与 cron 不重复触发增量；
整理被增量挡住时「错过即补」确实补上了。

**工作量** 2h（含「错过即补」）｜ **风险** 中（并发与任务状态，需要盯一轮实跑日志）

---

## 11. 阶段 8 ｜状态页（P2）

### 后端

```
GET  /sync/incr-status
  { life_gate: {ok, message, checked_at},
    endpoint: "proapi" | "webapi", web_until: 0,
    cursor: {from_id, from_time},
    last_round: {at, events_fresh, strm_created, deleted, moved, elapsed},
    pending_events: n, path_cache: n, interval_sec: 30 }

POST /sync/incr-probe        // 只拉不处理，返回最近 10 条事件原样
```

### 前端

[`webui/src/pages/strm/IncrSyncTab.vue`](webui/src/pages/strm/IncrSyncTab.vue)
加一张状态卡片 + 「测试事件流」按钮 + `interval_sec` 输入框。

做完阶段 3 的门禁之后，这个面板能直接回答「为什么没同步」——
现在这个问题只能翻日志，而空转轮次是刻意静默的。

**工作量** 2h ｜ **风险** 低

---

## 12. 对既有链路的影响

改造只换「拉取方式」「路径来源」「调度节奏」三层，但其中两处会反过来咬到整理链路，
**①② 是本次改造自己引入的债，不是优化，必须跟着做**。

### 12.1 完全不变的部分

| 保留 | 说明 |
|---|---|
| `SyncEvent` 两阶段（先落库去重，再应用） | 这是本项目比 p115strmhelper / openStrm 都强的地方 |
| pending / applied 状态机 | 断点续传语义不变 |
| `DirsSkipped > 0 → 整轮不消费` | 「正确性优先」的兜底不变，游标也跟着不推进 |
| `EventSuppress` 抑制表 + peek 不消费 | 整理自产事件的跳过逻辑不变（但检查**位置**要挪，见阶段 5） |
| 排除区（待整理/已存在/冗余/转存）作用域判定 | 判定**逻辑**不变，但路径**来源**换了 |
| 配置熔断体检（媒体库 cid 无效 / 工作区覆盖整库） | 不变 |
| 零遍历直推 + 回退目录遍历 | 不变，且 pick_code 落库后覆盖率更高 |
| 整理的识别 / 分类 / 洗版 / 重命名 | 零影响，完全没碰 |

### 12.2 增量链路

| 方面 | 现在 | 改造后 |
|---|---|---|
| 一轮请求数 | ~34 次拉取 + 每个新目录 depth 次 | 1~2 次拉取 + 每个新目录 1 次（命中缓存 0 次） |
| 触发间隔 | 10 分钟（跟着整理的 cron） | 30 秒 |
| 网盘删除的响应 | 最长 10 分钟 | 最长 30 秒 |
| 事件表体量 | 含 browse/star，活跃账号里是大头 | 只存 11 类有意义的事件 |
| 首次升级后第一轮 | — | 游标为空 + PathCache 为空 → 行为与现状基本一致，第二轮起才降到 1 次请求 |
| DB 体积 | — | 新增 `PathCache`（量级 = 网盘目录数，万级约 1MB），`SyncEvent` 反而缩水 |

> 升级后库里可能还躺着一批 browse 类 pending 行，第一轮的 stale 查询会把它们捞出来、
> 走 `default` 分支、标 applied。无害，**不需要清理脚本**（符合「不写数据迁移」约定）。

### 12.3 ① PathCache 陈旧 × 整理搬移目录 —— **必做项**

整理会整目录搬移：[`organize.go:1517`](internal/api/organize.go:1517) /
[`1623`](internal/api/organize.go:1623) / [`1644`](internal/api/organize.go:1644)
把目录搬进冗余/已存在，[`emptydir.go:181`](internal/api/emptydir.go:181) 搬空目录。
目录一搬，它和整棵子树的绝对路径就变了。

现在没事，因为 `dirAbsCache` 只有 10 分钟 TTL、`get115RelPath` 压根不查缓存。
**换成持久化 PathCache 之后，陈旧就是永久的**，而这些路径喂给的是两个安全关键判定：

1. `scopeOf`：已搬进「冗余」的目录仍被判成 `library` → **增量给冗余内容生成 STRM**
   —— 这正是排除区机制存在的理由
2. `orgGuards.absOf`：保护子树路径算错 → 守卫失效 → **把库内内容当待整理素材重排**

更隐蔽的是：这些搬移都走 `markSuppressed`，事件绕回增量时在
[`incr115.go:500`](internal/api/incr115.go:500) 被 `continue` 掉，**连缓存维护的机会都没有**。

→ 对策见 **§5.4 缓存失效钩子**（ops 层失效 + 安全路径不吃缓存）
与 **阶段 5「抑制检查的位置」**（抑制跳过的是本地动作，不是缓存更新）。

### 12.4 ② `fullSyncMu` 竞争 × 30 秒轮询 —— **必做项**

[`cron.go:236`](internal/api/cron.go:236) 的 `runScheduledTick` 拿不到锁就整轮放弃：

```go
if !fullSyncMu.TryLock() {
    log.Printf("[定时] ○ 已有任务运行中，本轮跳过")
    return
}
```

现在不会冲突（增量只在整理跑完后串行执行）。改成 30 秒独立轮询后，
只要整理的 cron 命中时增量恰好在跑（例如正在遍历刚转存进来的大目录，要几分钟），
**整理就整轮被跳过，要等下一个 cron 周期** —— 默认配置下就是 10 分钟后。

→ 对策见 **阶段 7** 的「错过即补」。

### 12.5 ③ handover：整理落盘失败交回增量（正向影响）

[`orgstrm.go:165`](internal/api/orgstrm.go:165)：整理写 STRM 失败的文件会 `unmarkSuppressed`，
交回增量兜底。这条链路的恢复速度直接由增量的触发频率决定 ——
**从最长 10 分钟缩短到 30 秒**。阶段 7 对整理是净收益。

### 12.6 其他链路的连带影响

| 链路 | 影响 |
|---|---|
| **全量同步** | `markEventsCoveredByFullSync` 改为「推进游标」而不是灌 1000 条 applied 行，快且干净；`absPathOf` 提速顺带受益 |
| **失效 STRM 检测** | 阶段 5 修好目录改名/移动后**孤儿显著变少** —— 现在目录改名留下的整棵旧树全靠它兜底。检测逻辑不变，只是待清理列表变短 |
| **上传监控回传** | [`upload115.go:560/704`](internal/api/upload115.go:560) 用 `absPathOf` 判库内，提速；同样受 ① 影响，靠 ops 层钩子解决 |
| **离线下载** | [`offline.go:724-749`](internal/api/offline.go:724) 四处 `absPathOf`，纯提速 |
| **目录浏览** | [`dir.go:157`](internal/api/dir.go:157) 一处，纯提速 |

---

## 13. 执行顺序与总量

```
阶段1 ✅ → 阶段0 ✅ → 阶段2 (4h，含缓存钩子) → 阶段3 (4h)
                  → 阶段4 (1h) → 阶段5 (2h) → 阶段6 (1.5h) → 阶段7 (2h) → 阶段8 (2h)
```

约 **18 小时**，已完成 2.5h。

阶段 1 是 0.5h 的纯收窄 bug 修复、不依赖地基，**插队先做**；
随后阶段 0 铺好可注入接口（§3.1），阶段 2/3 是主干，做完收益就拿到九成；4~8 可分批跟进。

> ⚠️ **顺序硬约束**：阶段 7（独立轮询）必须排在阶段 2 的缓存失效钩子之后。
> 先提频再修缓存，等于让 §12 ① 的陈旧窗口以 20 倍频率被命中。

每阶段结束都能独立跑门禁并单独提交：

```bash
go build ./... && go vet ./... && go test ./... -count=1
```

---

## 14. 前置真机验证清单

需要真实 cookie 各跑一次，结论回写 [REFERENCES.md](REFERENCES.md)。

**脚本已就绪**（一次跑完四条，UA 与 `ua115Unified()` 完全一致，请求间隔 800ms）：

```
<scratchpad>\probe-115.ps1
```

```powershell
.\probe-115.ps1 -CookieFile .\cookie.txt -Cid <一个至少三层深的目录 cid>
```

- Cookie：浏览器 F12 → Network → 任意 115 请求 → 复制整条 Cookie 头，存成文本文件
- `-DeletedCid <cid>` 可选，传一个真实的已删除目录，比脚本默认的不存在 id 更有代表性
- `-ProbeSetOption` 才会执行探测 D —— 它是**写操作**（打开账号的「生活」事件记录开关），
  默认跳过，不由脚本替你做
- 每条探测的原始响应落成 `.json`，**里面有真实目录名，别提交进仓库**
- ⚠️ 脚本控制台打印的 19 位 id 会被 PowerShell 的 `ConvertFrom-Json` 吃掉精度，
  精确值一律看落盘的 `.json` 原文（openStrm 为此手写了 `parseJsonBigIntSafe`）

| # | 验证项 | 阻塞阶段 | 状态 |
|---|---|---|---|
| A | `files?cid&limit=1&hide_data=1` 返回 `path` 数组，字段名 `cid`/`pid`/`name` | 阶段 2 | ✅ 2026-09-19 通过，见 §5.5 |
| B | 目录已删除 / cid 不存在时的返回形态 | 阶段 2 | ✅ 2026-09-19，**结果推翻原假设**，见 §5.5「B 带来的硬门槛」 |
| C | `webapi.115.com/behavior/detail` 可用性与字段一致性 | 阶段 3 | ✅ 2026-09-19 通过，见 §6.1.1 |
| D | `life.115.com/api/1.0/web/1.0/calendar/setoption` 的返回体 | 阶段 3 | ✅ 2026-09-19 通过：`{"state":true,"code":0,"message":"","data":true}`，响应结构与 webapi 不同，见 §6.1 |

**四条全部完成，没有阻塞项了。**

> B 是这轮唯一的意外：原以为已删除目录会像 `files/get_info` 那样报 `800001`，
> 实测是**静默按根目录处理**。阶段 2 因此多一条强制校验，不做就会删错文件。
>
> D 顺带确认了开关本身可用 —— 该账号的「115 生活」事件记录**现已开启**
> （探测 D 是写操作，跑过就生效了）。

---

## 15. 已确认的决策

2026-09-19 定案，三条都已落进上面的正文：

| # | 决策 | 落点 |
|---|---|---|
| 1 | **把 `executeIncrementalSync` 的 115 调用抽成可注入接口**，让主流程整体可单测 | §3.1 阶段 0（+2h，总量 16h → 18h） |
| 2 | **独立轮询默认开启，间隔 30 秒**（`incr.interval_sec` 默认 30，最小 15，`0` 关闭作逃生门） | 阶段 7 |
| 3 | **真机验证用脚本跑**，不手动敲 | §14，脚本已就绪 |

决策 2 的理由留档：请求频率从 ~3.4 次/分钟降到 ~2.0 次/分钟，
**默认开启反而降低风控面**；代价是改变了用户既有的「一条 cron 管全部」心智，
所以保留 `0` 关闭这条逃生门，并在 UI 上把这笔账写清楚（阶段 7「风控面反而下降」那张表）。
