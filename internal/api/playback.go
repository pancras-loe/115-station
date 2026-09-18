package api

// ==================== 播放账号（多端播放 + 小号播放） ====================
//
// 两种按需副本机制，播放取直链时自动选路：
//
//	多端播放（自己的设备）：同账号副本——PlaybackInfo 时识别设备（Emby
//	  DeviceId），首次播某文件把它复制到 主号副本目录/<设备名>/（115 同
//	  账号复制瞬时），播副本消除"同文件多端并发"的风控特征。不需要小号。
//
//	小号播放（其他观众）：观众自带 115 小号（管理员扫码代绑到设备）——
//	  首次播某文件时 主号出分享 → 观众小号秒传接收（镜像目录），
//	  之后用观众小号的 Cookie 取直链。主号零播放暴露。
//
// 优先级：设备绑定了小号 → 小号；未绑定且多端开启 → 设备副本；都没有
// → 主号直链（/d/ 原路径）。副本映射落 PlaybackCopy，命中秒开。

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"gorm.io/gorm"

	"strmhub/internal/config"
	"strmhub/internal/model"
)

// ==================== 配置 ====================

type playbackCfg struct {
	MultiEnabled bool          `json:"multi_enabled"` // 多端播放（设备副本）开关
	CopyRootCID  string        `json:"copy_root_cid"` // 主号副本根目录
	CopyRootName string        `json:"copy_root_name"`
	Alts         []playbackAlt `json:"alts"`    // 观众小号
	Routing      string        `json:"routing"` // device / round
}

type playbackAlt struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Cookie  string `json:"cookie"`
	Enabled bool   `json:"enabled"`
	Nick    string `json:"nick"`
	RootCid string `json:"root_cid"`
}

var (
	playbackMu       sync.Mutex
	playbackCfgV     *playbackCfg
	playbackCfgAt    time.Time
	playbackStateMu  sync.Mutex
	playbackStateMap = map[int64]string{} // 每小号最近一次同步状态
)

func loadPlaybackCfg() *playbackCfg {
	playbackMu.Lock()
	defer playbackMu.Unlock()
	if playbackCfgV != nil && time.Since(playbackCfgAt) < 5*time.Second {
		return playbackCfgV
	}
	cfg := &playbackCfg{}
	if v := settingValueCompat("playback"); v != "" {
		_ = json.Unmarshal([]byte(v), cfg)
	}
	playbackCfgV = cfg
	playbackCfgAt = time.Now()
	return cfg
}

func savePlaybackCfg(c *playbackCfg) error {
	b, _ := json.Marshal(c)
	if err := notifyConfigSource.SaveSetting("playback", string(b)); err != nil {
		return err
	}
	playbackMu.Lock()
	playbackCfgV = nil
	playbackMu.Unlock()
	return nil
}

func playbackSetAltErr(id int64, msg string) {
	playbackStateMu.Lock()
	playbackStateMap[id] = msg
	playbackStateMu.Unlock()
}

func altOps(alt *playbackAlt) *pan115Ops {
	return &pan115Ops{cookie: alt.Cookie}
}

