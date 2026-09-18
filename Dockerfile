# 多阶段构建：编译 Go 二进制
# --platform=$BUILDPLATFORM 让 builder 始终以宿主原生架构运行（CI 上是 amd64），
# 所有 RUN 不经过 QEMU；配合 GOARCH=$TARGETARCH 交叉编译出目标架构二进制。
# 若不固定 BUILDPLATFORM，arm64 构建的 RUN 会在 QEMU 模拟的 arm64 容器里执行，极慢
# golang 1.25：依赖链（tls-client 等）要求 go >= 1.24，go.mod 已升到 1.25
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
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -ldflags="-s -w -X main.BuildSHA=${BUILD_SHA}" -o strmhub .

# 运行阶段：最小镜像
FROM alpine:latest
# ffmpeg：观影门户服务端转封装（MKV→HLS 无损直串流）、内嵌轨道识别与媒体信息探测；
# ca-certificates（TLS）、tzdata（时区）
RUN apk add --no-cache ffmpeg ca-certificates tzdata

WORKDIR /app

# 复制编译好的二进制
COPY --from=builder /build/strmhub .
# 复制前端静态资源
COPY --from=builder /build/web ./web

# 6060 管理后台 / 6086 302直链代理 / 6688 观影门户
EXPOSE 6060 6086 6688

VOLUME ["/config", "/data", "/media", "/logs"]

ENTRYPOINT ["./strmhub"]
