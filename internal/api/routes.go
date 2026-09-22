package api

import (
	"115-station/internal/config"
	"115-station/internal/model"
	"archive/zip"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

// buildVersion 构建版本号（main 注入，侧边栏/日志确认运行版本）
var buildVersion = "dev"

// loginGuardEntry 登录防爆破计数
type loginGuardEntry struct {
	fails     int
	lockUntil time.Time
	lastFail  time.Time
}

var (
	loginGuard   = map[string]loginGuardEntry{}
	loginGuardMu sync.Mutex
)

// loginGuardCheck 防爆破闸门：命中锁定返回剩余时间，未锁定返回 0。
// key 使用客户端 IP，避免伪造反代头绕过登录限制。
func loginGuardCheck(key string) time.Duration {
	loginGuardMu.Lock()
	defer loginGuardMu.Unlock()
	if g, ok := loginGuard[key]; ok && time.Now().Before(g.lockUntil) {
		return time.Until(g.lockUntil)
	}
	return 0
}

// loginGuardFail 记一次失败：连续 5 次锁 10 分钟；顺带清理过期条目防无界增长
func loginGuardFail(key string) {
	loginGuardMu.Lock()
	defer loginGuardMu.Unlock()
	now := time.Now()
	for k, v := range loginGuard {
		// 锁定已过期且 10 分钟内无新失败的条目可删
		if now.After(v.lockUntil) && now.Sub(v.lastFail) > 10*time.Minute {
			delete(loginGuard, k)
		}
	}
	g := loginGuard[key]
	g.fails++
	g.lastFail = now
	if g.fails >= 5 {
		g.lockUntil = now.Add(10 * time.Minute)
		g.fails = 0
	}
	loginGuard[key] = g
}

// loginGuardPass 登录成功：清除记录
func loginGuardPass(key string) {
	loginGuardMu.Lock()
	defer loginGuardMu.Unlock()
	delete(loginGuard, key)
}

// SetVersion 注入构建版本号
func SetVersion(v string) { buildVersion = v }

func SetupRoutes(r *gin.RouterGroup, db *gorm.DB, cfg *config.Config) {
	// 注入通知配置读取源（YAML 优先，DB 回退）
	notifyConfigSource = cfg
	h := &Handler{DB: db, Config: cfg}
	cfgGlobal = cfg

	// 应用用户设置的 115 API 请求间隔（数据库 > 环境变量 > 默认 1s）
	Apply115Interval(db)

	// 启动同步 cron 调度器（每分钟检查 full / incr 配置）
	StartSyncScheduler(h)

	// 启动转存目录守望者（下载完成后 ~1 分钟自动整理）
	StartTransferWatcher(h)

	// 启动 115 每日签到调度器（配置时间窗口内随机执行）
	Start115CheckinScheduler(h)
	StartCoverGenScheduler(h)
	StartTgSubScheduler(h)

	// 启动离线任务监视器（完成即触发整理；失败告警——磁力不是百分百成功）
	StartOfflineTaskMonitor(h)

	// 媒体卷宽松权限（存量补 chmod，异步）
	h.RelaxedMediaPerms()

	// 元数据回传（本地媒体树的 poster/nfo → 115 对应目录）
	StartMetadataUploader(h)

	// 媒体信息补全队列（ffprobe 探测 → 规范重命名）
	StartEnrichWorker(h)

	// 启动监控上传引擎（Emby 生成图片回传 115）
	StartMonitorUploader(h)

	// 认证（账号由环境变量 AUTH_USER/AUTH_PASSWORD 提供或启动时自动生成，
	// 网页注册已移除）
	auth := r.Group("/auth")
	{
		auth.GET("/status", h.AuthStatus) // 检查是否已初始化（前端提示文案用）
		auth.POST("/login", h.Login)      // 登录
	}

	// Emby Webhook 接收端（无需登录鉴权：Emby 服务器推送事件，token 可选）
	r.POST("/emby/webhook", h.EmbyWebhook)
	r.GET("/emby/webhook", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "115-Station Emby Webhook 接收端就绪，请使用 POST 推送事件"})
	})

	// 以下接口需要认证
	protected := r.Group("/")
	protected.Use(h.AuthMiddleware())
	{
		// 仪表盘
		protected.GET("/dashboard", h.DashboardEnhanced)
		// 媒体库台账校准（只读本地 STRM 树 → 清 MediaLibrary 幽灵行，见 medialib.go）
		protected.POST("/media-library/calibrate", h.CalibrateMediaLibrary)

		// 账号
		protected.POST("/auth/update-account", h.UpdateAccount)

		// 代理测试
		protected.POST("/proxy/test", h.TestProxyLatency)
		protected.GET("/network/check", h.NetworkCheck)

		// 存储管理
		protected.GET("/storage", h.ListStorage)
		protected.POST("/storage", h.CreateStorage)
		protected.DELETE("/storage/:id", h.DeleteStorage)
		protected.POST("/storage/check", h.CheckStorage)

		// 115 扫码登录
		protected.POST("/storage/qrcode", h.CreateQrCode)
		protected.POST("/storage/qrcode/status", h.QrCodeStatus)

		// 115 开放平台扫码授权（OpenAPI）
		protected.POST("/storage/open/qrcode", h.CreateOpenQrCode)
		protected.POST("/storage/open/qrcode/status", h.OpenQrCodeStatus)

		// 目录浏览
		protected.GET("/storage/115/dirs", h.List115Dirs)
		protected.GET("/storage/115/resolve", h.Resolve115Path)
		protected.GET("/storage/115/path", h.Resolve115CID)
		protected.GET("/storage/local/dirs", h.ListLocalDirs)

		// 核心配置（TMDB）
		protected.GET("/config/tmdb", h.GetTmdbConfig)
		protected.POST("/config/tmdb", h.SaveTmdbConfig)
		protected.GET("/config/setting", h.GetSetting)
		protected.POST("/config/setting", h.SaveSetting)
		protected.POST("/config/test-ai", h.TestAIConnection)
		protected.POST("/config/test-tmdb", h.TestTMDBConnection)

		// Emby 连接测试
		protected.POST("/config/test-emby", h.TestEmbyConnection)

		// 消息通知
		protected.POST("/message/test", h.TestMessage)
		protected.GET("/message/tg-status", func(c *gin.Context) {
			c.JSON(http.StatusOK, TelegramBotStatus())
		})

		// 归档同步
		protected.GET("/sync/tasks", h.ListSyncTasks)
		protected.POST("/sync/tasks", h.CreateSyncTask)
		protected.POST("/sync/tasks/:id/run", h.RunSyncTask)
		protected.GET("/sync/tasks/:id/logs", h.GetSyncLogs)
		protected.POST("/sync/full", h.RunFullSync)
		protected.POST("/sync/incremental", h.RunIncrementalSync)
		// 增量事件流状态与探针（回答「为什么没同步」：门禁/通道/游标/上一轮结果）
		protected.GET("/sync/incr-status", h.IncrStatus)
		protected.POST("/sync/incr-probe", h.IncrProbe)
		// 快速模式是否可用（规则在后端，前端不自行按通道推断）
		protected.GET("/sync/capabilities", h.SyncCapabilities)
		// 失效 STRM（本地还在、网盘已删）预览与清理：全量同步只打标，删除必须用户确认
		protected.GET("/sync/orphans", h.ListOrphans)
		protected.POST("/sync/orphans/clean", h.CleanOrphans)
		// 深度删除（本地已删、网盘还在）：上一条的镜像，删的是网盘源文件。
		protected.GET("/sync/deep-delete/records", h.ListDeepDeleteRecords)

		// Cron 未来运行时间预览（校验表达式是否正确）
		protected.POST("/sync/cron-preview", func(c *gin.Context) {
			var req struct {
				Cron string `json:"cron"`
			}
			if err := c.ShouldBindJSON(&req); err != nil || req.Cron == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "请填写 cron 表达式"})
				return
			}
			var next []string
			t := time.Now()
			for i := 0; i < 5; i++ {
				t = nextCronTime(req.Cron, t)
				if t.IsZero() {
					break
				}
				next = append(next, t.Format("01-02 15:04"))
			}
			if len(next) == 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "cron 表达式无效或未来一年内不会触发"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"next": next})
		})

		// 任务状态（前端轮询：任务进行中禁用同步/整理按钮）+ 最近运行记录
		protected.GET("/sync/status", func(c *gin.Context) {
			running, name, start, progress := TaskStatus()
			g := gin.H{"running": running}
			if running {
				g["task"] = name
				g["since"] = start.Format("15:04:05")
				g["elapsed"] = time.Since(start).Truncate(time.Second).String()
				if progress != "" {
					g["progress"] = progress
				}
			}
			g["recent"] = GetRecentRuns()
			c.JSON(http.StatusOK, g)
		})

		// STRM 管理
		// 302 直连（与 6086 代理同款，6060 也能作为 strm 直连地址，CMS 二合一模式）
		registerDirectPlaybackRoutes(r, h.DB, h.Config)
		// RE0 OAuth 回调（浏览器地址栏跳转，无鉴权头，必须公开；靠 state 防 CSRF）
		r.GET("/re0/oauth/callback", h.Re0OAuthCallback)
		// TMDB 海报代理（仪表盘媒体库卡片/最新入库海报墙）
		r.GET("/poster/*path", func(c *gin.Context) { serveTMDBPoster(c, h.Config.DataDir) })
		// Emby 图片代理（仪表盘：服务端注入 api_key，避免密钥出现在前端 URL）
		r.GET("/embyimg", func(c *gin.Context) { h.EmbyImageProxy(c) })
		// TMDB 海报代理（影视转存选片弹窗；<img> 标签带不了登录态，须挂公开路由）
		r.GET("/tmdb/img", h.TmdbImg)
		// 封面预览（img 加载，无鉴权；仅读本地生成的封面文件）
		r.GET("/covergen/preview", h.CoverGenPreview)
		// QQ OneBot 事件回调（NapCat 等推送事件；token 鉴权，私聊管理 QQ 触发指令）
		r.POST("/onebot/event", h.OneBotEvent)

		// 刮削整理
		protected.GET("/scrape/rules", h.ListScrapeRules)
		protected.POST("/scrape/rules", h.SaveScrapeRules)
		protected.GET("/scrape/categories", h.ListCategories)
		protected.POST("/scrape/categories", h.SaveCategories)

		// 媒体信息补全（探测队列）
		protected.GET("/enrich/list", h.EnrichList)

		// 插件：一键创建 Emby 媒体库
		protected.GET("/plugin/emby-libraries", h.EmbyLibrariesPreview)
		protected.POST("/plugin/emby-libraries", h.EmbyLibrariesCreate)

		// 版本号与日志级别
		protected.GET("/version", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"version": buildVersion})
		})
		protected.POST("/system/log-level", func(c *gin.Context) {
			var req struct {
				Level string `json:"level"` // simple / verbose
			}
			if err := c.ShouldBindJSON(&req); err != nil || (req.Level != "simple" && req.Level != "verbose") {
				c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
				return
			}
			if err := cfg.SaveSetting("log-level", req.Level); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			// 立即刷新缓存
			logVerboseMu.Lock()
			logVerboseAt = time.Time{}
			logVerboseMu.Unlock()
			c.JSON(http.StatusOK, gin.H{"message": "已保存"})
		})

		protected.GET("/scrape/wash", h.ListWashRules)
		protected.POST("/scrape/wash", h.SaveWashRules)

		// 整理流水线（识别 → 搬移 → STRM → 刮削 → 刷 Emby 一条龙）
		protected.POST("/organize/pipeline", h.RunOrganizePipeline)

		// 整理记录：历史留痕 + 指定 TMDB 条目重新整理
		protected.GET("/organize/records", h.ListOrganizeRecords)
		protected.GET("/organize/records/:id", h.GetOrganizeRecord)
		protected.POST("/organize/records/:id/redo", h.RedoOrganizeRecord)
		protected.DELETE("/organize/records/:id", h.DeleteOrganizeRecord)
		// 深度删除：删这条记录整理出来的网盘源文件（进回收站），与上面「只删记录」两回事
		protected.POST("/organize/records/:id/deep-delete", h.DeepDeleteOrganizeRecord)
		protected.POST("/organize/records/clear", h.ClearOrganizeRecords)

		// TMDB 搜索（影视转存页与整理记录的「重新整理」共用：名称或 TMDB ID → 条目选择）
		protected.GET("/tmdb/search", h.TmdbSearchMulti)

		// 影视转存 · 观影（账号密码登录 + PoW 反爬自动过验证；磁力提交 115 离线）
		protected.GET("/guanying/config", h.GyGetConfig)
		protected.GET("/guanying/check", h.GyCheck)

		protected.POST("/guanying/config", h.GySaveConfig)
		protected.POST("/guanying/login", h.GyLogin)
		protected.POST("/guanying/logout", h.GyLogout)
		protected.GET("/guanying/search", h.GySearch)
		protected.GET("/guanying/resources", h.GyResources)
		protected.POST("/guanying/offline", h.GyOffline)

		// 影视转存 · RE0（官方 OpenAPI：OAuth 用户授权 + 资源查询/解锁/转存）
		protected.GET("/re0/config", h.Re0GetConfig)
		protected.POST("/re0/config", h.Re0SaveConfig)
		protected.GET("/re0/check", h.Re0Check)
		protected.GET("/re0/oauth/start", h.Re0OAuthStart)
		protected.GET("/re0/search", h.Re0Search)
		protected.POST("/re0/unlock", h.Re0Unlock)

		// 分享链接转存（转存到接收文件夹后由整理+增量接管）
		protected.POST("/share/receive", h.ShareReceive)

		// 115 每日签到（定时窗口随机执行 + 手动签到）
		protected.GET("/115checkin/config", h.Checkin115GetConfig)
		protected.POST("/115checkin/config", h.Checkin115SaveConfig)
		protected.POST("/115checkin/run", h.Checkin115Run)

		// 媒体库封面生成（分类聚合 TMDB 海报 → 合成封面 → 推送 Emby）
		protected.GET("/covergen/config", h.CoverGenGetConfig)
		protected.POST("/covergen/config", h.CoverGenSaveConfig)
		protected.POST("/covergen/run", h.CoverGenRun)
		protected.GET("/covergen/list", h.CoverGenList)

		// TG 关键词订阅（频道轮询 → 水位去重 → 命中通知/自动转存）
		protected.GET("/tgsub/config", h.TgSubGetConfig)
		protected.POST("/tgsub/config", h.TgSubSaveConfig)
		protected.POST("/tgsub/run", h.TgSubRun)

		// 影视刮削（原生 NFO + 海报到本地媒体库）
		protected.GET("/scrape/config", h.ScrapeGetConfig)
		protected.POST("/scrape/config", h.ScrapeSaveConfig)
		protected.POST("/scrape/run", h.ScrapeRun)
		protected.GET("/scrape/status", h.ScrapeStatus)
		protected.POST("/scrape/stop", h.ScrapeStop)

		// 影视转存 · 木咖（不太灵系影视库，搜索匿名/资源需 VIP token）
		protected.GET("/mukaku/config", h.MukakuGetConfig)
		protected.POST("/mukaku/config", h.MukakuSaveConfig)
		protected.GET("/mukaku/captcha", h.MukakuCaptcha)
		protected.POST("/mukaku/login", h.MukakuLogin)
		protected.GET("/mukaku/search", h.MukakuSearch)
		protected.GET("/mukaku/resources", h.MukakuResources)

		// 影视转存 · PanSou 网盘聚合搜索（开源项目公开实例，免认证）
		protected.GET("/pansou/config", h.PansouGetConfig)
		protected.POST("/pansou/config", h.PansouSaveConfig)
		protected.GET("/pansou/search", h.PansouSearch)

		// 离线下载（磁力/ed2k/HTTP）
		protected.POST("/offline/add", h.offlineAddTask)

		// 下载记录（离线/分享转存提交过的链接 + 整理出来的识别结果）
		protected.GET("/download/links", h.ListDownloadLinks)
		protected.DELETE("/download/links/:id", h.DeleteDownloadLink)
		protected.POST("/download/links/clear", h.ClearDownloadLinks)

		// 302 代理

		// 系统设置
		protected.GET("/system/logs", h.GetSystemLogs)
		// 实时日志长轮询：带上次修改时间 since，日志有新内容立即返回，
		// 25 秒无变化返回 changed=false（前端随即再次挂起，实现"有日志就出现"）
		protected.GET("/system/logs/wait", func(c *gin.Context) {
			logPath := "/logs/app.log"
			var since int64
			fmt.Sscanf(c.Query("since"), "%d", &since)
			deadline := time.Now().Add(25 * time.Second)
			for {
				st, err := os.Stat(logPath)
				if err == nil && st.ModTime().Unix() > since {
					c.JSON(http.StatusOK, gin.H{"changed": true, "mtime": st.ModTime().Unix()})
					return
				}
				if time.Now().After(deadline) {
					mt := int64(since)
					if err == nil {
						mt = st.ModTime().Unix()
					}
					c.JSON(http.StatusOK, gin.H{"changed": false, "mtime": mt})
					return
				}
				time.Sleep(400 * time.Millisecond)
			}
		})
		// 清空任务日志：截断 app.log（追加模式写入器继续写同一文件）
		protected.POST("/system/logs/clear", func(c *gin.Context) {
			logPath := "/logs/app.log"
			if err := os.Truncate(logPath, 0); err != nil {
				if os.IsNotExist(err) {
					c.JSON(http.StatusOK, gin.H{"message": "日志本来就为空"})
					return
				}
				c.JSON(http.StatusInternalServerError, gin.H{"error": "清空失败: " + err.Error()})
				return
			}
			log.Printf("[系统] 任务日志已清空（界面操作）")
			c.JSON(http.StatusOK, gin.H{"message": "日志已清空"})
		})
		protected.GET("/system/backup", h.SystemBackup)
		protected.GET("/system/guide", h.SystemGuide)
		protected.GET("/storage/115/diagnose", h.Diagnose115)
	}
}

