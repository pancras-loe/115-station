# REFERENCES.md — 外部参考项目

做 115 相关功能时的「先去哪看」清单。这些项目都在 115 的非公开接口上踩过坑，
比自己从浏览器抓包摸索快得多。

**读它们，不要抄它们。** 本仓库的许可证约束见 [AGENTS.md](AGENTS.md#️-许可证约束改动前必读)：
参考接口形态、参数含义、错误处理思路是安全的；整段复制代码会把对方的许可证要求带进来。
用到某个项目的具体做法时，在代码注释里写明出处（`pickcode115.go` 就是这么做的）。

本机的本地副本都在 `D:\Code\` 下，与本仓库同级。

---

## 一、115 接口实现（接口问题先查这两个）

### `ChenyangGao/p115client`（Python，MIT）—— 目前最完整的 115 逆向

本机：`D:\Code\p115client-main`

- `p115client/client.py` —— **接口大全**，每个方法的 docstring 里写着完整 URL、
  payload 字段和字段含义。找某个端点怎么调，`grep` 这个文件最快
- `p115client/tool/iterdir.py` —— 遍历目录树的各种策略（`iter_files` / `iter_dirs` /
  `iter_files_with_path` / `traverse_tree`），参数注释解释了每种策略的取舍
- `p115client/tool/export_dir.py` —— 「导出目录树」链路
- `p115client/tool/life.py` —— 生活事件
- `p115client/tool/fs_files.py`、`download.py`、`upload.py` —— 分页、直链、秒传/分片上传

配套的 `p115pickcode` 包实现了 pickcode ⇄ id 的本地换算（替换表 + 36 进制），
本仓库的 [`internal/api/pickcode115.go`](internal/api/pickcode115.go) 是它的 Go 等价实现。

### `SheltonZhu/115driver`（Go，MIT + 作者附加声明）—— 同语言可直接对照

本机：`D:\Code\115driver-main`

- `pkg/driver/` —— 按功能分文件：`file.go` / `dir.go` / `download.go` / `upload.go` /
  `offline.go` / `login.go` / `qrcode.go`
- `pkg/crypto/ec115`、`m115` —— 115 的两套加密（本仓库 `internal/api/115crypto.go` 同源）
- `pkg/driver/consts.go` —— 端点常量表

Go 写的，错误处理和重试模式可以直接对照我们的 `pan115Ops`。

---

## 二、STRM 同步类项目（做同步/整理功能时参考）

| 项目 | 语言 | 本机路径 | 看什么 |
|---|---|---|---|
| `qicfan/qmediasync` | Go | `D:\Code\qmediasync-main` | 同语言。扁平取文件 + 并行分页 + 路径补全的完整实现 |
| `DDSRem/MoviePilot-Plugins` 的 `p115strmhelper` | Python | `D:\Code\MoviePilot-Plugins-main\plugins.v2\p115strmhelper` | 全量/增量两套策略、网盘目录的持久化 DB 镜像、幽灵记录清理 |
| openStrm | TypeScript | `D:\Code\openStrm-main` | 导出目录树路线、路径式 STRM、RxJS 并发限流 |
| MediaSync115 | Python | `D:\Code\MediaSync115-master` | 并发递归遍历、目录快照哈希做变更检测 |
| `Cp0204/SmartStrm` | — | `D:\Code\SmartStrm-main` | **仅有 README，无源码**（闭源发行），只能看功能设计 |

### 各自的全量同步策略（2026-09 对比结论）

| 项目 | 取清单方式 | 请求量级 |
|---|---|---|
| **115-Station 标准模式** | 逐目录 DFS，串行节流 | O(目录数)，万级库 2000+ |
| **115-Station 快速模式** | `show_dir=0` 递归列文件 + `downfolders` 取目录表 | **O(文件数/1150) + 1**，实测 9716 视频 10 次请求 |
| qmediasync | 递归列文件 + 逐个父目录补路径（并发） | O(含媒体的目录数) |
| p115strmhelper | 同上，经 `p115client.iter_files_with_path` 封装 | 同上 |
| openStrm | 导出目录树（提交任务 → 轮询 → 下载解析 txt） | O(1)，但**只给路径不给 pickcode** |
| MediaSync115 | 并发递归遍历（asyncio，4 并发，200/页） | O(目录数) |

openStrm 因为导出树不带 pickcode，被迫把 STRM 写成路径式、播放时再查 pickcode。
我们的 `/d/{pickcode}` 形态不用付这个代价 —— 这是选型时的关键差异。

---

## 三、已经挖出来的 115 接口事实

这些是本仓库实测验证过的，直接可用，不必重新探测。
实现见 [`internal/api/fast115.go`](internal/api/fast115.go) 与 [`internal/api/pickcode115.go`](internal/api/pickcode115.go)。

### 递归列文件：`GET {webapi}/files?cid=..&show_dir=0`

`show_dir=0` 是递归开关 —— 返回整棵子树的**文件**（不含目录）。
`show_dir=1`（或不传）则是普通目录视图，只有直接子项。

- `cur` 参数与递归无关，加不加都一样
- 带 `type=` 或 `suffix=` 筛选时同样递归（这是 115 网页「筛选」功能的行为）
- 每页上限 **1150**；深翻页正常，实测 offset 9696 无异常，**没有** 1000 条天花板
- 条目带 `fid` / `n` / `s` / `pc` / `sha`，以及 `cid` = **父目录 id**（不是路径）
- `o` / `asc` 排序参数**会被目录自己保存的排序偏好覆盖**（响应 `path[].fv` 字段），
  所以翻页必须按 `fid` 去重，不能依赖排序稳定
- `type` 的分类（1文档 2图片 3音乐 4视频 5压缩 6应用 7书籍）**不覆盖字幕**：
  `.ass` 文件不算「文档」。按后缀过滤要用 `suffix=`，或拉全量后在本地按扩展名分流

### 一次请求取整条祖先链：`GET {webapi}/files?cid=..&limit=1&hide_data=1`

响应里的 **`path` 数组就是从根到该目录的完整祖先链**，一次请求全拿到，
不必逐级打 `files/get_info`（那是每层一次请求 × 节流）。2026-09-19 实测：

- 元素字段 `cid` / `pid` / `name`（还有 `aid` / `isp`，非根元素多 `p_cid` / `iss` / `fv` / `fvs`）
- **首元素是根目录**：`{"cid":"0","pid":"0","name":"根目录"}` —— 拼路径时必须跳过，
  否则会得到 `/根目录/影视/...`
- **末元素就是请求的那个 cid 自身**
- `hide_data=1` 只影响 `data` 体积，**不影响 `path`**（1344 vs 1348 字节，path 完全一致），
  所以取祖先链时应该带上它

> ⚠️⚠️ **无效 / 不存在的 cid 会被静默当作根目录处理**，不报错：
> 响应仍是 `HTTP 200` + `state:true` + `errNo:0`，但响应体里的 `cid` 变成 `0`、
> `path` 只剩根那一级、`data` 返回的是**根目录的内容**。
>
> 这跟 `files/get_info` 的行为完全不同——后者对已删除目录会明确返回 `800001 目录不存在`。
> 所以改用本接口解析路径时**必须自己校验 `path` 末元素的 `cid` 等于请求的 cid**，
> 不等就判定「目录已消失」。漏了这一步，一个已删除父目录会解析成「网盘根」，
> 路径推导会指向媒体库根下的同名文件——删错东西的经典路径。
>
> （未验证：回收站里的目录是否返回 `aid=7` 而非 `aid=1`。稳妥起见一并校验 `aid=="1"`。）

### 递归列目录：`GET https://proapi.115.com/app/chrome/downfolders`

参数 `pickcode`（目录的 pickcode）、`page`、`per_page`（上限 5000）。
返回子树内**每一个目录**的 `fid` / `fn`(名字) / `pid`(父 id) —— 正好补上上面缺的路径信息。

配套的 `/app/chrome/downfiles` 递归返回所有文件的 `pc` / `pid` / `fs`，但**不带文件名**。

⚠️ 这是 115 客户端自用端点，不是公开 API，也**没有开放平台对应端点**。
调用方必须能降级（见 `collectSyncFiles`）。

### 生活事件 `behavior/detail`：两条通道字段几乎同构

| 通道 | 端点 |
|---|---|
| app | `GET https://proapi.115.com/{app}/behavior/detail`（app 一般取 `android` / `ios`） |
| web | `GET https://webapi.115.com/behavior/detail` |

参数 `type`（省略=全部）、`limit`、`offset`、`date`（可选 `YYYY-MM-DD`）。
2026-09-19 双通道实测对比（同一账号、同一时刻）：

- **条目字段 webapi 是 proapi 的超集**：webapi 多出 `iv` 与 **`parent_name`**，没有任何字段缺失。
  两边都有 `id` / `type` / `file_id` / `file_name` / `parent_id` / `pick_code` /
  `file_category` / `update_time` / `sha1` / `file_size`，所以 405 降级可以共用一套解析
- `parent_name`（父目录名）**只有 webapi 给**。它不足以拼出完整路径，
  但足够用来校验本地路径缓存是否陈旧——白送的一道保险
- `id` / `file_id` / `parent_id` 都是**字符串**返回，Go 侧没有大整数精度问题
  （openStrm 手写 `parseJsonBigIntSafe` 是 JS 的 `JSON.parse` 才需要）
- `type` / `file_category` 是**数字**返回（`6` 而不是 `"6"`）
- ⚠️ **`data.count` 两条通道类型不一样**：proapi 返回字符串 `"4"`，webapi 返回数字 `4`。
  声明成 `int` 会让 proapi 的整个响应解析失败。要翻页就得用 `json.RawMessage` 双形态解析

`file_category`：`0` = 目录，`1` = 文件。

### 「115 生活」事件记录总开关：`POST life.115.com/api/1.0/web/1.0/calendar/setoption`

表单 `locus=1&open_life=1`。**不开这个开关，`behavior/detail` 会一直返回空** ——
增量同步会永久静默空转，日志里连一行异常都没有。p115client 的 `life_show`、
p115strmhelper、openStrm 都在启动前调它。2026-09-19 实测返回：

```json
{"state":true,"code":0,"message":"","data":true}
```

⚠️ 两个坑：

1. **响应结构跟 webapi 不一样**：这里是 `state` / `code` / `message` / `data`，
   不是 webapi 惯用的 `state` / `errNo` / `error`。别套用通用响应结构体
2. **它对失效 cookie 也返回成功**，光看这个返回判断不了「请重新登录」。
   拿它当门禁必须再真拉一条事件（openStrm 在注释里专门记了这一条）

域名是 `life.115.com`，不在 `isThrottledHost` 的节流名单里（调用频率极低，无需节流）。

### 删除 = 进回收站，可还原

| 通道 | 端点 | 参数 |
|---|---|---|
| Cookie | `POST webapi.115.com/rb/delete` | `fid[0]` `fid[1]` …（115driver 的 `Delete` 同款） |
| OpenAPI | `POST proapi.115.com/open/ufile/delete` | `file_ids`（多个逗号分隔） |

`rb` = recycle bin。**删除是移进 115 回收站，用户能在网页端还原**，不是不可逆的抹除。
空目录自动清理敢做成默认行为就是基于这一点。p115client 提示单次别超过 5 万个、不要并发。

回收站相关：`/open/rb/list` 列、`/open/rb/revert` 还原、`/open/rb/del` 彻底删（**这个才不可逆**）。

### 分享转存：两个端点的 HTTP 方法不一样

| 端点 | 方法 | 参数 |
|---|---|---|
| `webapi.115.com/share/snap` | **GET** + query | `share_code` `receive_code` `cid` `offset` `limit`（上限 1150）`asc` `fc_mix` |
| `webapi.115.com/share/receive` | **POST** + form | `share_code` `receive_code` `file_id`（多个逗号分隔）`cid`（目标目录） |

一前一后方法相反，很容易顺手都写成 GET。用 GET 打 `receive` 会拿到：

```json
{"state":false,"error":"405 METHOD NOT ALLOWED","errNo":980005,"request":"/share/receive?cid=..."}
```

这条错误跟「链接失效」「提取码错误」长得完全不一样，别往那边排查。
权威签名见 `p115client/client.py` 的 `share_receive`。

已作废的端点：`POST /share/info`（恒返「服务器开小差」）、`POST /share/snap`（405）、
`POST /share/sharepost` + `files/receive`（逐个转存，现在一次 `receive` 带逗号分隔的 file_id 即可）。

分享类请求带上分享页 Referer 更稳：`https://115cdn.com/s/{share_code}?password={code}&`
（`115driver` 的 `BuildShareReferer` 同款）。

### pickcode ⇄ id 是纯本地换算

替换表 + 36 进制，不需要任何请求。目录 pickcode 以 `fa`~`fe` 开头，文件以 `a`~`e` 开头，
末 4 位是该账号固定的「不动点」（首字符恒为 `0`），可由任意一个已知 pickcode 反推。

所以从媒体库 cid 算出目录 pickcode 去打 `downfolders` 是零成本的。

### 递归模式下拿不到的东西

- **文件夹无法递归获取**（除了上面的 `downfolders`）。`r_all` / `stdir` / `fc` /
  `min_size` / `type=0` 都试过，全部只返回直接子项
- 条目里**没有完整路径**，只有父目录 cid，路径必须靠目录表在本地重建

---

## 四、探测脚本

这几轮探测用的 PowerShell 脚本没有入库（一次性工具）。要重新验证 115 接口行为时，
模式是：`Invoke-WebRequest` + `Cookie` 头 + `Mozilla/5.0 115Browser/{版本}` UA，
请求间隔 800ms。从浏览器 F12 → Network → 任意 115 请求复制整条 Cookie 即可。