// altVerifyCookie 验证 115 Cookie，返回 (uid, 昵称, 错误)
func altVerifyCookie(cookie string) (string, string, error) {
	body, err := httpGet115Full("https://webapi.115.com/user/infos", nil, cookie, ua115Unified(), 15*time.Second, nil)
	if err != nil {
		return "", "", err
	}
	var r struct {
		State bool `json:"state"`
		Data  struct {
			UID      any    `json:"uid"`
			UserName string `json:"user_name"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &r) != nil || !r.State {
		return "", "", fmt.Errorf("Cookie 无效（%s）", truncateStr(string(body), 80))
	}
	return fmt.Sprint(r.Data.UID), r.Data.UserName, nil
}

// ==================== 设备身份 ====================

var reEmbyAuthField = regexp.MustCompile(`([A-Za-z]+)="([^"]*)"`)

// playbackDeviceKeyFromUA 设备键：UA 的 fnv 哈希（同一播放器 UA 稳定）
func playbackDeviceKeyFromUA(ua string) (string, string) {
	if strings.TrimSpace(ua) == "" {
		return "", ""
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(ua))
	return "u" + fmt.Sprint(h.Sum32()), truncateStr(ua, 40)
}

// playbackBoundAlt 设备绑定的小号（无绑定返回 nil）
func playbackBoundAlt(cfg *playbackCfg, db *gorm.DB, devKey string) *playbackAlt {
	if devKey == "" {
		return nil
	}
	var d model.PlaybackDevice
	if err := db.Where("ua_hash = ?", devKey).First(&d).Error; err != nil || d.AltID == 0 {
		return nil
	}
	for i := range cfg.Alts {
		if cfg.Alts[i].ID == d.AltID && cfg.Alts[i].Enabled {
			return &cfg.Alts[i]
		}
	}
	return nil
}

// ==================== HTTP 处理器 ====================

// PlaybackGetConfig GET /playback/config（Cookie 不回传）
func (h *Handler) PlaybackGetConfig(c *gin.Context) {
	cfg := loadPlaybackCfg()
	type altOut struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
		Nick    string `json:"nick"`
		RootCID string `json:"root_cid"`
		Covered int64  `json:"covered"`
		LastErr string `json:"last_err"`
	}
	out := make([]altOut, 0, len(cfg.Alts))
	for _, a := range cfg.Alts {
		o := altOut{ID: a.ID, Name: a.Name, Enabled: a.Enabled, Nick: a.Nick, RootCID: a.RootCid}
		model.DB.Model(&model.PlaybackAltFile{}).Where("account_id = ?", a.ID).Count(&o.Covered)
		playbackStateMu.Lock()
		o.LastErr = playbackStateMap[a.ID]
		playbackStateMu.Unlock()
		out = append(out, o)
	}
	var devices []model.PlaybackDevice
	model.DB.Order("last_seen DESC").Limit(50).Find(&devices)
	c.JSON(http.StatusOK, gin.H{"cfg": gin.H{
		"multi_enabled":  cfg.MultiEnabled,
		"copy_root_cid":  cfg.CopyRootCID,
		"copy_root_name": cfg.CopyRootName,
		"alts":           out,
		"devices":        devices,
	}})
}

// PlaybackSaveMulti POST /playback/multi
func (h *Handler) PlaybackSaveMulti(c *gin.Context) {
	var req struct {
		MultiEnabled bool   `json:"multi_enabled"`
		CopyRootCID  string `json:"copy_root_cid"`
		CopyRootName string `json:"copy_root_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	cfg := loadPlaybackCfg()
	cfg.MultiEnabled = req.MultiEnabled
	if req.CopyRootCID != "" {
		cfg.CopyRootCID = req.CopyRootCID
		cfg.CopyRootName = req.CopyRootName
	}
	if err := savePlaybackCfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

// PlaybackAddAlt POST /playback/alt/add
func (h *Handler) PlaybackAddAlt(c *gin.Context) {
	var req struct {
		Name   string `json:"name"`
		Cookie string `json:"cookie"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Cookie) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写 Cookie"})
		return
	}
	cookie := strings.TrimSpace(req.Cookie)
	uid, nick, err := altVerifyCookie(cookie)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cookie 验证失败: " + err.Error()})
		return
	}
	if mainCookie, err := h.get115Cookie(); err == nil && mainCookie != "" {
		if mainUID, _, _ := altVerifyCookie(mainCookie); uid != "" && uid == mainUID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "该 Cookie 是主号自己，请填观众的小号"})
			return
		}
	}
	cfg := loadPlaybackCfg()
	for _, a := range cfg.Alts {
		if auid, _, _ := altVerifyCookie(a.Cookie); uid != "" && auid == uid {
			c.JSON(http.StatusBadRequest, gin.H{"error": "该小号已存在"})
			return
		}
	}
	var maxID int64
	for _, a := range cfg.Alts {
		if a.ID > maxID {
			maxID = a.ID
		}
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = nick
	}
	cfg.Alts = append(cfg.Alts, playbackAlt{ID: maxID + 1, Name: name, Cookie: cookie, Enabled: true, Nick: nick})
	if err := savePlaybackCfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[播放账号] ✓ 观众小号「%s」已添加（uid=%s）", name, uid)
	c.JSON(http.StatusOK, gin.H{"message": "小号已添加并验证通过（" + nick + "）"})
}

