package api

// 媒体库封面生成（参考 CMS/MoviePilot 同类插件）：
// 按「二级分类」聚合入库记录，按选取策略取 TMDB 海报，合成带库名的封面图
// （1280×720 PNG），保存到 /data/library-covers/ 并推送为 Emby 对应媒体库的
// 主页图片。支持 cron 定时与手动触发；三种封面样式 + 随机。

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"115-station/internal/model"
	"github.com/gin-gonic/gin"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

//go:embed assets/sourcehansans.otf
var coverFontBytes []byte

var (
	coverFontOnce sync.Once
	coverFontObj  *opentype.Font
)

func loadCoverFont() *opentype.Font {
	coverFontOnce.Do(func() {
		if f, err := opentype.Parse(coverFontBytes); err == nil {
			coverFontObj = f
		} else {
			log.Printf("[封面生成] ✗ 字体解析失败: %v", err)
		}
	})
	return coverFontObj
}

type coverGenCfg struct {
	Cron      string `json:"cron"`
	Style     string `json:"style"`    // 1 2 3 random
	Strategy  string `json:"strategy"` // title release added rating
	Blacklist string `json:"blacklist"`
	Advanced  string `json:"advanced"`
}

func (h *Handler) loadCoverGenCfg() coverGenCfg {
	c := coverGenCfg{Cron: "0 0 * * *", Style: "1", Strategy: "added"}
	if v := h.Config.GetSetting("covergen"); v != "" {
		_ = json.Unmarshal([]byte(v), &c)
	}
	return c
}

func (h *Handler) saveCoverGenCfg(c coverGenCfg) error {
	b, _ := json.Marshal(c)
	return h.Config.SaveSetting("covergen", string(b))
}

// ==================== 数据聚合 ====================

type coverLib struct {
	Name      string
	Items     []model.MediaLibrary
	ItemID    string
	PosterIDs []string
}

// 参考 MoviePilot-2 的媒体库与条目图片接口：直接用服务器的库 ID，
// 避免全量同步没有整理台账、分类路径与显示库名不同导致无图或推错库。
func (h *Handler) coverEmbyLibs(cfg coverGenCfg) ([]coverLib, error) {
	base, key, ok := h.embyServerInfo()
	if !ok || key == "" {
		return nil, nil
	}
	resp, err := embyRequest(http.MethodGet, base, key, "/Library/VirtualFolders", nil, nil)
	if err != nil {
		return nil, fmt.Errorf("连接 Emby 失败，请检查服务器配置")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("读取 Emby 媒体库失败：HTTP %d", resp.StatusCode)
	}
	var folders []struct{ Name, ItemID, CollectionType string }
	if err := json.NewDecoder(resp.Body).Decode(&folders); err != nil {
		return nil, fmt.Errorf("Emby 媒体库响应无法解析")
	}
	var libs []coverLib
	for _, f := range folders {
		if f.ItemID == "" || f.Name == "" {
			continue
		}
		blocked := false
		for _, line := range strings.Split(cfg.Blacklist, "\n") {
			if strings.TrimSpace(line) == f.Name {
				blocked = true
			}
		}
		if blocked {
			continue
		}
		sortBy, order := "DateCreated", "Descending"
		switch cfg.Strategy {
		case "title":
			sortBy, order = "SortName", "Ascending"
		case "release":
			sortBy = "PremiereDate"
		case "rating":
			sortBy = "CommunityRating"
		}
		q := url.Values{"ParentId": {f.ItemID}, "Recursive": {"true"}, "IncludeItemTypes": {embyCountTypes(f.CollectionType)}, "IsVirtualItem": {"false"}, "ImageTypes": {"Primary"}, "SortBy": {sortBy}, "SortOrder": {order}, "Limit": {"9"}}
		r, err := embyRequest(http.MethodGet, base, key, "/Items", q, nil)
		if err != nil {
			return nil, fmt.Errorf("读取媒体库「%s」条目失败", f.Name)
		}
		var items struct {
			Items []struct {
				ID string `json:"Id"`
			}
		}
		err = json.NewDecoder(r.Body).Decode(&items)
		r.Body.Close()
		if r.StatusCode != 200 || err != nil {
			return nil, fmt.Errorf("读取媒体库「%s」条目失败：HTTP %d", f.Name, r.StatusCode)
		}
		lib := coverLib{Name: f.Name, ItemID: f.ItemID}
		for _, it := range items.Items {
			if it.ID != "" {
				lib.PosterIDs = append(lib.PosterIDs, it.ID)
			}
		}
		libs = append(libs, lib)
	}
	return libs, nil
}

