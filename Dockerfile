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
# CI 注入提交号（版本标识，启动日志里可确认运行的是哪个提交）
ARG BUILD_SHA=dev

WORKDIR /build

# 先复制依赖文件，利用 Docker 缓存
COPY go.mod go.sum ./
RUN go mod download

# 复制源码
COPY . .

# 编译（CGO 禁用，按目标架构交叉编译；注入版本标识）
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -ldflags="-s -w -X main.BuildSHA=${BUILD_SHA}" -o 115-station .

# 运行阶段：最小镜像
FROM alpine:latest
# ffmpeg：内嵌轨道识别与媒体信息探测；
# ca-certificates（TLS）、tzdata（时区）
RUN apk add --no-cache ffmpeg ca-certificates tzdata

WORKDIR /app

# 复制编译好的二进制
COPY --from=builder /build/115-station .
# 新前端产物（默认服务的就是它）
COPY --from=webbuilder /webui/dist ./webui/dist

# 6060 管理后台 / 6086 302直链代理
EXPOSE 6060 6086

VOLUME ["/config", "/data", "/media", "/logs"]

ENTRYPOINT ["./115-station"]