// PlaybackDelAlt POST /playback/alt/del
func (h *Handler) PlaybackDelAlt(c *gin.Context) {
	var req struct {
		ID int64 `json:"id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	cfg := loadPlaybackCfg()
	left := cfg.Alts[:0]
	for _, a := range cfg.Alts {
		if a.ID != req.ID {
			left = append(left, a)
		}
	}
	cfg.Alts = left
	if err := savePlaybackCfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	model.DB.Where("account_id = ?", req.ID).Delete(&model.PlaybackAltFile{})
	model.DB.Where("alt_id = ?", req.ID).Delete(&model.PlaybackDevice{})
	c.JSON(http.StatusOK, gin.H{"message": "已移除"})
}

// PlaybackToggleAlt POST /playback/alt/toggle
func (h *Handler) PlaybackToggleAlt(c *gin.Context) {
	var req struct {
		ID      int64 `json:"id"`
		Enabled bool  `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	cfg := loadPlaybackCfg()
	for i := range cfg.Alts {
		if cfg.Alts[i].ID == req.ID {
			cfg.Alts[i].Enabled = req.Enabled
		}
	}
	if err := savePlaybackCfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已更新"})
}

// PlaybackBindDevice POST /playback/bind {device_key, alt_id}（0=解绑）
func (h *Handler) PlaybackBindDevice(c *gin.Context) {
	var req struct {
		DeviceKey string `json:"device_key"`
		AltID     int64  `json:"alt_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.DeviceKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	var d model.PlaybackDevice
	if err := model.DB.Where("ua_hash = ?", req.DeviceKey).First(&d).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "设备不存在（需先播放一次）"})
		return
	}
	d.AltID = req.AltID
	d.AltName = ""
	if req.AltID > 0 {
		cfg := loadPlaybackCfg()
		for _, a := range cfg.Alts {
			if a.ID == req.AltID {
				d.AltName = a.Name
			}
		}
	}
	model.DB.Save(&d)
	c.JSON(http.StatusOK, gin.H{"message": "绑定已更新"})
}

// PlaybackSetAltRoot POST /playback/alt/root {id, cid}
func (h *Handler) PlaybackSetAltRoot(c *gin.Context) {
	var req struct {
		ID  string `json:"id"`
		CID string `json:"cid"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ID == "" || req.CID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	cfg := loadPlaybackCfg()
	for i := range cfg.Alts {
		if fmt.Sprint(cfg.Alts[i].ID) == req.ID {
			cfg.Alts[i].RootCid = req.CID
			if err := savePlaybackCfg(cfg); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"message": "镜像目录已保存"})
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "小号不存在"})
}