func (h *Handler) coverEmbyPoster(id string) image.Image {
	base, key, _ := h.embyServerInfo()
	r, err := embyRequest(http.MethodGet, base, key, "/Items/"+url.PathEscape(id)+"/Images/Primary", url.Values{"MaxWidth": {"400"}}, nil)
	if err != nil {
		return nil
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return nil
	}
	im, _, _ := image.Decode(io.LimitReader(r.Body, 8<<20))
	return im
}

func (h *Handler) coverCollectLibs(cfg coverGenCfg) []coverLib {
	black := map[string]bool{}
	for _, ln := range strings.Split(cfg.Blacklist, "\n") {
		if s := strings.TrimSpace(ln); s != "" {
			black[s] = true
		}
	}
	var rows []model.MediaLibrary
	h.DB.Where("poster_path <> ''").Order("created_at ASC").Find(&rows)
	groups := map[string]*coverLib{}
	for _, r := range rows {
		if r.Category == "" || black[r.Category] {
			continue
		}
		g, ok := groups[r.Category]
		if !ok {
			g = &coverLib{Name: r.Category}
			groups[r.Category] = g
		}
		g.Items = append(g.Items, r)
	}
	var libs []coverLib
	for _, g := range groups {
		h.coverSortItems(g.Items, cfg.Strategy)
		if len(g.Items) > 9 {
			g.Items = g.Items[:9]
		}
		libs = append(libs, *g)
	}
	sort.Slice(libs, func(i, j int) bool { return libs[i].Name < libs[j].Name })
	return libs
}

func (h *Handler) coverSortItems(items []model.MediaLibrary, strategy string) {
	yearOf := func(i int) string { return items[i].Year }
	switch strategy {
	case "title":
		sort.Slice(items, func(i, j int) bool { return items[i].Title < items[j].Title })
	case "release": // 无发行日期字段，用年份近似（新→旧）
		sort.SliceStable(items, func(i, j int) bool {
			a, b := yearOf(i), yearOf(j)
			if a != b {
				return a > b
			}
			return items[i].ID > items[j].ID
		})
	case "rating":
		sort.SliceStable(items, func(i, j int) bool { return items[i].VoteAverage > items[j].VoteAverage })
	default: // added：加入日期新→旧
		sort.SliceStable(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	}
}

// ==================== 海报下载与绘制 ====================

func coverFetchPoster(path string) image.Image {
	base := strings.TrimSuffix(tmdbImageBase(), "/t/p")
	u := base + "/t/p/w300/" + strings.TrimPrefix(path, "/")
	client := &http.Client{Timeout: 10 * time.Second}
	proxyURL := getProxyURL()
	var cfg model.TmdbConfig
	if model.DB != nil && model.DB.First(&cfg).Error == nil && cfg.EnableProxy && cfg.ProxyUrl != "" {
		proxyURL = cfg.ProxyUrl
	}
	// 与刮削使用相同代理，否则刮削正常的库仍可能一张封面都下载不到。
	if proxyURL != "" {
		if proxy, err := parseProxyURL(proxyURL); err == nil {
			client.Transport = &http.Transport{Proxy: proxy}
			defer client.CloseIdleConnections()
		}
	}
	resp, err := client.Get(u)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	im, _, err := image.Decode(resp.Body)
	return im
}

// coverDrawPoster 等比放大/缩小并居中裁切填充目标矩形（cover 模式）
func coverDrawPoster(dst draw.Image, src image.Image, rect image.Rectangle) {
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	rw, rh := rect.Dx(), rect.Dy()
	if sw <= 0 || sh <= 0 || rw <= 0 || rh <= 0 {
		return
	}
	scale := math.Max(float64(rw)/float64(sw), float64(rh)/float64(sh))
	dw, dh := int(float64(sw)*scale)+1, int(float64(sh)*scale)+1
	scaled := image.NewRGBA(image.Rect(0, 0, dw, dh))
	xdraw.ApproxBiLinear.Scale(scaled, scaled.Bounds(), src, src.Bounds(), draw.Src, nil)
	ox := (dw - rw) / 2
	oy := (dh - rh) / 2
	if ox < 0 {
		ox = 0
	}
	if oy < 0 {
		oy = 0
	}
	draw.Draw(dst, rect, scaled, image.Pt(ox, oy), draw.Src)
	// 细白描边提升层次
	bd := color.RGBA{255, 255, 255, 90}
	for x := rect.Min.X; x < rect.Max.X; x++ {
		dst.Set(x, rect.Min.Y, bd)
		dst.Set(x, rect.Max.Y-1, bd)
	}
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		dst.Set(rect.Min.X, y, bd)
		dst.Set(rect.Max.X-1, y, bd)
	}
}

