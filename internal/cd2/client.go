// Package cd2 提供 CloudDrive2（CD2）gRPC 客户端。
//
// CD2 把几十种网盘（115/百度/天翼/OneDrive/S3…）统一成一个
// gRPC 服务（默认端口 19798，明文 HTTP/2）。本包只封装只读能力：
//
//	GetToken    用户名密码换 JWT（返回 expiration，过期自动重登）
//	GetSubFiles 列目录（服务端流式，按批返回 CloudDriveFile）
//	GetDownloadUrlPath 取播放地址：
//	  - directUrl        网盘原始直链（可能要求特定 UA/请求头，见 UserAgent/AdditionalHeaders）
//	  - downloadUrlPath  CD2 中转地址模板，形如 /static/{SCHEME}/{HOST}/{PREVIEW}/path?token=…，
//	                    替换 {SCHEME}/{HOST}/{PREVIEW} 后即得 CD2 服务器上的完整 URL，
//	                    由 CD2 中转流量，对任何网盘都可用且不受 UA 限制
//
// 认证失败（Unauthenticated）时自动重登一次再重试。
package cd2

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	pb "strmhub/internal/cd2/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const cd2Service = "clouddrive.CloudDriveFileSrv"

// Client 线程安全；endpoint 形如 host:port（可带 http(s):// 前缀，默认端口 19798）
type Client struct {
	endpoint string // grpc target（host:port）
	username string
	password string

	mu          sync.Mutex
	conn        *grpc.ClientConn
	token       string
	tokenExpiry time.Time
}

// NewClient 创建客户端（惰性连接，首次调用时拨号）
func NewClient(endpoint, username, password string) *Client {
	return &Client{endpoint: endpoint, username: username, password: password}
}

// Close 释放底层连接
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}

func (c *Client) getConn() (*grpc.ClientConn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return c.conn, nil
	}
	conn, err := grpc.NewClient(c.endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("连接 CD2 失败: %w", err)
	}
	c.conn = conn
	return conn, nil
}

// ensureTokenLocked 确保 JWT 有效（过期前 5 分钟内视为失效）；调用方须持有 c.mu
func (c *Client) ensureTokenLocked() error {
	if c.token != "" && time.Now().Before(c.tokenExpiry.Add(-5*time.Minute)) {
		return nil
	}
	conn, err := c.getConn()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req := &pb.GetTokenRequest{UserName: c.username, Password: c.password}
	var out pb.JWTToken
	if err := conn.Invoke(ctx, cd2Service+"/GetToken", req, &out); err != nil {
		return fmt.Errorf("CD2 登录失败: %w", err)
	}
	if !out.Success || out.Token == "" {
		return fmt.Errorf("CD2 登录失败: %s", orDefault(out.ErrorMessage, "账号或密码错误"))
	}
	c.token = out.Token
	if out.Expiration != nil {
		c.tokenExpiry = out.Expiration.AsTime()
	} else {
		c.tokenExpiry = time.Now().Add(12 * time.Hour)
	}
	return nil
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func (c *Client) invalidateToken() {
	c.mu.Lock()
	c.token = ""
	c.mu.Unlock()
}

func authErr(err error) bool {
	code := status.Code(err)
	return code == codes.Unauthenticated || code == codes.PermissionDenied
}

// withAuth 返回带 Bearer 的 ctx（必要时登录）
func (c *Client) withAuth(ctx context.Context) (context.Context, error) {
	c.mu.Lock()
	err := c.ensureTokenLocked()
	tok := c.token
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+tok)), nil
}

// File 目录项（只取 STRM/整理相关字段）
type File struct {
	Name  string
	Path  string // CD2 内绝对路径
	Size  int64
	IsDir bool
	Sha1  string // 网盘提供时用于去重（fileHashes[HashType.Sha1]）
}

// ListDir 列出一层目录（GetSubFiles 服务端流，可能分批返回）
func (c *Client) ListDir(ctx context.Context, path string) ([]File, error) {
	files, err := c.listDirOnce(ctx, path)
	if authErr(err) {
		c.invalidateToken()
		files, err = c.listDirOnce(ctx, path)
	}
	return files, err
}

func (c *Client) listDirOnce(ctx context.Context, path string) ([]File, error) {
	actx, err := c.withAuth(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := c.getConn()
	if err != nil {
		return nil, err
	}
	desc := &grpc.StreamDesc{StreamName: "GetSubFiles", ServerStreams: true}
	stream, err := conn.NewStream(actx, desc, cd2Service+"/GetSubFiles")
	if err != nil {
		return nil, fmt.Errorf("CD2 列目录失败: %w", err)
	}
	if err := stream.SendMsg(&pb.ListSubFileRequest{Path: path}); err != nil {
		return nil, err
	}
	if err := stream.CloseSend(); err != nil {
		return nil, err
	}
	var files []File
	for {
		reply := &pb.SubFilesReply{}
		if err := stream.RecvMsg(reply); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("CD2 列目录中断（%s）: %w", path, err)
		}
		for _, f := range reply.SubFiles {
			if f == nil {
				continue
			}
			fi := File{Name: f.Name, Path: f.FullPathName, Size: f.Size, IsDir: f.IsDirectory}
			if sha, ok := f.FileHashes[2]; ok { // HashType: Sha1=2
				fi.Sha1 = strings.ToLower(sha)
			}
			files = append(files, fi)
		}
	}
	return files, nil
}