// PlaybackSaveMode POST /playback/mode {routing}
func (h *Handler) PlaybackSaveMode(c *gin.Context) {
	var req struct {
		Routing string `json:"routing"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	cfg := loadPlaybackCfg()
	if req.Routing == "device" || req.Routing == "round" {
		cfg.Routing = req.Routing
	}
	if err := savePlaybackCfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

// PlaybackDevices GET /playback/devices
func (h *Handler) PlaybackDevices(c *gin.Context) {
	var devices []model.PlaybackDevice
	model.DB.Order("last_seen DESC").Limit(50).Find(&devices)
	c.JSON(http.StatusOK, gin.H{"data": devices})
}

// PlaybackSync POST /playback/sync
func (h *Handler) PlaybackSync(c *gin.Context) {
	if !playbackSyncRunning.CompareAndSwap(false, true) {
		c.JSON(http.StatusConflict, gin.H{"error": "同步正在进行中"})
		return
	}
	go func() {
		defer playbackSyncRunning.Store(false)
		cfg := loadPlaybackCfg()
		for i := range cfg.Alts {
			if !cfg.Alts[i].Enabled {
				continue
			}
			alt := &cfg.Alts[i]
			n, err := h.playbackMirrorAlt(alt)
			if err != nil {
				log.Printf("[播放账号] ✗ 小号「%s」镜像失败: %v", alt.Name, err)
				playbackSetAltErr(alt.ID, err.Error())
			} else {
				log.Printf("[播放账号] ✓ 小号「%s」镜像同步完成：覆盖 %d 个文件", alt.Name, n)
				playbackSetAltErr(alt.ID, "")
			}
		}
	}()
	c.JSON(http.StatusOK, gin.H{"message": "镜像同步已开始，结果见运行日志"})
}

// ==================== 镜像同步 ====================

var playbackSyncRunning atomic.Bool

// playbackMirrorAlt 小号预镜像（台账全量对账；播放时另有按需兜底）
func (h *Handler) playbackMirrorAlt(alt *playbackAlt) (int, error) {
	if strings.TrimSpace(alt.Cookie) == "" {
		return 0, fmt.Errorf("Cookie 为空")
	}
	var ledger []model.SyncedFile
	if err := model.DB.Where("kind = ? AND pick_code <> ''", "video").Find(&ledger).Error; err != nil {
		return 0, err
	}
	if len(ledger) == 0 {
		return 0, fmt.Errorf("同步台账为空（先完成一次全量同步）")
	}
	ledgerMap := map[string]string{}
	for _, sf := range ledger {
		ledgerMap[strings.Trim(sf.RelPath, "/")] = sf.PickCode
	}
	altOpsC := altOps(alt)
	altRoot, err := altEnsureRoot(altOpsC, alt)
	if err != nil {
		return 0, err
	}
	altTree := map[string]string{}
	walkAltDir(altOpsC, altRoot, "", altTree)
	var missing []string
	for rel := range ledgerMap {
		if _, ok := altTree[rel]; !ok {
			missing = append(missing, rel)
		}
	}
	sort.Strings(missing)
	byDir := map[string][]string{}
	for _, rel := range missing {
		byDir[relDirOf(rel)] = append(byDir[relDirOf(rel)], rel)
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	mainCookie, _ := h.get115Cookie()
	for _, dir := range dirs {
		select {
		case <-stopCh:
			return playbackSaveMapping(alt.ID, ledgerMap, altTree), nil
		default:
		}
		var targetCid string
		var derr error
		if dir != "" {
			targetCid, derr = altEnsureDir(altOpsC, altRoot, dir)
		} else {
			targetCid = altRoot
		}
		if derr != nil || targetCid == "" {
			continue
		}
		var fileIDs []string
		for _, rel := range byDir[dir] {
			if fid := mainFileIDByPick(mainCookie, ledgerMap[rel]); fid != "" {
				fileIDs = append(fileIDs, fid)
			}
		}
		if len(fileIDs) == 0 {
			continue
		}
		shareCode, receiveCode, serr := mainShareFiles(mainCookie, fileIDs)
		if serr != nil {
			continue
		}
		if err := altReceiveShare(alt.Cookie, shareCode, receiveCode, targetCid); err != nil {
			continue
		}
		time.Sleep(500 * time.Millisecond)
		if dir != "" {
			walkAltDir(altOpsC, targetCid, dir, altTree)
		} else {
			walkAltDir(altOpsC, altRoot, "", altTree)
		}
	}
	return playbackSaveMapping(alt.ID, ledgerMap, altTree), nil
}

// ==================== 按需副本 ====================

// playbackRelPath pickcode → 台账相对路径
func playbackRelPath(db *gorm.DB, pickcode string) string {
	var sf model.SyncedFile
	if err := db.Where("pick_code = ? AND kind = ?", pickcode, "video").First(&sf).Error; err != nil {
		return ""
	}
	return strings.Trim(sf.RelPath, "/")
}

// playbackEnsureAltCopy 确保观众小号有该文件副本
func (h *Handler) playbackEnsureAltCopy(alt *playbackAlt, rel string) (string, error) {
	var m model.PlaybackAltFile
	if err := model.DB.Where("account_id = ? AND rel_path = ?", alt.ID, rel).First(&m).Error; err == nil && m.AltPickCode != "" {
		return m.AltPickCode, nil
	}
	mainCookie, err := h.get115Cookie()
	if err != nil {
		return "", err
	}
	var sf model.SyncedFile
	if err := model.DB.Where("rel_path = ? AND kind = ?", rel, "video").First(&sf).Error; err != nil || sf.PickCode == "" {
		return "", fmt.Errorf("台账无此文件")
	}
	fid := mainFileIDByPick(mainCookie, sf.PickCode)
	if fid == "" {
		return "", fmt.Errorf("主号文件定位失败")
	}
	altOpsC := altOps(alt)
	altRoot, err := altEnsureRoot(altOpsC, alt)
	if err != nil {
		return "", err
	}
	dir := relDirOf(rel)
	targetCid := altRoot
	if dir != "" {
		if targetCid, err = altEnsureDir(altOpsC, altRoot, dir); err != nil {
			return "", err
		}
	}
	shareCode, receiveCode, err := mainShareFiles(mainCookie, []string{fid})
	if err != nil {
		return "", err
	}
	if err := altReceiveShare(alt.Cookie, shareCode, receiveCode, targetCid); err != nil {
		return "", err
	}
	time.Sleep(600 * time.Millisecond)
	tree := map[string]string{}
	walkAltDir(altOpsC, targetCid, dir, tree)
	altPC := tree[rel]
	if altPC == "" {
		return "", fmt.Errorf("转存后未找到副本")
	}
	model.DB.Create(&model.PlaybackAltFile{AccountID: alt.ID, RelPath: rel, AltPickCode: altPC})
	return altPC, nil
}

// playbackEnsureDeviceCopy 确保主号内该设备副本
func (h *Handler) playbackEnsureDeviceCopy(cfg *playbackCfg, devKey, devName, pickcode string) (string, error) {
	var c model.PlaybackCopy
	if err := model.DB.Where("device_key = ? AND main_pick_code = ?", devKey, pickcode).First(&c).Error; err == nil && c.CopyPickCode != "" {
		model.DB.Model(&c).Updates(map[string]interface{}{"last_played": time.Now()})
		return c.CopyPickCode, nil
	}
	cookie, err := h.get115Cookie()
	if err != nil {
		return "", err
	}
	rel := playbackRelPath(model.DB, pickcode)
	if rel == "" {
		return "", fmt.Errorf("台账无此文件")
	}
	root := cfg.CopyRootCID
	if root == "" {
		ops, oerr := h.newPan115Ops()
		if oerr != nil {
			return "", oerr
		}
		cid, cerr := ops.mkdir("0", "多端播放")
		if cerr != nil {
			return "", cerr
		}
		cfg.CopyRootCID = cid
		cfg.CopyRootName = "多端播放"
		_ = savePlaybackCfg(cfg)
		root = cid
	}
	dirName := devName
	if dirName == "" {
		dirName = devKey
	}
	dirName = sanitizePlaybackDirName(dirName)
	ops, oerr := h.newPan115Ops()
	if oerr != nil {
		return "", oerr
	}
	dirCid, derr := ops.ensurePath(root, dirName)
	if derr != nil {
		return "", derr
	}
	fid := ""
	var sf model.SyncedFile
	if err := model.DB.Where("rel_path = ? AND kind = ?", rel, "video").First(&sf).Error; err == nil && sf.FileID != "" {
		fid = sf.FileID
	} else {
		fid = mainFileIDByPick(cookie, pickcode)
	}
	if fid == "" {
		return "", fmt.Errorf("主号文件定位失败")
	}
	copyPick, cerr := copy115File(cookie, fid, dirCid)
	if cerr != nil {
		return "", cerr
	}
	model.DB.Create(&model.PlaybackCopy{
		DeviceKey: devKey, MainPickCode: pickcode, CopyPickCode: copyPick, RelPath: rel, LastPlayed: time.Now(),
	})
	return copyPick, nil
}

// ==================== 115 同账号复制 ====================

// copy115File 同账号复制文件到目录
func copy115File(cookie, fid, targetCid string) (string, error) {
	form := url.Values{
		"pid":    {targetCid},
		"fid[0]": {fid},
	}
	body, err := httpPostForm115("https://webapi.115.com/files/copy", form, cookie, 15*time.Second)
	if err != nil {
		return "", err
	}
	var r struct {
		State bool `json:"state"`
		Data  struct {
			PickCode string `json:"pick_code"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &r) != nil || !r.State {
		return "", fmt.Errorf("复制被拒: %s", truncateStr(string(body), 100))
	}
	if r.Data.PickCode != "" {
		return r.Data.PickCode, nil
	}
	return "", fmt.Errorf("复制响应无 pick_code: %s", truncateStr(string(body), 100))
}