type Handler struct {
	DB     *gorm.DB
	Config *config.Config
}

// ==================== 认证 ====================

func (h *Handler) AuthStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"initialized": h.Config.IsAuthExists(),
	})
}

// UpdateAccount POST /auth/update-account —— 已废弃：账号由环境变量
// AUTH_USER/AUTH_PASSWORD 管理，网页修改入口随注册功能一并移除
func (h *Handler) UpdateAccount(c *gin.Context) {
	c.JSON(http.StatusForbidden, gin.H{"error": "账号由环境变量 AUTH_USER / AUTH_PASSWORD 管理，请修改容器环境变量后重启"})
}

func (h *Handler) Login(c *gin.Context) {
	var req struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	// 防爆破：同 IP 连续 5 次失败锁定 10 分钟
	// （ClientIP 的可信度由 main.go SetTrustedProxies(nil) 保证：XFF 不可伪造）
	ip := c.ClientIP()
	if remain := loginGuardCheck(ip); remain > 0 {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": fmt.Sprintf("失败次数过多，已锁定，请 %s 后再试", remain.Truncate(time.Second))})
		return
	}
	if !h.Config.VerifyAuth(req.Username, req.Password) {
		loginGuardFail(ip)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
		return
	}
	loginGuardPass(ip)

	token := h.generateToken(1, req.Username)
	c.JSON(http.StatusOK, gin.H{
		"token":    token,
		"username": req.Username,
		"message":  "登录成功",
	})
}

