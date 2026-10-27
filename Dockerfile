# 多阶段构建：前端（Node）+ Go 二进制
#
# 前端阶段同样固定 BUILDPLATFORM：产物是与架构无关的静态 HTML，
# 没有任何理由让它在 QEMU 模拟的 arm64 里跑 npm。
FROM --platform=$BUILDPLATFORM node:22-alpine AS webbuilder
WORKDIR /webui
# 先装依赖再拷源码，改前端代码不会让 npm ci 缓存失效
COPY webui/package.json webui/package-lock.json ./
RUN npm ci
COPY webui/ ./
# 产物是单个 dist/index.html（JS/CSS 全部内联）
RUN npm run build

# 编译 Go 二进制
# --platform=$BUILDPLATFORM 让 builder 始终以宿主原生架构运行（CI 上是 amd64），
# 所有 RUN 不经过 QEMU；配合 GOARCH=$TARGETARCH 交叉编译出目标架构二进制。
# 若不固定 BUILDPLATFORM，arm64 构建的 RUN 会在 QEMU 模拟的 arm64 容器里执行，极慢
# golang 1.25：go.mod 声明 go 1.25.0，构建镜像的工具链不得低于该版本
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

# buildx 多架构构建时自动注入目标架构（amd64/arm64）
ARG TARGETARCH

WORKDIR /build

# 先复制依赖文件，利用 Docker 缓存
COPY go.mod go.sum ./
RUN go mod download

# 复制源码
COPY . .

# CI 注入提交号与版本号（git describe，如 v1.2.0 / v1.2.0-3-gabc1234）。
# 构建上下文排除了 .git，版本号只能从外面传进来；本地 docker build 不传就是 dev，不做更新提示
# 放在 go mod download 之后声明：ARG 值每个提交都变，声明在前面会让依赖下载那层缓存每次都失效
ARG BUILD_SHA=dev
ARG APP_VERSION=dev
# 编译（CGO 禁用，按目标架构交叉编译；注入版本标识）
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -ldflags="-s -w -X main.BuildSHA=${BUILD_SHA} -X main.Version=${APP_VERSION}" -o 115-station .

# 运行阶段：debian-slim（2026-10-14 起，原来是 alpine）
# RE0 网页登录用的 CloakBrowser 只有 glibc 构建，alpine（musl）跑不了。浏览器本体（约 210MB）
# 授权上不能随镜像分发，由程序在用户启用 RE0 网页登录时下载到 /data（re0browser.go）；
# 这里只装它运行要的系统库（Playwright 给 Debian 12 列的 Chromium 依赖）和一套字体。
# 不再带 ffmpeg：「媒体补全」与刮削「轨道探测」写 NFO 已删（2026-09-29），轨道信息交给 Emby 自己探测
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends         ca-certificates tzdata fonts-liberation         libasound2 libatk-bridge2.0-0 libatk1.0-0 libatspi2.0-0 libcairo2 libcups2 libdbus-1-3         libdrm2 libgbm1 libglib2.0-0 libnspr4 libnss3 libpango-1.0-0 libx11-6 libxcb1         libxcomposite1 libxdamage1 libxext6 libxfixes3 libxkbcommon0 libxrandr2     && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# 运行环境版本号：一键更新只换程序与前端，不换这一层往上的东西（internal/api/selfupdate.go）。
# 改了运行阶段（基础镜像、apt 装的包、目录布局）要把它加一，新旧不一样时界面会让用户 docker compose pull
ENV STATION_RUNTIME=1

# 复制编译好的二进制
COPY --from=builder /build/115-station .
# 新前端产物（默认服务的就是它）
COPY --from=webbuilder /webui/dist ./webui/dist

# 6060 管理后台 / 6086 302直链代理
EXPOSE 6060 6086

VOLUME ["/config", "/data", "/media", "/logs"]

ENTRYPOINT ["./115-station"]
