package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// 上手清单：总览面板顶部那张卡片的数据源。
//
// 新用户登录后不知道下一步做什么（「上手五步」只写在 README 里），清单只负责两件事：
// 判定每一步做没做、把人带到能做这一步的页签。**不复制任何配置表单** ——
// 同一份配置多一个编辑入口就多一处整存整取互相覆盖的机会（incr.cron 吃过一次）。
//
// 判定一律读现有配置与台账，**零 115 请求**；只有 Emby 一项要读一次 Emby 的媒体库列表，
// 结果缓存一分钟（总览 30 秒轮询一次）。

// setupStep 清单里的一步。Key 前端用来配跳转地址与按钮，别改名
type setupStep struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Done     bool   `json:"done"`
	Detail   string `json:"detail"`             // 现在是什么状态 / 还差什么，一句人话
	Optional bool   `json:"optional,omitempty"` // 不做也能用（Emby、整理）
}

// setupInputs 判定要用到的全部事实，由 handler 收集，planSetup 只做判断（便于单测）
type setupInputs struct {
	Pan115     bool   // 有 Cookie 或 OpenAPI 已授权
	Username   string // 容量缓存里读到的 115 用户名（没缓存就空，不为它发请求）
	LibCid     string
	Share      string
	Existing   string
	Redundant  string
	TmdbKey    bool
	StrmCount  int64
	EmbyConfig bool   // 填了服务器地址
	EmbyKey    bool   // 填了 API 密钥
	EmbyErr    string // 读 Emby 媒体库失败的原因
	EmbyLibs   int    // Emby 媒体库个数
	EmbyCover  int    // 按当前路径映射落在本地媒体目录下的 Emby 媒体库个数
	EmbySugg   string // 推算出的「Emby 媒体库目录」
	Organized  int64  // 成功整理入库的记录数
	LocalRoot  string
}

func planSetup(in setupInputs) []setupStep {
	steps := make([]setupStep, 0, 7)

	s := setupStep{Key: "pan115", Title: "登录 115 账号", Done: in.Pan115}
	switch {
	case in.Pan115 && in.Username != "":
		s.Detail = "已登录：" + in.Username
	case in.Pan115:
		s.Detail = "已登录"
	default:
		s.Detail = "用 115 App 扫码登录，或用 OpenAPI 授权"
	}
	steps = append(steps, s)

	lib := in.LibCid != "" && in.LibCid != "0"
	s = setupStep{Key: "library", Title: "选择 115 上的媒体库目录", Done: lib}
	if lib {
		s.Detail = "已选择"
	} else {
		s.Detail = "整理好的影视会放进这个目录，本地 STRM 也照它的结构生成"
	}
	steps = append(steps, s)

	var lack []string
	if in.Share == "" {
		lack = append(lack, "转存/待整理文件夹（在媒体库卡片上设）")
	}
	if in.Existing == "" {
		lack = append(lack, "已存在")
	}
	if in.Redundant == "" {
		lack = append(lack, "冗余")
	}
	s = setupStep{Key: "workspace", Title: "准备工作目录", Done: len(lack) == 0}
	if len(lack) == 0 {
		s.Detail = "转存、已存在、冗余三个目录都已配置"
	} else {
		s.Detail = "还缺：" + strings.Join(lack, "、") + "。可以一键在网盘根下创建 /StrmStation"
	}
	steps = append(steps, s)

	s = setupStep{Key: "tmdb", Title: "填写 TMDB API Key", Done: in.TmdbKey}
	if in.TmdbKey {
		s.Detail = "已填写"
	} else {
		s.Detail = "识别片名、刮削海报都靠它，在 themoviedb.org 免费申请"
	}
	steps = append(steps, s)

	s = setupStep{Key: "full", Title: "跑一次全量同步", Done: in.StrmCount > 0}
	if in.StrmCount > 0 {
		s.Detail = "本地已有 STRM 文件"
	} else {
		s.Detail = "把媒体库里已有的影视生成 STRM 到本地 " + in.LocalRoot + "；库还是空的也可以先跳过"
	}
	steps = append(steps, s)

	s = setupStep{Key: "emby", Title: "接上 Emby / Jellyfin", Optional: true}
	switch {
	case !in.EmbyConfig:
		s.Detail = "填服务器地址与 API 密钥后，入库会自动通知 Emby 刷新"
	case !in.EmbyKey:
		s.Detail = "还没填 API 密钥（Emby 后台「设置 → API 密钥」生成）"
	case in.EmbyErr != "":
		s.Detail = in.EmbyErr
	case in.EmbyLibs == 0:
		s.Detail = "Emby 里还没有媒体库：添加一个媒体库，目录指向挂载进 Emby 的 STRM 目录"
	case in.EmbyCover == 0 && in.EmbySugg != "":
		s.Detail = "Emby 的媒体库对不上本地 " + in.LocalRoot + "，「Emby 媒体库目录」多半应填 " + in.EmbySugg
	case in.EmbyCover == 0:
		s.Detail = "Emby 的媒体库对不上本地 " + in.LocalRoot + "：检查 Emby 挂载的目录和路径映射"
	default:
		s.Done = true
		s.Detail = "已连接，媒体库目录对得上"
	}
	steps = append(steps, s)

	s = setupStep{Key: "organize", Title: "整理第一部影视", Done: in.Organized > 0, Optional: true}
	if in.Organized > 0 {
		s.Detail = "已经整理入库过"
	} else {
		s.Detail = "在「发现与订阅」找一部片转存，或往转存目录丢一部，整理会自动接手"
	}
	steps = append(steps, s)
	return steps
}