func (h *Handler) generateToken(userID uint, username string) string {
	claims := jwt.MapClaims{
		"user_id":  userID,
		"username": username,
		// 自托管工具常用场景：30 天有效期（此前 72 小时，三天没操作就会被
		// 全站 401 轰炸着赶去重新登录，体验差且无安全收益——密码仍可随时改）
		"exp": time.Now().Add(30 * 24 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	t, _ := token.SignedString([]byte(h.Config.JWTSecret))
	return t
}

func (h *Handler) AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.GetHeader("Authorization")
		if token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
			c.Abort()
			return
		}
		// 去掉 Bearer 前缀
		if len(token) > 7 && token[:7] == "Bearer " {
			token = token[7:]
		}
		// 验证 JWT
		claims := jwt.MapClaims{}
		_, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (interface{}, error) {
			return []byte(h.Config.JWTSecret), nil
		})
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "登录已过期"})
			c.Abort()
			return
		}
		c.Set("user_id", claims["user_id"])
		c.Set("username", claims["username"])
		c.Next()
	}
}

// ==================== 仪表盘 ====================
// （旧版 Dashboard 接口已删除：返回写死的假系统信息（CPU/内存/运行时间），
//   实际路由使用 DashboardEnhanced，见各 handler 文件）

// ==================== 存储管理（占位） ====================

func (h *Handler) ListStorage(c *gin.Context) {
	var storages []model.Storage
	h.DB.Find(&storages)
	c.JSON(http.StatusOK, gin.H{"data": storages})
}