// ==================== 写操作（整理用） ====================

// invoke 带认证的一元调用，Unauthenticated 时重登一次再重试
func (c *Client) invoke(ctx context.Context, method string, in, out interface{}) error {
	actx, err := c.withAuth(ctx)
	if err != nil {
		return err
	}
	conn, err := c.getConn()
	if err != nil {
		return err
	}
	err = conn.Invoke(actx, method, in, out)
	if authErr(err) {
		c.invalidateToken()
		actx, err = c.withAuth(ctx)
		if err != nil {
			return err
		}
		return conn.Invoke(actx, method, in, out)
	}
	return err
}

func opResultErr(res *pb.FileOperationResult) error {
	if res == nil {
		return nil
	}
	if !res.Success && !strings.Contains(strings.ToLower(res.ErrorMessage), "exist") {
		return fmt.Errorf("%s", res.ErrorMessage)
	}
	return nil
}

// EnsureDir 逐级创建目录（已存在视为成功）。dir 为 CD2 绝对路径
func (c *Client) EnsureDir(ctx context.Context, dir string) error {
	segs := strings.Split(strings.Trim(dir, "/"), "/")
	cur := ""
	for _, seg := range segs {
		if seg == "" {
			continue
		}
		req := &pb.CreateFolderRequest{ParentPath: cur, FolderName: seg}
		var out pb.CreateFolderResult
		if err := c.invoke(ctx, cd2Service+"/CreateFolder", req, &out); err != nil {
			// 目录已存在时 CD2 可能以 gRPC 错误或错误消息返回，都放行
			if !strings.Contains(strings.ToLower(err.Error()), "exist") {
				return fmt.Errorf("创建目录 %s/%s 失败: %w", cur, seg, err)
			}
		} else if err := opResultErr(out.Result); err != nil {
			return fmt.Errorf("创建目录 %s/%s 失败: %w", cur, seg, err)
		}
		if cur == "" {
			cur = "/" + seg
		} else {
			cur = cur + "/" + seg
		}
	}
	return nil
}

// MoveFiles 把若干绝对路径移动到 destDir（同名跳过）
func (c *Client) MoveFiles(ctx context.Context, paths []string, destDir string) error {
	skip := pb.MoveFileRequest_Skip
	req := &pb.MoveFileRequest{TheFilePaths: paths, DestPath: destDir, ConflictPolicy: &skip}
	var out pb.FileOperationResult
	if err := c.invoke(ctx, cd2Service+"/MoveFile", req, &out); err != nil {
		return err
	}
	return opResultErr(&out)
}

// Rename 原地重命名（同目录）
func (c *Client) Rename(ctx context.Context, path, newName string) error {
	req := &pb.RenameFileRequest{TheFilePath: path, NewName: newName}
	var out pb.FileOperationResult
	if err := c.invoke(ctx, cd2Service+"/RenameFile", req, &out); err != nil {
		return err
	}
	return opResultErr(&out)
}

// ==================== 实时变更推送 ====================

// Change 一条文件系统变更事件
type Change struct {
	Type    string // create / delete / rename
	IsDir   bool
	Path    string
	NewPath string // 仅 rename
}

// WatchOnce 订阅 PushMessage 流直到出错或 ctx 结束，把 FILE_SYSTEM_CHANGE
// 事件回调给 onChange。重连策略由调用方负责
func (c *Client) WatchOnce(ctx context.Context, onChange func(Change)) error {
	actx, err := c.withAuth(ctx)
	if err != nil {
		return err
	}
	conn, err := c.getConn()
	if err != nil {
		return err
	}
	desc := &grpc.StreamDesc{StreamName: "PushMessage", ServerStreams: true}
	stream, err := conn.NewStream(actx, desc, cd2Service+"/PushMessage")
	if err != nil {
		return fmt.Errorf("订阅 CD2 推送失败: %w", err)
	}
	if err := stream.SendMsg(&emptypb.Empty{}); err != nil {
		return err
	}
	if err := stream.CloseSend(); err != nil {
		return err
	}
	for {
		msg := &pb.CloudDrivePushMessage{}
		if err := stream.RecvMsg(msg); err != nil {
			if err == io.EOF || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("CD2 推送流中断: %w", err)
		}
		if msg.GetMessageType() != pb.CloudDrivePushMessage_FILE_SYSTEM_CHANGE {
			continue
		}
		fc := msg.GetFileSystemChange()
		if fc == nil {
			continue
		}
		ch := Change{IsDir: fc.GetIsDirectory(), Path: fc.GetPath(), NewPath: fc.GetNewPath()}
		switch fc.GetChangeType() {
		case pb.FileSystemChange_CREATE:
			ch.Type = "create"
		case pb.FileSystemChange_DELETE:
			ch.Type = "delete"
		case pb.FileSystemChange_RENAME:
			ch.Type = "rename"
		default:
			continue
		}
		onChange(ch)
	}
}