func coverFace(size float64) font.Face {
	f := loadCoverFont()
	if f == nil {
		return nil
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72})
	if err != nil {
		return nil
	}
	return face
}

func coverText(dst draw.Image, text string, size float64, x, y int, c color.Color) {
	face := coverFace(size)
	if face == nil {
		return
	}
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: face, Dot: fixed.P(x, y)}
	d.DrawString(text)
}

func coverTextWidth(text string, size float64) int {
	face := coverFace(size)
	if face == nil {
		return 0
	}
	d := &font.Drawer{Face: face}
	return int(d.MeasureString(text) >> 6)
}

func coverEnName(cn string) string {
	switch {
	case strings.Contains(cn, "动漫"), strings.Contains(cn, "动画"):
		return "ANIME"
	case strings.Contains(cn, "纪录"):
		return "DOCUMENTARY"
	case strings.Contains(cn, "综艺"):
		return "VARIETY"
	case strings.Contains(cn, "剧集"), strings.Contains(cn, "剧"):
		return "TV SERIES"
	case strings.Contains(cn, "电影"), strings.Contains(cn, "影"):
		return "MOVIE"
	}
	return "MEDIA LIBRARY"
}

// coverSpaced 字母间隔排版（C N   M O V I E）
func coverSpaced(en string) string {
	var b strings.Builder
	for i, r := range en {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return strings.ReplaceAll(b.String(), "   ", "    ")
}

var coverPalette = []string{"#e74c3c", "#8e44ad", "#2980b9", "#16a085", "#e67e22", "#34495e", "#d35400", "#27ae60"}

func coverHash(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

func coverHexColor(hex string) color.RGBA {
	v, _ := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	return color.RGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 255}
}

// ==================== 三种样式（1280×720） ====================

func coverStyle1(img *image.RGBA, name string, posters []image.Image) {
	draw.Draw(img, img.Bounds(), &image.Uniform{coverHexColor(coverPalette[coverHash(name)%uint32(len(coverPalette))])}, image.Point{}, draw.Src)
	// 斜向阶梯海报
	x, y := 560, 40
	for i, p := range posters {
		if i >= 5 {
			break
		}
		coverDrawPoster(img, p, image.Rect(x, y, x+260, y+390))
		x += 130
		y += 75
	}
	// 左下：竖条 + 中文名 + 英文间隔字幕
	draw.Draw(img, image.Rect(90, 440, 102, 600), &image.Uniform{color.RGBA{255, 255, 255, 230}}, image.Point{}, draw.Src)
	coverText(img, name, 92, 122, 566, color.White)
	en := coverSpaced(coverEnName(name))
	coverText(img, en, 34, 124, 634, color.RGBA{255, 255, 255, 200})
}

func coverStyle2(img *image.RGBA, name string, posters []image.Image) {
	// 深色底 + 顶部微亮横带
	draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{16, 24, 34, 255}}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(0, 0, 1280, 8), &image.Uniform{coverHexColor(coverPalette[coverHash(name)%uint32(len(coverPalette))])}, image.Point{}, draw.Src)
	coverText(img, name, 84, 80, 140, color.White)
	coverText(img, coverSpaced(coverEnName(name)), 30, 82, 196, color.RGBA{255, 255, 255, 170})
	// 底部海报横排（最多 5 张）
	x, y, w, h := 80, 720-330-60, 212, 318
	for i, p := range posters {
		if i >= 5 {
			break
		}
		coverDrawPoster(img, p, image.Rect(x+i*(w+14), y, x+i*(w+14)+w, y+h))
	}
}

func coverStyle3(img *image.RGBA, name string, posters []image.Image) {
	draw.Draw(img, img.Bounds(), &image.Uniform{coverHexColor(coverPalette[(coverHash(name)+3)%uint32(len(coverPalette))])}, image.Point{}, draw.Src)
	// 右侧大图（3 张叠放错位营造厚度）
	if len(posters) > 0 {
		draw.Draw(img, image.Rect(806-14, 30, 1280-14, 720), &image.Uniform{color.RGBA{0, 0, 0, 70}}, image.Point{}, draw.Src)
		coverDrawPoster(img, posters[0], image.Rect(792, 16, 1266, 706))
	}
	draw.Draw(img, image.Rect(80, 300, 240, 308), &image.Uniform{color.RGBA{255, 255, 255, 220}}, image.Point{}, draw.Src)
	coverText(img, name, 88, 80, 420, color.White)
	coverText(img, coverSpaced(coverEnName(name)), 32, 82, 478, color.RGBA{255, 255, 255, 190})
}