func (h *Handler) CreateStorage(c *gin.Context) {
	var storage model.Storage
	if err := c.ShouldBindJSON(&storage); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	// 启用 OpenAPI 必须带 AppID：缺了 open115FromDB 会当作未启用静默回落 Cookie，
	// 用户以为自己在走开放平台，实际不是——这种「配了但没生效」最难自查，直接拒
	if storage.OpenapiEnabled && strings.TrimSpace(storage.AppID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "启用 OPENAPI 必须填写开放平台 AppID；没有 AppID 请选择「禁用」，Cookie 通道功能完整"})
		return
	}
	// upsert：按 type 去重，存在则更新（保留已有 Cookie 和账号名）
	var existing model.Storage
	if err := h.DB.Where("type = ?", storage.Type).First(&existing).Error; err == nil {
		updates := map[string]interface{}{
			"cookie_path":     storage.CookiePath,
			"device":          storage.Device,
			"interval":        storage.Interval,
			"openapi_enabled": storage.OpenapiEnabled,
			"app_id":          storage.AppID,
			"app_key":         storage.AppKey,
		}
		if storage.Name != "" && storage.Name != "115主号" {
			updates["name"] = storage.Name
		}
		if storage.Cookie != "" {
			updates["cookie"] = storage.Cookie
		}
		h.DB.Model(&existing).Updates(updates)
		// 间隔设置变更立即生效
		if storage.Type == "115" {
			Apply115Interval(h.DB)
		}
		c.JSON(http.StatusOK, gin.H{"data": existing, "message": "保存成功"})
		return
	}
	// 不存在则创建
	if storage.Name == "" {
		storage.Name = "115主号"
	}
	h.DB.Create(&storage)
	if storage.Type == "115" {
		Apply115Interval(h.DB)
	}
	c.JSON(http.StatusOK, gin.H{"data": storage, "message": "创建成功"})
}

func (h *Handler) DeleteStorage(c *gin.Context) {
	id := c.Param("id")
	h.DB.Delete(&model.Storage{}, id)
	c.JSON(http.StatusOK, gin.H{"message": "删除成功"})
}