// ==================== 工具函数 ====================

// altEnsureRoot 小号副本根目录
func altEnsureRoot(ops *pan115Ops, alt *playbackAlt) (string, error) {
	if alt.RootCid != "" {
		if _, err := get115DirInfo(alt.Cookie, alt.RootCid); err == nil {
			return alt.RootCid, nil
		}
	}
	cid, err := ops.mkdir("0", "strmhub_media_alt")
	if err != nil {
		return "", fmt.Errorf("创建镜像根目录失败: %w", err)
	}
	alt.RootCid = cid
	cfg := loadPlaybackCfg()
	for i := range cfg.Alts {
		if cfg.Alts[i].ID == alt.ID {
			cfg.Alts[i].RootCid = cid
		}
	}
	_ = savePlaybackCfg(cfg)
	return cid, nil
}

// walkAltDir 递归遍历小号目录树
func walkAltDir(ops *pan115Ops, cid, prefix string, out map[string]string) {
	entries, _, err := ops.listEntries(cid, 0)
	if err != nil {
		return
	}
	type sd struct{ id, name string }
	var subs []sd
	for _, e := range entries {
		if fmt.Sprint(e["f"]) == "1" {
			name := fmt.Sprint(e["n"])
			rel := name
			if prefix != "" {
				rel = prefix + "/" + name
			}
			if pc := fmt.Sprint(e["pc"]); pc != "" {
				out[rel] = pc
			}
		} else {
			subs = append(subs, sd{fmt.Sprint(e["fid"]), fmt.Sprint(e["n"])})
		}
	}
	for _, s := range subs {
		rel := s.name
		if prefix != "" {
			rel = prefix + "/" + s.name
		}
		walkAltDir(ops, s.id, rel, out)
	}
}