// setupEmbyFacts 读一次 Emby 媒体库并按路径映射判覆盖，缓存一分钟
var (
	setupEmbyMu    sync.Mutex
	setupEmbyAt    time.Time
	setupEmbyKey   string
	setupEmbyCache setupInputs
)

func setupEmbyFacts(root string) setupInputs {
	var out setupInputs
	cfg, ok := loadEmbyRefreshCfg()
	out.EmbyConfig = ok
	out.EmbyKey = cfg.APIKey != ""
	if !ok || !out.EmbyKey {
		return out
	}
	// 配置一改缓存就作废：用户刚在设置页改完映射，回到总览看到的得是新结果
	key := cfg.ServerURL + "|" + cfg.APIKey + "|" + cfg.PathMapping + "|" + root
	setupEmbyMu.Lock()
	if key == setupEmbyKey && time.Since(setupEmbyAt) < time.Minute {
		c := setupEmbyCache
		setupEmbyMu.Unlock()
		return c
	}
	setupEmbyMu.Unlock()

	sug := embyPathSuggestion(cfg, root)
	if e, _ := sug["error"].(string); e != "" {
		out.EmbyErr = e
	}
	// libraries 是 embyPathSuggestion 里的局部类型切片，借 JSON 数个数
	if b, err := json.Marshal(sug["libraries"]); err == nil {
		var arr []json.RawMessage
		if json.Unmarshal(b, &arr) == nil {
			out.EmbyLibs = len(arr)
		}
	}
	out.EmbyCover, _ = sug["covered"].(int)
	out.EmbySugg, _ = sug["suggest"].(string)

	setupEmbyMu.Lock()
	setupEmbyKey, setupEmbyAt, setupEmbyCache = key, time.Now(), out
	setupEmbyMu.Unlock()
	return out
}

// setupHiddenKey 用户点了「不再显示」。存在 setting 里而不是浏览器：换台设备不该再弹一遍
const setupHiddenKey = "setup"

// SetupStatus GET /setup/status
func (h *Handler) SetupStatus(c *gin.Context) {
	in := setupInputs{}
	if ck, err := h.get115Cookie(); err == nil && ck != "" {
		in.Pan115 = true
	} else if oc := h.getOpen115(); oc != nil && oc.authorized() {
		in.Pan115 = true
	}
	// 只看缓存，不为一个用户名去请求 115（总览自己会刷新容量缓存）
	pan115CapMu.Lock()
	if pan115CapCache != nil {
		in.Username = pan115CapCache.Username
	}
	pan115CapMu.Unlock()

	slots := workspaceSlots(h.getSettingValue)
	for _, s := range slots {
		switch s.key {
		case "library":
			in.LibCid = s.cid
		case "share":
			in.Share = s.cid
		case "existing":
			in.Existing = s.cid
		case "redundant":
			in.Redundant = s.cid
		}
	}
	// 媒体库表（V2）：有库就算选了目录；转存文件夹每个库自己设，有一个库设了就算
	if libs := loadLibraries(); len(libs) > 0 {
		in.LibCid, in.Share = libs[0].Cid, ""
		if org := organizeLibs(libs); len(org) > 0 {
			in.Share = org[0].StagingCid
		}
	}

	var tmdb model.TmdbConfig
	if h.DB.First(&tmdb).Error == nil && strings.TrimSpace(tmdb.ApiKey) != "" {
		in.TmdbKey = true
	}
	h.DB.Model(&model.SyncedFile{}).Where("kind = ?", "video").Limit(1).Count(&in.StrmCount)
	h.DB.Model(&model.OrganizeRecord{}).Where("status = ?", "success").Limit(1).Count(&in.Organized)

	root := strings.TrimRight(filepath.ToSlash(localMediaRoot()), "/")
	in.LocalRoot = root
	e := setupEmbyFacts(root)
	in.EmbyConfig, in.EmbyKey, in.EmbyErr = e.EmbyConfig, e.EmbyKey, e.EmbyErr
	in.EmbyLibs, in.EmbyCover, in.EmbySugg = e.EmbyLibs, e.EmbyCover, e.EmbySugg

	steps := planSetup(in)
	done := true
	for _, s := range steps {
		if !s.Done && !s.Optional {
			done = false
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"steps":  steps,
		"done":   done,
		"hidden": jsonBoolField(h.getSettingValue(setupHiddenKey), "hidden"),
	})
}

// SetupHide POST /setup/hide {hidden}：「不再显示」与「重新显示」
func (h *Handler) SetupHide(c *gin.Context) {
	var req struct {
		Hidden bool `json:"hidden"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	b, _ := json.Marshal(gin.H{"hidden": req.Hidden})
	if err := h.Config.SaveSetting(setupHiddenKey, string(b)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败：" + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"hidden": req.Hidden})
}

func jsonBoolField(raw, field string) bool {
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return false
	}
	v, _ := m[field].(bool)
	return v
}