// CheckStorage 检测 115 账号可用性：OpenAPI 优先，Cookie 回退
func (h *Handler) CheckStorage(c *gin.Context) {
	var req struct {
		Type       string `json:"type"`
		CookiePath string `json:"cookie_path"`
		Cookie     string `json:"cookie"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	// ===== OpenAPI 通道 =====
	if oc := h.getOpen115(); oc != nil && oc.authorized() {
		if err := oc.ping(); err != nil {
			log.Printf("[系统] OpenAPI 校验失败: %v", err)
			c.JSON(http.StatusUnauthorized, gin.H{
				"valid":   false,
				"message": "OpenAPI 校验失败：" + err.Error(),
			})
			return
		}
		var storage model.Storage
		name := "OpenAPI"
		if err := h.DB.Where("type = ?", "115").First(&storage).Error; err == nil && storage.Name != "" {
			name = storage.Name
		}
		c.JSON(http.StatusOK, gin.H{
			"username": name,
			"capacity": "OpenAPI 通道（官方授权）",
			"valid":    true,
			"message":  "OpenAPI 授权有效",
			"channel":  "OpenAPI",
		})
		return
	}

	// ===== Cookie 通道 =====
	// 支持手动导入：请求带 cookie 字段时优先使用，检测通过后自动保存
	cookie := strings.TrimSpace(req.Cookie)
	if cookie == "" {
		cookie, _ = h.get115Cookie()
	}
	if cookie == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "尚未绑定 115 账号"})
		return
	}
	// 基本格式校验，避免保存明显无效的内容
	if !strings.Contains(cookie, "UID=") || !strings.Contains(cookie, "SEID=") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cookie 格式不正确，需要包含 UID=...;CID=...;SEID=... 字段"})
		return
	}

	log.Printf("[系统] 检测 Cookie（长度=%d）", len(cookie))

	// 用 web 端用户信息接口校验 Cookie（my.115.com nav，115driver ApiUserInfo 同款）
	// 注意：不能用 proapi.115.com/android/* 的 App 专用接口，那些接口要求对应 App 的 UA
	const navAPI = "https://my.115.com/?ct=ajax&ac=nav"
	body, err := httpGet115UA(navAPI, nil, cookie, ua115Unified(), 15*time.Second)
	if err != nil {
		log.Printf("[系统] 调用失败: %v", err)
		c.JSON(http.StatusOK, gin.H{
			"valid":   false,
			"message": "调用 115 接口失败：" + err.Error(),
		})
		return
	}

	var resp struct {
		State bool            `json:"state"`
		Error string          `json:"error"`
		Data  json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		c.JSON(http.StatusOK, gin.H{
			"valid":   false,
			"message": "解析 115 响应失败",
		})
		return
	}
	// state=false 时 data 可能是 []，仅在 state=true 时解析用户信息
	var userName string
	accInfo := gin.H{}
	if resp.State && len(resp.Data) > 0 {
		var d struct {
			UserName    string `json:"user_name"`
			UserID      int64  `json:"user_id"`
			Face        string `json:"face"`   // 头像 URL
			Vip         int    `json:"vip"`    // 0=非会员 1/2=会员
			Expire      int64  `json:"expire"` // 会员到期时间戳（秒）
			Forever     int    `json:"forever"`
			IsPrivilege bool   `json:"is_privilege"`
		}
		_ = json.Unmarshal(resp.Data, &d)
		userName = d.UserName
		accInfo = gin.H{
			"avatar":       d.Face,
			"user_id":      d.UserID,
			"vip":          d.Vip,
			"vip_expire":   d.Expire,
			"vip_forever":  d.Forever,
			"is_privilege": d.IsPrivilege,
		}
	}
	if !resp.State || userName == "" {
		msg := resp.Error
		if msg == "" {
			msg = "Cookie 无效或已过期"
		}
		c.JSON(http.StatusOK, gin.H{
			"valid":   false,
			"message": msg + "，请重新扫码登录",
		})
		return
	}

	// 容量信息（webapi files/index_info，115driver GetInfo 同款）
	capacity := "-"
	var usedSize, totalSize int64
	const infoAPI = "https://webapi.115.com/files/index_info"
	if infoBody, err := httpGet115UA(infoAPI, nil, cookie, ua115Unified(), 15*time.Second); err == nil {
		var info struct {
			State bool `json:"state"`
			Data  struct {
				SpaceInfo struct {
					AllTotal struct {
						Size int64  `json:"size"`
						Fmt  string `json:"size_format"`
					} `json:"all_total"`
					AllUse struct {
						Size int64  `json:"size"`
						Fmt  string `json:"size_format"`
					} `json:"all_use"`
				} `json:"space_info"`
			} `json:"data"`
		}
		if json.Unmarshal(infoBody, &info) == nil && info.State && info.Data.SpaceInfo.AllTotal.Size > 0 {
			usedSize = info.Data.SpaceInfo.AllUse.Size
			totalSize = info.Data.SpaceInfo.AllTotal.Size
			capacity = fmt.Sprintf("%s / %s", formatBytes(usedSize), formatBytes(totalSize))
		}
		// 登录设备列表（排查风控/异常登录用）
		var info2 struct {
			State bool `json:"state"`
			Data  struct {
				LoginDevicesInfo struct {
					List []struct {
						Name      string `json:"name"`
						Device    string `json:"device"`
						IP        string `json:"ip"`
						City      string `json:"city"`
						Utime     int64  `json:"utime"`
						IsCurrent int    `json:"is_current"`
					} `json:"list"`
				} `json:"login_devices_info"`
			} `json:"data"`
		}
		if json.Unmarshal(infoBody, &info2) == nil && info2.State && len(info2.Data.LoginDevicesInfo.List) > 0 {
			devices := make([]gin.H, 0, len(info2.Data.LoginDevicesInfo.List))
			for _, d := range info2.Data.LoginDevicesInfo.List {
				devices = append(devices, gin.H{
					"name": d.Name, "device": d.Device, "ip": d.IP,
					"city": d.City, "utime": d.Utime, "is_current": d.IsCurrent == 1,
				})
			}
			accInfo["devices"] = devices
		}
	}
	accInfo["used_size"] = usedSize
	accInfo["total_size"] = totalSize

	resp2 := gin.H{
		"username": userName,
		"capacity": capacity,
		"valid":    true,
		"message":  "Cookie 有效",
		"channel":  "Cookie",
	}
	for k, v := range accInfo {
		resp2[k] = v
	}
	c.JSON(http.StatusOK, resp2)

	// 手动导入的 Cookie 检测通过后自动保存（绕过被风控的扫码登录接口）
	if strings.TrimSpace(req.Cookie) != "" {
		h.Config.SaveCookie(cookie)
		h.Config.Save115Device("web")
		h.upsert115Storage(cookie, "web", userName)
		log.Printf("[系统] 已导入并保存手动提供的 Cookie，账号=%s", userName)
	}
}

// pan115Capacity 115 容量信息（仪表盘用，5 分钟内存缓存）
type pan115Cap struct {
	Username  string `json:"username"`
	Used      int64  `json:"used"`
	Total     int64  `json:"total"`
	FetchedAt int64  `json:"-"`
}

var (
	pan115CapMu    sync.Mutex
	pan115CapCache *pan115Cap
)

// pan115CapacityCached 带缓存读取 115 容量（Cookie 有效才查询）
func (h *Handler) pan115CapacityCached() gin.H {
	pan115CapMu.Lock()
	cached := pan115CapCache
	pan115CapMu.Unlock()
	if cached != nil && time.Since(time.Unix(cached.FetchedAt, 0)) < 5*time.Minute {
		return gin.H{"username": cached.Username, "used": cached.Used, "total": cached.Total,
			"used_h": formatBytes(cached.Used), "total_h": formatBytes(cached.Total)}
	}
	cookie, err := h.get115Cookie()
	if err != nil {
		return gin.H{"enabled": false}
	}
	const navAPI = "https://my.115.com/?ct=ajax&ac=nav"
	body, err := httpGet115UA(navAPI, nil, cookie, ua115Unified(), 10*time.Second)
	if err != nil {
		return gin.H{"enabled": false}
	}
	var resp struct {
		State bool            `json:"state"`
		Data  json.RawMessage `json:"data"`
	}
	cap2 := &pan115Cap{}
	if json.Unmarshal(body, &resp) == nil && resp.State {
		var d struct {
			UserName string `json:"user_name"`
		}
		_ = json.Unmarshal(resp.Data, &d)
		cap2.Username = d.UserName
	}
	if infoBody, err := httpGet115UA("https://webapi.115.com/files/index_info", nil, cookie, ua115Unified(), 10*time.Second); err == nil {
		var info struct {
			State bool `json:"state"`
			Data  struct {
				SpaceInfo struct {
					AllTotal struct{ Size int64 } `json:"all_total"`
					AllUse   struct{ Size int64 } `json:"all_use"`
				} `json:"space_info"`
			} `json:"data"`
		}
		if json.Unmarshal(infoBody, &info) == nil && info.State {
			cap2.Used, cap2.Total = info.Data.SpaceInfo.AllUse.Size, info.Data.SpaceInfo.AllTotal.Size
		}
	}
	cap2.FetchedAt = time.Now().Unix()
	pan115CapMu.Lock()
	pan115CapCache = cap2
	pan115CapMu.Unlock()
	return gin.H{"username": cap2.Username, "used": cap2.Used, "total": cap2.Total,
		"used_h": formatBytes(cap2.Used), "total_h": formatBytes(cap2.Total)}
}

// formatBytes 将字节数格式化为人类可读容量（如 1.50 GiB）
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// ==================== 核心配置 TMDB ====================

func (h *Handler) GetTmdbConfig(c *gin.Context) {
	var cfg model.TmdbConfig
	result := h.DB.First(&cfg)
	if result.Error != nil {
		// 返回默认值
		c.JSON(http.StatusOK, gin.H{
			"api_key":        "",
			"api_url":        "https://api.themoviedb.org",
			"image_api_url":  "https://image.tmdb.org",
			"language":       "zh-CN",
			"image_language": "zh-CN",
			"enable_proxy":   false,
			"proxy_url":      "",
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": cfg})
}

func (h *Handler) SaveTmdbConfig(c *gin.Context) {
	var cfg model.TmdbConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	cfg.ApiKey = strings.TrimSpace(cfg.ApiKey)
	if cfg.ApiKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写 TMDB API 密钥，自动整理需要它来识别影视"})
		return
	}
	// 配置是单例，前端不传 ID；否则每次保存都会新增一行，识别仍读旧密钥。
	var current model.TmdbConfig
	if err := h.DB.First(&current).Error; err != nil && err != gorm.ErrRecordNotFound {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取 TMDB 配置失败"})
		return
	}
	cfg.ID = current.ID
	if err := h.DB.Save(&cfg).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存 TMDB 配置失败，请重试"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": cfg, "message": "保存成功"})
}

// ==================== 归档同步（占位） ====================

func (h *Handler) ListSyncTasks(c *gin.Context) {
	var tasks []model.SyncTask
	h.DB.Find(&tasks)
	c.JSON(http.StatusOK, gin.H{"data": tasks})
}

func (h *Handler) CreateSyncTask(c *gin.Context) {
	var task model.SyncTask
	if err := c.ShouldBindJSON(&task); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	h.DB.Create(&task)
	c.JSON(http.StatusOK, gin.H{"data": task, "message": "创建成功"})
}

// RunSyncTask/GetSyncLogs：任务编排功能未实现，返回明确错误而非假成功
func (h *Handler) RunSyncTask(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{"error": "任务编排功能规划中，请使用全量/增量同步按钮"})
}

func (h *Handler) GetSyncLogs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"data": []gin.H{}})
}

// SystemBackup 导出配置+数据库备份（zip 下载）。
// 内容：setting.yaml（全部配置）、115-station.db（VACUUM INTO 一致性快照）、
// auth.yaml（管理员账号，含密码哈希）。换机迁移 = 下载后放到新机器对应目录
// GET /system/backup
func (h *Handler) SystemBackup(c *gin.Context) {
	tmp, err := os.CreateTemp("", "115-station-backup-*.zip")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建临时文件失败"})
		return
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	zw := zip.NewWriter(tmp)

	addFile := func(src, name string) {
		data, err := os.ReadFile(src)
		if err != nil {
			return // 不存在的文件跳过（如尚未注册过账号）
		}
		w, err := zw.Create(name)
		if err != nil {
			return
		}
		_, _ = w.Write(data)
	}
	addFile(filepath.Join(h.Config.ConfigDir, "setting.yaml"), "config/setting.yaml")
	addFile(filepath.Join(h.Config.ConfigDir, "auth.yaml"), "config/auth.yaml")
	addFile(filepath.Join(h.Config.ConfigDir, "jwt.key"), "config/jwt.key")
	addFile(filepath.Join(h.Config.ConfigDir, "115-cookie.txt"), "config/115-cookie.txt")

	// sqlite 一致性快照（VACUUM INTO 不锁库）
	dbSnap := filepath.Join(os.TempDir(), fmt.Sprintf("115-station-db-%d.db", time.Now().UnixNano()))
	defer os.Remove(dbSnap)
	if err := h.DB.Exec("VACUUM INTO ?", dbSnap).Error; err == nil {
		addFile(dbSnap, "data/115-station.db")
	} else {
		log.Printf("[备份] ○ 数据库快照失败（只导出配置）: %v", err)
	}
	_ = zw.Close()

	info, _ := tmp.Stat()
	c.Header("Content-Disposition", "attachment; filename=115-station-backup-"+time.Now().Format("20060102-150405")+".zip")
	c.Header("Content-Type", "application/zip")
	c.Data(http.StatusOK, "application/zip", func() []byte {
		data, _ := os.ReadFile(tmp.Name())
		return data
	}())
	_ = info
}

// SystemGuide 首启引导检查：逐项返回配置完成度（前端渲染"下一步"清单）
// GET /system/guide
func (h *Handler) SystemGuide(c *gin.Context) {
	guide := gin.H{}
	// 1. 115 账号
	var storage model.Storage
	guide["pan115"] = h.DB.Where("type = ?", "115").First(&storage).Error == nil && (storage.Cookie != "" || storage.AppID != "")
	// 2. TMDB
	var tmdbCfg model.TmdbConfig
	guide["tmdb"] = h.DB.First(&tmdbCfg).Error == nil && tmdbCfg.ApiKey != ""
	// 3. 整理目录
	type orgBasic struct {
		Pending string `json:"pending"`
	}
	var ob orgBasic
	_ = json.Unmarshal([]byte(h.getSettingValue("org-basic")), &ob)
	guide["orgDirs"] = ob.Pending != ""
	// 4. 首次同步
	var synced int64
	h.DB.Model(&model.SyncedFile{}).Count(&synced)
	guide["synced"] = synced > 0
	// 5. Emby
	type embyCfg struct {
		ServerURL string `json:"server_url"`
	}
	var ec embyCfg
	_ = json.Unmarshal([]byte(settingValueCompat("emby")), &ec)
	guide["emby"] = ec.ServerURL != ""
	// 6. 通知
	guide["notify"] = false
	if raw := settingValueCompat("message"); raw != "" {
		var mc MessageConfig
		if json.Unmarshal([]byte(raw), &mc) == nil {
			guide["notify"] = mc.Wecom.isEnabled() || mc.TG.isEnabled()
		}
	}
	done := 0
	for _, k := range []string{"pan115", "tmdb", "orgDirs", "synced"} {
		if v, _ := guide[k].(bool); v {
			done++
		}
	}
	guide["coreDone"] = done == 4 // 核心四步完成即隐藏引导卡
	c.JSON(http.StatusOK, guide)
}

// ==================== 刮削整理（占位） ====================

func (h *Handler) ListScrapeRules(c *gin.Context) {
	var rules []model.ScrapeRule
	h.DB.Find(&rules)
	c.JSON(http.StatusOK, gin.H{"data": rules})
}

func (h *Handler) SaveScrapeRules(c *gin.Context) {
	var rules []model.ScrapeRule
	if err := c.ShouldBindJSON(&rules); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	for _, r := range rules {
		if r.ID > 0 {
			h.DB.Save(&r)
		} else {
			h.DB.Create(&r)
		}
	}
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// TestTMDBConnection 测试 TMDB 连接
// POST /config/test-tmdb  body: {"api_url":"...","api_key":"...","language":"..."}
func (h *Handler) TestTMDBConnection(c *gin.Context) {
	var req struct {
		APIURL   string `json:"api_url"`
		APIKey   string `json:"api_key"`
		Language string `json:"language"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.APIKey) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写 TMDB API 密钥"})
		return
	}
	req.APIURL = normalizeTMDBBase(req.APIURL)
	req.APIKey = strings.TrimSpace(req.APIKey)
	if req.Language == "" {
		req.Language = "zh-CN"
	}
	// 用 /configuration 接口测试（/3 前缀已在规范化时补齐）
	endpoint := req.APIURL + "/configuration?api_key=" + url.QueryEscape(req.APIKey)
	// 与真实识别链路一致：走全局代理（否则代理配对、测试却直连失败，误导排障）
	client := &http.Client{Timeout: 10 * time.Second}
	if pu := getProxyURL(); pu != "" {
		if p, err := parseProxyURL(pu); err == nil {
			client.Transport = &http.Transport{Proxy: p}
		}
	}
	start := time.Now()
	resp, err := client.Get(endpoint)
	if err != nil {
		// 网络错误可能包含带密钥的 URL，不向页面回传原始错误。
		c.JSON(http.StatusOK, gin.H{"ok": false, "success": false, "error": "无法连接 TMDB，请检查 API 地址、网络或系统代理配置"})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		message := fmt.Sprintf("TMDB 服务返回 HTTP %d，请稍后重试", resp.StatusCode)
		if resp.StatusCode == http.StatusUnauthorized {
			message = "TMDB 密钥验证失败，请检查是否复制了 API Key（而非 API Read Access Token）"
		} else if resp.StatusCode == http.StatusForbidden {
			message = "TMDB 拒绝访问，请检查密钥权限或代理设置"
		}
		c.JSON(http.StatusOK, gin.H{"ok": false, "success": false, "error": message})
		return
	}
	var configuration struct {
		Images json.RawMessage `json:"images"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&configuration); err != nil || len(configuration.Images) == 0 || string(configuration.Images) == "null" {
		c.JSON(http.StatusOK, gin.H{"ok": false, "success": false, "error": "响应不是有效的 TMDB 配置，请检查 API 地址或反代设置"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "success": true, "message": "TMDB 连接成功", "latency_ms": time.Since(start).Milliseconds()})
}

func (h *Handler) ListCategories(c *gin.Context) {
	// 从 ScrapeRule 表读取已保存的 YAML
	var rule model.ScrapeRule
	h.DB.Where("type = ?", "category_config").First(&rule)
	c.JSON(http.StatusOK, gin.H{"config": rule.Config})
}

// SaveCategories 保存二级分类（支持 YAML 字符串，对齐 CMS）
func (h *Handler) SaveCategories(c *gin.Context) {
	var req struct {
		Yaml string `json:"yaml"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	// 解析 YAML → CategoryRule 规则表（classifyMedia 真正读取的存储；
	// 此前只存 YAML 文本、规则表永远是首次种子的默认值，界面编辑从未生效）
	rows, perr := parseCategoryYAML(req.Yaml)
	if perr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "YAML 解析失败: " + perr.Error()})
		return
	}
	if len(rows) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未解析到 movie/tv 分类规则"})
		return
	}
	// 存储 YAML 到 ScrapeRule 表（界面回显源）
	var rule model.ScrapeRule
	h.DB.Where("type = ?", "category_config").First(&rule)
	rule.Type = "category_config"
	rule.Enabled = true
	rule.Config = req.Yaml
	if rule.ID > 0 {
		h.DB.Save(&rule)
	} else {
		h.DB.Create(&rule)
	}
	// 事务重建 movie/tv 规则
	if err := replaceCategoryRules(h.DB, rows); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "重建规则失败: " + err.Error()})
		return
	}
	movieN, tvN := 0, 0
	for _, r := range rows {
		if r.MediaType == "movie" {
			movieN++
		} else {
			tvN++
		}
	}
	log.Printf("[配置] 二级分类已生效：%d 条规则（电影 %d / 剧集 %d）", len(rows), movieN, tvN)
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("保存成功（%d 条分类规则已生效）", len(rows))})
}