// altEnsureDir 逐级确保目录存在
func altEnsureDir(ops *pan115Ops, rootCid, relDir string) (string, error) {
	cur := rootCid
	for _, seg := range strings.Split(relDir, "/") {
		if seg == "" {
			continue
		}
		found := ""
		dirs, _, _, err := ops.listDirs(cur)
		if err == nil {
			for _, d := range dirs {
				if fmt.Sprint(d["name"]) == seg {
					found = fmt.Sprint(d["cid"])
					break
				}
			}
		}
		if found == "" {
			var err error
			found, err = mkdir115(ops.cookie, cur, seg)
			if err != nil {
				dirs, _, _, err2 := ops.listDirs(cur)
				if err2 == nil {
					for _, d := range dirs {
						if fmt.Sprint(d["name"]) == seg {
							found = fmt.Sprint(d["cid"])
							break
						}
					}
				}
				if found == "" {
					return "", err
				}
			}
		}
		cur = found
		time.Sleep(200 * time.Millisecond)
	}
	return cur, nil
}

// mainFileIDByPick pickcode → file_id
func mainFileIDByPick(cookie, pickCode string) string {
	body, err := httpGet115Full("https://webapi.115.com/files/getinfo",
		url.Values{"pick_code": {pickCode}}, cookie, ua115Unified(), 15*time.Second, nil)
	if err != nil {
		return ""
	}
	var r struct {
		State bool `json:"state"`
		Data  []struct {
			FileID any `json:"file_id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &r) != nil || !r.State || len(r.Data) == 0 {
		return ""
	}
	return fmt.Sprint(r.Data[0].FileID)
}

// mainShareFiles 主号创建分享
func mainShareFiles(cookie string, fileIDs []string) (string, string, error) {
	if len(fileIDs) == 0 {
		return "", "", fmt.Errorf("无文件")
	}
	form := url.Values{
		"pid":         {"0"},
		"file_ids":    {strings.Join(fileIDs, ",")},
		"is_asc":      {"1"},
		"secret_code": {randomCode(4)},
	}
	body, err := httpPostForm115("https://webapi.115.com/share/send", form, cookie, 15*time.Second)
	if err != nil {
		return "", "", err
	}
	var r struct {
		State bool `json:"state"`
		Data  struct {
			ShareCode   string `json:"share_code"`
			ReceiveCode string `json:"receive_code"`
		} `json:"data"`
		Error    string `json:"error"`
		ErrorMsg string `json:"error_msg"`
	}
	if json.Unmarshal(body, &r) != nil || !r.State {
		msg := r.Error
		if msg == "" {
			msg = r.ErrorMsg
		}
		return "", "", fmt.Errorf("%s: %s", msg, truncateStr(string(body), 100))
	}
	if r.Data.ShareCode == "" || r.Data.ReceiveCode == "" {
		return "", "", fmt.Errorf("响应缺 share_code: %s", truncateStr(string(body), 100))
	}
	return r.Data.ShareCode, r.Data.ReceiveCode, nil
}

func randomCode(n int) string {
	const chars = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}

// playbackSaveMapping 映射落库
func playbackSaveMapping(accountID int64, ledgerMap, altTree map[string]string) int {
	var rows []model.PlaybackAltFile
	for rel := range ledgerMap {
		if altPC, ok := altTree[rel]; ok && altPC != "" {
			rows = append(rows, model.PlaybackAltFile{AccountID: accountID, RelPath: rel, AltPickCode: altPC})
		}
	}
	model.DB.Where("account_id = ?", accountID).Delete(&model.PlaybackAltFile{})
	if len(rows) == 0 {
		return 0
	}
	for i := 0; i < len(rows); i += 500 {
		end := i + 500
		if end > len(rows) {
			end = len(rows)
		}
		model.DB.CreateInBatches(rows[i:end], 200)
	}
	return len(rows)
}

// altReceiveShare 小号侧转存
func altReceiveShare(altCookie, shareCode, receiveCode, targetCid string) error {
	var fids []string
	for offset := 0; ; offset += 1150 {
		body, err := getShareAPI("/share/snap", url.Values{
			"share_code":   {shareCode},
			"receive_code": {receiveCode},
			"cid":          {"0"},
			"offset":       {fmt.Sprint(offset)},
			"limit":        {"1150"},
			"asc":          {"1"},
			"fc_mix":       {"0"},
		}, altCookie, 15*time.Second)
		if err != nil {
			return fmt.Errorf("分享列表获取失败: %s", err.Error())
		}
		var snap struct {
			State bool `json:"state"`
			Data  struct {
				List []struct {
					Fid string `json:"fid"`
				} `json:"list"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &snap) != nil || !snap.State {
			return fmt.Errorf("分享列表被拒: %s", truncateStr(string(body), 100))
		}
		for _, it := range snap.Data.List {
			fids = append(fids, it.Fid)
		}
		if len(snap.Data.List) < 1150 {
			break
		}
	}
	if len(fids) == 0 {
		return nil
	}
	body, err := getShareAPI("/share/receive", url.Values{
		"share_code":   {shareCode},
		"receive_code": {receiveCode},
		"file_id":      {strings.Join(fids, ",")},
		"cid":          {targetCid},
	}, altCookie, 30*time.Second)
	if err != nil {
		return fmt.Errorf("转存提交失败: %s", err.Error())
	}
	var r struct {
		State bool   `json:"state"`
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &r) != nil || !r.State {
		return fmt.Errorf("转存被拒: %s", truncateStr(string(body), 120))
	}
	return nil
}

