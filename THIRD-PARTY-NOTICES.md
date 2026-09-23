# 第三方组件声明（THIRD-PARTY NOTICES）

本项目分发时包含下列第三方组件。它们各自的许可证义务**独立于**本仓库自身的
许可证状况（见 [README.md 的「许可证与再分发声明」](README.md#许可证与再分发声明)）——
即便本仓库刻意不附带 LICENSE 文件，这些组件要求的版权声明与许可证副本仍然必须随附。

许可证全文在 [`licenses/`](licenses/) 目录。

---

## 随二进制/镜像分发的组件

| 组件 | 路径 | 许可证 | 全文 |
|---|---|---|---|
| 霞鹜文楷 LXGW WenKai | `internal/api/assets/lxgwwenkai-medium.ttf`（`//go:embed` 进二进制） | SIL OFL 1.1 | [licenses/OFL-1.1-LXGWWenKai.txt](licenses/OFL-1.1-LXGWWenKai.txt) |
| 得意黑 Smiley Sans | `internal/api/assets/smileysans-oblique.ttf`（`//go:embed` 进二进制） | SIL OFL 1.1 | [licenses/OFL-1.1-SmileySans.txt](licenses/OFL-1.1-SmileySans.txt) |
| 站酷小薇体 ZCOOL XiaoWei | `internal/api/assets/zcoolxiaowei-regular.ttf`（`//go:embed` 进二进制） | SIL OFL 1.1 | [licenses/OFL-1.1-ZCOOLXiaoWei.txt](licenses/OFL-1.1-ZCOOLXiaoWei.txt) |
| CodeMirror 5.65.16 | 旧前端曾使用，现已移除；保留许可副本 | MIT | [licenses/MIT-CodeMirror5.txt](licenses/MIT-CodeMirror5.txt) |
| FFmpeg / ffprobe | 运行镜像内由 `apk add ffmpeg` 安装，未修改、未静态链接进本项目二进制 | LGPL / GPL（取决于 Alpine 构建选项） | 见镜像内 `/usr/share/licenses` 与 [Alpine ffmpeg 包](https://pkgs.alpinelinux.org/package/edge/community/x86_64/ffmpeg) |
| Go 依赖 | 见 [`go.mod`](go.mod) / [`go.sum`](go.sum) | 各依赖自有许可证 | `go mod download` 后见各模块目录 |
| 前端 npm 依赖 | 见 [`webui/package.json`](webui/package.json) | 各依赖自有许可证 | `npm ci` 后见 `node_modules/*/LICENSE` |

> **关于三款海报字体（已修改）**：嵌入的是子集而非原版——只保留约八千个常用汉字
> （《通用规范汉字表》∪ GB2312）与西文/标点，并去掉 hinting、字形名和竖排度量，
> 三款合计从约 34MB 降到约 10MB。按 OFL 第 3 条，Modified Version 不得以保留字体名
> 作为主名称，因此子集的家族名改为 `StrmStation Cover WK / SM / XW`；原版权与许可证
> 条目保留在字体 name 表内。生成脚本：`internal/api/assets/subset_fonts.py`。

> **关于 CodeMirror（历史组件，现已移除）**：`web/vendor/cm5/*.min.js` 是 jsDelivr 用 Terser 压缩的产物，
> 压缩过程删掉了原文件顶部的 MIT 版权声明。MIT 要求保留该声明，因此在
> `licenses/MIT-CodeMirror5.txt` 中补回。

## 上游项目

本项目由 [DaisyYijin/STRMhub](https://github.com/DaisyYijin/STRMhub) 二次开发而来。
上游截至本文撰写时未附带 LICENSE 文件，相关说明与本仓库的应对方式见
[README.md](README.md#许可证与再分发声明)。

开发过程中参考过 `ChenyangGao/p115client`、`SheltonZhu/115driver`、`qicfan/qmediasync`、
`DDSRem/MoviePilot-Plugins` 的 `p115strmhelper`、openStrm、MediaSync115 等开源项目——
阅读其接口形态与实现思路，未整段复制代码。具体到某处实现借鉴了哪个项目，
在对应源码的注释里写明（例如 [`internal/api/pickcode115.go`](internal/api/pickcode115.go)）。