// replaceCategoryRules 事务重建 movie/tv 规则表（整表替换，不做增量合并）
func replaceCategoryRules(db *gorm.DB, rows []model.CategoryRule) error {
	tx := db.Begin()
	if err := tx.Where("media_type IN ?", []string{"movie", "tv"}).Delete(&model.CategoryRule{}).Error; err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Create(&rows).Error; err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit().Error
}

// SyncCategoryRulesFromYAML 启动时按库里存的 YAML 重建规则表。
//
// YAML 是唯一事实来源，规则表只是它的解析结果：两者解析规则变化后必须重新落一遍，
// 否则用户界面上看到的 YAML 和整理实际用的分类对不上（且只有再点一次保存才会一致）。
// 没存过 YAML（从没进过分类设置）时什么都不做，保留首次部署播种的默认规则。
func SyncCategoryRulesFromYAML(db *gorm.DB) error {
	var rule model.ScrapeRule
	if err := db.Where("type = ?", "category_config").First(&rule).Error; err != nil {
		return nil // 没存过，用种子默认规则
	}
	if strings.TrimSpace(rule.Config) == "" {
		return nil
	}
	rows, err := parseCategoryYAML(rule.Config)
	if err != nil || len(rows) == 0 {
		return err
	}
	return replaceCategoryRules(db, rows)
}