// coverRenderWith 按指定样式渲染（1/2/3；random 或空 = 按库名哈希随机）
func (h *Handler) coverRenderWith(style, name string, posters []image.Image) ([]byte, error) {
	if style == "" || style == "random" {
		style = strconv.Itoa(1 + int(coverHash(name))%3)
	}
	img := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	switch style {
	case "2":
		coverStyle2(img, name, posters)
	case "3":
		coverStyle3(img, name, posters)
	default:
		coverStyle1(img, name, posters)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// coverRender 按用户配置的样式渲染
func (h *Handler) coverRender(name string, posters []image.Image) ([]byte, error) {
	cfg := h.loadCoverGenCfg()
	return h.coverRenderWith(cfg.Style, name, posters)
}

func coverOutDir(dataDir string) string {
	return filepath.Join(dataDir, "library-covers")
}

var coverNameRe = regexp.MustCompile(`[^\w\p{Han}]+`)

func coverSafeName(name string) string {
	return strings.Trim(coverNameRe.ReplaceAllString(name, "_"), "_")
}

// coverPushEmby 把封面推送为 Emby 同名媒体库的主页图片（未配置 Emby 时静默跳过）
func (h *Handler) coverPushEmby(name, itemID string, pngData []byte) error {
	base, apiKey, ok := h.embyServerInfo()
	if !ok || apiKey == "" {
		return nil
	}
	// 媒体库列表 → 名称匹配 ItemId（解析形态见 embyVirtualFolderIds 的注释，
	// 这里曾经自己抄过一份，两份对同一个端点的解析形状还不一样）
	if itemID == "" {
		itemID = embyVirtualFolderIds(base, apiKey)[name]
	}
	if itemID == "" {
		log.Printf("[封面生成] ○ Emby 中未找到同名媒体库「%s」，跳过推送", name)
		return fmt.Errorf("未找到同名 Emby 媒体库")
	}
	// ⚠️ **图片要 base64 再发**：Emby / Jellyfin 的 POST /Items/{Id}/Images/{Type}
	// 是把整个请求体当文本读进去再 Convert.FromBase64String 的，
	// 直接 POST 原始 PNG 字节在服务端解码就会炸（此前一直这么发，推送必失败）。
	// Emby Web 自己上传封面走的也是 FileReader.readAsDataURL 去掉头部的 base64
	body := []byte(base64.StdEncoding.EncodeToString(pngData))
	req, err := http.NewRequest(http.MethodPost,
		base+"/Items/"+itemID+"/Images/Primary?api_key="+url.QueryEscape(apiKey),
		bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("无法创建封面上传请求")
	}
	req.Header.Set("Content-Type", "image/png")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		log.Printf("[封面生成] ✗ Emby 推送「%s」失败: %v", name, err)
		return fmt.Errorf("连接 Emby 失败")
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("[封面生成] ✗ Emby 推送「%s」失败: HTTP %d", name, resp.StatusCode)
		return fmt.Errorf("上传失败：HTTP %d", resp.StatusCode)
	}
	log.Printf("[封面生成] ✓ 已推送 Emby 媒体库「%s」封面", name)
	return nil
}

// runCoverGen 生成全部媒体库封面；返回（生成数、库名列表、跳过的库名、错误）
func (h *Handler) runCoverGen() (int, []string, []string, error) {
	if !coverRunMu.TryLock() {
		return 0, nil, nil, fmt.Errorf("封面生成正在运行，请等待完成")
	}
	defer coverRunMu.Unlock()
	cfg := h.loadCoverGenCfg()
	libs, err := h.coverEmbyLibs(cfg)
	if err != nil {
		return 0, nil, nil, err
	}
	if _, key, ok := h.embyServerInfo(); !ok || key == "" {
		libs = h.coverCollectLibs(cfg)
	}
	if len(libs) == 0 {
		return 0, nil, nil, fmt.Errorf("没有可用的媒体库分类（先完成整理入库，或检查黑名单）")
	}
	outDir := coverOutDir(h.Config.DataDir)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return 0, nil, nil, fmt.Errorf("创建封面目录失败：%w", err)
	}
	done, skipped := []string{}, []string{}
	for _, lib := range libs {
		var imgs []image.Image
		for _, id := range lib.PosterIDs {
			if len(imgs) >= 5 {
				break
			}
			if im := h.coverEmbyPoster(id); im != nil {
				imgs = append(imgs, im)
			}
		}
		for _, it := range lib.Items {
			if len(imgs) >= 5 {
				break
			}
			if im := coverFetchPoster(it.PosterPath); im != nil {
				imgs = append(imgs, im)
			}
		}
		if len(imgs) == 0 {
			log.Printf("[封面生成] ○ %s：没有可用海报，跳过", lib.Name)
			skipped = append(skipped, lib.Name+"：没有可用海报，请检查图片与网络配置")
			continue
		}
		data, err := h.coverRender(lib.Name, imgs)
		if err != nil {
			skipped = append(skipped, lib.Name+"：渲染失败")
			continue
		}
		if err := os.WriteFile(filepath.Join(outDir, coverSafeName(lib.Name)+".png"), data, 0o644); err != nil {
			skipped = append(skipped, lib.Name+"：保存失败："+err.Error())
			continue
		}
		if err := h.coverPushEmby(lib.Name, lib.ItemID, data); err != nil {
			skipped = append(skipped, lib.Name+"：本地已生成，但 "+err.Error())
		}
		done = append(done, lib.Name)
	}
	if len(done) == 0 {
		return 0, done, skipped, fmt.Errorf("未生成任何封面：%s", strings.Join(skipped, "；"))
	}
	return len(done), done, skipped, nil
}