func relDirOf(rel string) string {
	i := strings.LastIndex(rel, "/")
	if i <= 0 {
		return ""
	}
	return rel[:i]
}

// ==================== 播放路由 ====================

var errPlaybackSkip = fmt.Errorf("playback: 无副本路由，回退主号")

// sanitizePlaybackDirName 设备名做目录名时去除非法字符
func sanitizePlaybackDirName(name string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune("/\\:*?\"<>|", r) {
			return '_'
		}
		return r
	}, name)
}

// playbackResolvePlay 起播解析：观众小号 → 设备副本 → 主号（skip）。
// 返回 /pb/ 相对地址（errPlaybackSkip = 走主号 /d/ 原路径）
func (h *Handler) playbackResolvePlay(db *gorm.DB, devKey, devName, pickcode, ua string) (string, error) {
	cfg := loadPlaybackCfg()
	if alt := playbackBoundAlt(cfg, db, devKey); alt != nil {
		rel := playbackRelPath(db, pickcode)
		if rel != "" {
			altPC, err := h.playbackEnsureAltCopy(alt, rel)
			if err == nil && altPC != "" {
				return "/pb/u-" + fmt.Sprint(alt.ID) + "/" + altPC, nil
			}
			log.Printf("[播放账号] ○ 小号副本获取失败，回退: %v", err)
		}
	}
	if cfg.MultiEnabled {
		pc, err := h.playbackEnsureDeviceCopy(cfg, devKey, devName, pickcode)
		if err == nil && pc != "" {
			return "/pb/d-" + devKey + "/" + pc, nil
		}
		log.Printf("[播放账号] ○ 设备副本失败，回退主号: %v", err)
	}
	return "", errPlaybackSkip
}