// parseCategoryYAML 解析二级分类 YAML 为有序规则行（movie/tv；无条件条目作为兜底）
func parseCategoryYAML(src string) ([]model.CategoryRule, error) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(src), &root); err != nil {
		return nil, err
	}
	var rows []model.CategoryRule
	if len(root.Content) == 0 {
		return rows, nil
	}
	mp := root.Content[0]
	for i := 0; i+1 < len(mp.Content); i += 2 {
		mediaKey := mp.Content[i].Value
		if mediaKey != "movie" && mediaKey != "tv" {
			continue
		}
		val := mp.Content[i+1]
		if val.Kind != yaml.MappingNode {
			continue
		}
		prio := 0
		for j := 0; j+1 < len(val.Content); j += 2 {
			raw := val.Content[j].Value
			if strings.TrimSpace(raw) == "" {
				continue // 空键，没有分类名可言
			}
			// 分类名**就是**库内目录名，写什么就是什么（多级用 / 分隔）。
			// 这里不能再去剥「电影/」「电视剧/」前缀：剥掉之后调用方补一层
			// mediaTypeCategory，写成平铺结构（tv 下并列 动漫番剧/综艺/剧集）
			// 的用户会被整理成 剧集/动漫番剧
			name := libSubPath(raw)
			r := model.CategoryRule{MediaType: mediaKey, Name: name}
			fields := val.Content[j+1]
			if fields != nil && fields.Kind == yaml.MappingNode {
				for k := 0; k+1 < len(fields.Content); k += 2 {
					fk, fv := fields.Content[k].Value, fields.Content[k+1].Value
					switch fk {
					case "genre_ids":
						r.GenreIds = fv
					case "original_language":
						r.OriginalLanguage = fv
					case "origin_country":
						r.OriginCountry = fv
					case "custom_regex":
						r.CustomRegex = fv
					case "ext":
						r.Ext = fv
					}
				}
			}
			prio++
			r.Priority = prio
			if r.GenreIds == "" && r.OriginalLanguage == "" && r.OriginCountry == "" && r.CustomRegex == "" && r.Ext == "" {
				r.IsDefault = true
			}
			rows = append(rows, r)
		}
	}
	return rows, nil
}

// ==================== 洗版策略 ====================

func (h *Handler) ListWashRules(c *gin.Context) {
	// 从 ScrapeRule 表读取已保存的 YAML
	var rule model.ScrapeRule
	h.DB.Where("type = ?", "wash_config").First(&rule)
	c.JSON(http.StatusOK, gin.H{"config": rule.Config, "default_config": model.DefaultWashYAML})
}

// SaveWashRules 保存洗版策略（支持 YAML 字符串，对齐 CMS）
func (h *Handler) SaveWashRules(c *gin.Context) {
	var req struct {
		Yaml string `json:"yaml"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	var strategies map[string]washStrategy
	if err := yaml.Unmarshal([]byte(req.Yaml), &strategies); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "洗版策略格式错误: " + err.Error()})
		return
	}
	var rule model.ScrapeRule
	h.DB.Where("type = ?", "wash_config").First(&rule)
	rule.Type = "wash_config"
	rule.Enabled = true
	rule.Config = req.Yaml
	if err := h.DB.Save(&rule).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存洗版策略失败"})
		return
	}
	resetWashCache() // 不失效的话点完保存最多一分钟内还在按旧策略判
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// ==================== 302 代理（占位） ====================

// GetSystemLogs 读取系统日志文件最后 500 行
// GET /system/logs（日志页唯一数据源：整理/同步/转存等任务动作的实时输出）
func (h *Handler) GetSystemLogs(c *gin.Context) {
	logPath := "/logs/app.log"
	data, err := os.ReadFile(logPath)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"logs": "暂无日志文件（日志文件在 /logs/app.log）"})
		return
	}
	lines := strings.Split(string(data), "\n")
	// 只返回最后 500 行（一轮整理/同步动辄上百行，200 行看不全一个完整动作）
	start := 0
	if len(lines) > 500 {
		start = len(lines) - 500
	}
	c.JSON(http.StatusOK, gin.H{"logs": strings.Join(lines[start:], "\n")})
}

// Diagnose115 诊断 115 连接问题：会话有效性 / 域名风控 / UA 配对全矩阵探测
// GET /storage/115/diagnose
func (h *Handler) Diagnose115(c *gin.Context) {
	cookie, err := h.get115Cookie()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	results := gin.H{}
	probe := func(name, api string, query url.Values, ua string) (ok bool, info string) {
		body, err := httpGet115Full(api, query, cookie, ua, 15*time.Second, nil)
		if err != nil {
			return false, err.Error()
		}
		var r struct {
			State bool   `json:"state"`
			Error string `json:"error"`
			Count int    `json:"count"`
			Data  []struct {
				N string `json:"n"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return false, "非 JSON 响应: " + truncateStr(string(body), 100)
		}
		if !r.State {
			return false, r.Error
		}
		return true, fmt.Sprintf("成功，count=%d，示例: %s", r.Count, firstDiagName(r.Data))
	}

	// 1. 会话有效性（my.115.com 不做设备校验）
	results["会话检测(my.115.com)"] = diagResult(probe("nav", "https://my.115.com/?ct=ajax&ac=nav", nil, ua115Unified()))

	// 2. 无参数探测（p115client：被风控的 /files 不带参数仍可用，可判定域名是否被标记）
	results["无参数探测(webapi.115.com)"] = diagResult(probe("noparam", "https://webapi.115.com/files", nil, ua115Unified()))

	// 3. 各镜像域名（统一 UA + 标准参数）
	for _, origin := range webapiFileOrigins {
		name := "列目录(" + strings.TrimPrefix(origin, "https://") + ")"
		results[name] = diagResult(probe("files", origin+"/files", build115FileQuery("0", 0), ua115Unified()))
	}

	// 4. 各 UA（主域名 + 标准参数）
	uaList := []struct{ name, ua string }{
		{"统一UA(115Browser)", ua115Unified()},
		{"朴素UA(Mozilla/5.0)", "Mozilla/5.0"},
		{"Chrome浏览器UA", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36"},
		{"115disk UA", "Mozilla/5.0 115disk/30.1.0"},
	}
	for _, u := range uaList {
		results["UA测试-"+u.name] = diagResult(probe("files", "https://webapi.115.com/files", build115FileQuery("0", 0), u.ua))
	}

	c.JSON(http.StatusOK, gin.H{
		"cookie_len": len(cookie),
		"results":    results,
		"hint":       "判读：会话检测失败=Cookie 无效需重新扫码；无参数探测成功但列目录失败=域名被风控（镜像行若有成功项则已自动回退可用）；所有列目录都失败但会话成功=UA 配对或账号风控问题，把本报告发回。",
	})
}

