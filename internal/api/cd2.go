package api

// CloudDrive2（CD2）接入——定位是「整理引擎」而非播放链路：
// CD2 把几十种网盘（115/百度/天翼/OneDrive/S3…）统一成一个 gRPC
// 服务，StrmHub 用它做跨网盘的识别/重命名/分类/搬运（见 cd2org.go）。
// 整理目标指向 CD2 挂载的 115 媒体库路径，文件落位后由项目原生的增量
// 同步生成 STRM（/d/ 直链播放）——CD2 不在播放路径上。
//
// 本文件：配置读写、连接测试、目录浏览（选择器用）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"strmhub/internal/cd2"

	"github.com/gin-gonic/gin"
)

type cd2Cfg struct {
	Endpoint    string `json:"endpoint"` // host:port 或 http(s)://host:port，默认端口 19798
	Username    string `json:"username"`
	Password    string `json:"password"`
	RootPath    string `json:"root_path"`    // CD2 内的整理目标根（建议 115 挂载内的媒体库路径）
	OrgEnabled  bool   `json:"org_enabled"`  // 实时监控整理开关
	OrgPending  string `json:"org_pending"`  // 监控（待整理）目录，CD2 绝对路径，任意网盘
	OrgExisting string `json:"org_existing"` // 已存在目录（去重命中/洗版淘汰旧版去向），建议在整理目标根之外
}

func (h *Handler) loadCd2Cfg() cd2Cfg {
	c := cd2Cfg{}
	if v := h.Config.GetSetting("cd2"); v != "" {
		_ = json.Unmarshal([]byte(v), &c)
	}
	return c
}

func (h *Handler) saveCd2Cfg(c cd2Cfg) {
	b, _ := json.Marshal(c)
	h.Config.SaveSetting("cd2", string(b))
}

// ==================== 客户端缓存（配置变更时自动重建） ====================

var (
	cd2ClientMu  sync.Mutex
	cd2ClientVal *cd2.Client
	cd2ClientKey string
)

func (h *Handler) cd2Client() (*cd2.Client, error) {
	cfg := h.loadCd2Cfg()
	if strings.TrimSpace(cfg.Endpoint) == "" || cfg.Username == "" {
		return nil, fmt.Errorf("请先配置 CD2 服务地址与账号")
	}
	target, err := cd2GrpcTarget(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	key := target + "\x00" + cfg.Username + "\x00" + cfg.Password
	cd2ClientMu.Lock()
	defer cd2ClientMu.Unlock()
	if cd2ClientVal == nil || cd2ClientKey != key {
		if cd2ClientVal != nil {
			_ = cd2ClientVal.Close()
		}
		cd2ClientVal = cd2.NewClient(target, cfg.Username, cfg.Password)
		cd2ClientKey = key
	}
	return cd2ClientVal, nil
}

// cd2GrpcTarget 把用户输入规范化为 grpc target（host:port）。
// 支持带 http(s):// 前缀；缺端口时补默认 19798
func cd2GrpcTarget(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("CD2 地址为空")
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return "", fmt.Errorf("CD2 地址无效: %s", raw)
		}
		s = u.Host
	}
	if !strings.Contains(s, ":") || strings.HasSuffix(s, "]") {
		s += ":19798"
	}
	return s, nil
}

// ==================== HTTP handlers ====================

// Cd2GetConfig GET /cd2/config
func (h *Handler) Cd2GetConfig(c *gin.Context) {
	cfg := h.loadCd2Cfg()
	if cfg.Password != "" {
		cfg.Password = settingMask
	}
	c.JSON(http.StatusOK, gin.H{"data": cfg})
}

// Cd2SaveConfig POST /cd2/config（密码掩码回传 = 保持旧值）
func (h *Handler) Cd2SaveConfig(c *gin.Context) {
	var req cd2Cfg
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	old := h.loadCd2Cfg()
	if req.Password == settingMask {
		req.Password = old.Password
	}
	// 三个目录均已自动派生（前端不再填写）：请求里为空时保留旧派生值，
	// 避免把派生缓存冲掉
	if req.RootPath == "" {
		req.RootPath = old.RootPath
	}
	if req.OrgPending == "" {
		req.OrgPending = old.OrgPending
	}
	if req.OrgExisting == "" {
		req.OrgExisting = old.OrgExisting
	}
	if _, err := cd2GrpcTarget(req.Endpoint); req.Endpoint != "" && err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.saveCd2Cfg(req)
	if req.OrgEnabled {
		// 开启状态下保存：立即按最新 115 目录刷新派生目录
		//（失败保留旧值，监控循环会自动重试）
		if err := h.cd2RefreshOrgDirs(); err != nil {
			c.JSON(http.StatusOK, gin.H{"message": "已保存（目录派生暂失败，沿用旧值稍后自动重试: " + err.Error() + "）"})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

// Cd2Test POST /cd2/test：登录并列出根目录（根目录即各网盘挂载点）
func (h *Handler) Cd2Test(c *gin.Context) {
	cl, err := h.cd2Client()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	start := time.Now()
	files, err := cl.ListDir(ctx, "/")
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	var drives []string
	for _, f := range files {
		if f.IsDir && len(drives) < 8 {
			drives = append(drives, f.Name)
		}
	}
	msg := fmt.Sprintf("连接成功（%dms），CD2 根目录 %d 项：%s",
		time.Since(start).Milliseconds(), len(files), strings.Join(drives, "、"))
	c.JSON(http.StatusOK, gin.H{"message": msg})
}

// Cd2Dirs GET /cd2/dirs?path=/：目录浏览（只返回文件夹），供前端目录选择器
func (h *Handler) Cd2Dirs(c *gin.Context) {
	pathParam := strings.TrimSpace(c.Query("path"))
	if pathParam == "" {
		pathParam = "/"
	}
	cl, err := h.cd2Client()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	files, err := cl.ListDir(ctx, pathParam)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	dirs := []gin.H{}
	for _, f := range files {
		if f.IsDir {
			dirs = append(dirs, gin.H{"name": f.Name, "path": f.Path})
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": dirs, "path": pathParam, "count": len(dirs)})
}