// ==================== 调度与处理器 ====================

var coverGenLastRun string
var coverRunMu sync.Mutex

func StartCoverGenScheduler(h *Handler) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
			case <-stopCh:
				return
			}
			cfg := h.loadCoverGenCfg()
			if cfg.Cron == "" || !CronMatch(cfg.Cron, time.Now()) {
				continue
			}
			key := time.Now().Format("2006-01-02 15:04")
			if coverGenLastRun == key {
				continue
			}
			coverGenLastRun = key
			go func() {
				defer func() { recover() }()
				n, names, skipped, err := h.runCoverGen()
				if err != nil {
					NotifyMessage("", "▣ 媒体库封面生成失败: "+err.Error())
					return
				}
				NotifyMessage("", coverGenResultText(n, names, skipped))
			}()
		}
	}()
	log.Println("[封面生成] 调度器已启动")
}

// CoverGenGetConfig GET /covergen/config
func (h *Handler) CoverGenGetConfig(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"data": h.loadCoverGenCfg()})
}

// CoverGenSaveConfig POST /covergen/config
func (h *Handler) CoverGenSaveConfig(c *gin.Context) {
	var req coverGenCfg
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if err := h.saveCoverGenCfg(req); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败：" + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

// CoverGenRun POST /covergen/run
func (h *Handler) CoverGenRun(c *gin.Context) {
	n, names, skipped, err := h.runCoverGen()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": coverGenResultText(n, names, skipped), "warnings": skipped})
}

// coverGenResultText 生成结果文案（跳过的库点名原因）
func coverGenResultText(n int, names, skipped []string) string {
	b := fmt.Sprintf("▣ 媒体库封面已生成：%d 个\n%s", n, strings.Join(names, "、"))
	if len(skipped) > 0 {
		b += fmt.Sprintf("\n\n○ %d 项未完成：\n%s", len(skipped), strings.Join(skipped, "；"))
	}
	return b
}

// CoverGenList GET /covergen/list：已生成的封面清单
func (h *Handler) CoverGenList(c *gin.Context) {
	entries, err := os.ReadDir(coverOutDir(h.Config.DataDir))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"data": []gin.H{}})
		return
	}
	var out []gin.H
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".png") {
			continue
		}
		info, err1 := e.Info()
		t := ""
		if err1 == nil {
			t = info.ModTime().Format("01-02 15:04")
		}
		out = append(out, gin.H{"name": strings.TrimSuffix(e.Name(), ".png"), "time": t})
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

// CoverGenPreview GET /covergen/preview?name=xxx：返回生成的封面 PNG
func (h *Handler) CoverGenPreview(c *gin.Context) {
	name := coverSafeName(strings.TrimSpace(c.Query("name")))
	if name == "" {
		c.String(http.StatusBadRequest, "bad name")
		return
	}
	p := filepath.Join(coverOutDir(h.Config.DataDir), name+".png")
	if _, err := os.Stat(p); err != nil {
		c.String(http.StatusNotFound, "not found")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.File(p)
}