// diagResult 包装探测结果
func diagResult(ok bool, info string) gin.H {
	return gin.H{"ok": ok, "info": info}
}

// firstDiagName 取第一个文件名（无数据返回空）
func firstDiagName(data []struct {
	N string `json:"n"`
}) string {
	if len(data) == 0 {
		return "-"
	}
	return data[0].N
}

// GetSetting 获取通用配置
// GET /config/setting?key=strm
func (h *Handler) GetSetting(c *gin.Context) {
	key := c.Query("key")
	value := h.settingValueRaw(key)
	if sensitiveSettingKeys[key] {
		value = maskSensitiveJSON(value)
	}
	c.JSON(http.StatusOK, gin.H{"key": key, "value": value})
}

// settingValueRaw 通用配置读取（配置源 > 数据库回退）
func (h *Handler) settingValueRaw(key string) string {
	value := h.Config.GetSetting(key)
	if value == "" {
		var s model.Setting
		if err := h.DB.Where("key = ?", key).First(&s).Error; err == nil {
			value = s.Value
		}
	}
	return value
}

// ---- 敏感字段掩码（GET 脱敏 / SAVE 回填，管理员态防 XSS 窃密链） ----
// 掩码标记 "••••"：表单原样回传即保持旧值，清空才是真清除——前端零改动
const settingMask = "••••"

var sensitiveSettingKeys = map[string]bool{
	"message":     true, // 企微 Secret/Token/AESKey、TG token、OneBot/飞书/QQ 密钥
	"emby":        true, // api_key
	"emby-notify": true, // webhook token
	"tgsub":       true, // TG bot token
}

var sensitiveFieldRe = regexp.MustCompile(`(?i)secret|token|api_?key|aes_?key|password`)

func maskSensitiveJSON(v string) string {
	if v == "" {
		return v
	}
	var root any
	if json.Unmarshal([]byte(v), &root) != nil {
		return v
	}
	var walk func(n any) any
	walk = func(n any) any {
		switch t := n.(type) {
		case map[string]any:
			for k, val := range t {
				if sensitiveFieldRe.MatchString(k) {
					if sv, ok := val.(string); ok && sv != "" {
						t[k] = settingMask
						continue
					}
				}
				t[k] = walk(val)
			}
			return t
		case []any:
			for i, val := range t {
				t[i] = walk(val)
			}
			return t
		}
		return n
	}
	b, err := json.Marshal(walk(root))
	if err != nil {
		return v
	}
	return string(b)
}

// unmaskSensitiveJSON 保存前回填：新值里的掩码字段还原为旧值
func unmaskSensitiveJSON(newV, oldV string) string {
	if newV == "" || oldV == "" {
		return newV
	}
	var nn, oo any
	if json.Unmarshal([]byte(newV), &nn) != nil || json.Unmarshal([]byte(oldV), &oo) != nil {
		return newV
	}
	var walk func(n, o any) any
	walk = func(n, o any) any {
		nt, ok := n.(map[string]any)
		if !ok {
			return n
		}
		ot, _ := o.(map[string]any)
		for k, val := range nt {
			if sv, ok := val.(string); ok && sv == settingMask && ot != nil {
				if ov, exist := ot[k]; exist {
					nt[k] = ov
					continue
				}
			}
			ov := any(nil)
			if ot != nil {
				ov = ot[k]
			}
			nt[k] = walk(val, ov)
		}
		return nt
	}
	b, err := json.Marshal(walk(nn, oo))
	if err != nil {
		return newV
	}
	return string(b)
}

// SaveSetting 保存通用配置（key-value，value 为 JSON 字符串）
// POST /config/setting  body: {"key":"strm","value":"{...}"}
func (h *Handler) SaveSetting(c *gin.Context) {
	var req struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	// 敏感键：表单回传的掩码字段回填旧值（未改动的密钥不丢）
	if sensitiveSettingKeys[req.Key] {
		req.Value = unmaskSensitiveJSON(req.Value, h.settingValueRaw(req.Key))
	}
	if err := h.Config.SaveSetting(req.Key, req.Value); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	log.Printf("[配置] ✓ 已保存：%s", req.Key)
	// emby 配置保存时同步更新反代缓存
	if req.Key == "emby" {
		UpdateEmbyConfig(req.Value)
	}
	// AI 增强识别配置有 5 分钟缓存，保存后立刻失效，否则刚改完还按旧地址调
	if req.Key == aiSettingKey {
		invalidateAICfgCache()
	}
	// 消息配置保存后重新生成企微聊天底栏菜单（启动时也会自动生成；
	// 覆盖式创建，配置齐全才尝试，失败只记日志不打断保存）
	if req.Key == "message" {
		invalidateMsgCfgCache() // 通知配置缓存立即失效（5 秒 TTL 内也立刻生效）
		go func() {
			cfg, err := loadMessageConfig()
			if err != nil {
				return
			}
			if !cfg.Wecom.isEnabled() || cfg.Wecom.CorpID == "" || cfg.Wecom.AgentID == "" {
				return
			}
			if err := wecomMenuCreate(cfg.Wecom); err != nil {
				log.Printf("[企微菜单] ○ 保存后生成失败（下次启动会自动重试）: %v", err)
			} else {
				log.Printf("[企微菜单] ✓ 底栏菜单已生成（自动整理 / 增量同步）")
			}
		}()
	}
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}
