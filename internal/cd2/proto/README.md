# CloudDrive2 proto

`clouddrive.proto` 是官方全量 proto（来源 https://www.clouddrive2.com/api/clouddrive.proto ，版本见文件头 `option (version)`）。

生成的 Go 代码在 `../pb/clouddrive.pb.go`（已提交，CI/Docker 构建无需 protoc）。

## 重新生成（更新 proto 后）

```bash
# 一次性安装插件（版本与 go.mod 里 protobuf 保持一致）
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.1

# 用项目自带的纯 Go 编译驱动（无 protoc 依赖）
cd ../../.tools/protogen
go run . ../../internal/cd2/proto ../../internal/cd2/pb
```

## 说明

- 客户端只用了 `GetToken` / `GetSubFiles` / `GetDownloadUrlPath` 三个 RPC，
  通过 `grpc.ClientConn.Invoke/NewStream` 直接调用（未生成 service 桩代码）。
- proto3 解码会忽略未知字段：CD2 服务端新增字段不影响旧客户端。