// h0EnsureAltCopy playbackEnsureAltCopy 的包内直调变体
func h0EnsureAltCopy(db *gorm.DB, alt *playbackAlt, rel string) (string, error) {
	var m model.PlaybackAltFile
	if err := db.Where("account_id = ? AND rel_path = ?", alt.ID, rel).First(&m).Error; err == nil && m.AltPickCode != "" {
		return m.AltPickCode, nil
	}
	return "", fmt.Errorf("映射未命中")
}

// playbackResolve 主号 302 取链接入（小号/副本优先，未命中回退主号）。
// 由 proxyDownloadURLFull 在每次取链时调用（含真实 UA）
func playbackResolve(db *gorm.DB, cfg *config.Config, mainPickcode, ua string) (string, map[string]string, error) {
	h := &Handler{DB: db, Config: cfg}
	devKey, devName := playbackDeviceKeyFromUA(ua)
	u, err := h.playbackResolvePlay(db, devKey, devName, mainPickcode, ua)
	if err != nil {
		return "", nil, errPlaybackSkip
	}
	// /pb/ 端点在 6086 代理上，组装本机可达地址
	return "http://127.0.0.1:" + cfg.ProxyPortStr() + u, nil, nil
}

// PlaybackAltQrStatus 小号扫码轮询（成功 → Cookie 入账号池，不落主号）
func (h *Handler) PlaybackAltQrStatus(c *gin.Context) {
	var req struct {
		Uid  string `json:"uid"`
		Time int64  `json:"time"`
		Sign string `json:"sign"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Uid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	query := url.Values{
		"uid":  {req.Uid},
		"time": {fmt.Sprint(req.Time)},
		"sign": {req.Sign},
	}
	body, err := httpGetJSON(statusAPI, query, 60*time.Second)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"status": "waiting"})
		return
	}
	var st qrStatusResp
	if json.Unmarshal(body, &st) != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "解析状态失败"})
		return
	}
	switch st.Data.Status {
	case 0:
		c.JSON(http.StatusOK, gin.H{"status": "waiting"})
	case 1:
		c.JSON(http.StatusOK, gin.H{"status": "scanned"})
	case -1:
		h.dropQrSession(req.Uid)
		c.JSON(http.StatusOK, gin.H{"status": "expired"})
	case -2:
		h.dropQrSession(req.Uid)
		c.JSON(http.StatusOK, gin.H{"status": "cancelled"})
	case 2:
		cookie, _, _, _, err := h.fetchQrLoginCookie(req.Uid)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		uid, nick, verr := altVerifyCookie(cookie)
		if verr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Cookie 验证失败: " + verr.Error()})
			return
		}
		if mainCookie, merr := h.get115Cookie(); merr == nil && mainCookie != "" {
			if mainUID, _, _ := altVerifyCookie(mainCookie); uid != "" && uid == mainUID {
				c.JSON(http.StatusBadRequest, gin.H{"error": "扫的是主号自己，请用小号的 115 App 扫码"})
				return
			}
		}
		cfg := loadPlaybackCfg()
		for _, a := range cfg.Alts {
			if auid, _, _ := altVerifyCookie(a.Cookie); uid != "" && auid == uid {
				c.JSON(http.StatusBadRequest, gin.H{"error": "该小号已在账号池中"})
				return
			}
		}
		var maxID int64
		for _, a := range cfg.Alts {
			if a.ID > maxID {
				maxID = a.ID
			}
		}
		name := nick
		if name == "" {
			name = "小号#" + fmt.Sprint(maxID+1)
		}
		cfg.Alts = append(cfg.Alts, playbackAlt{
			ID: maxID + 1, Name: name, Cookie: cookie, Enabled: true, Nick: nick,
		})
		if err := savePlaybackCfg(cfg); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		log.Printf("[播放账号] ✓ 小号「%s」扫码加入账号池（uid=%s）", name, uid)
		c.JSON(http.StatusOK, gin.H{"status": "success", "name": name})
	default:
		c.JSON(http.StatusOK, gin.H{"status": "waiting"})
	}
}
