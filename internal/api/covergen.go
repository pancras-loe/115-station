package api

// 媒体库封面生成（参考 CMS/MoviePilot 同类插件）：
// 按「二级分类」聚合入库记录，按选取策略取 TMDB 海报，合成带库名的封面图
// （1280×720 PNG），保存到 /data/library-covers/ 并推送为 Emby 对应媒体库的
// 主页图片。支持 cron 定时与手动触发；三种封面样式 + 随机。

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	"image/png"
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

	"github.com/gin-gonic/gin"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"strmhub/internal/model"
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

func (h *Handler) saveCoverGenCfg(c coverGenCfg) {
	b, _ := json.Marshal(c)
	h.Config.SaveSetting("covergen", string(b))
}

// ==================== 数据聚合 ====================

type coverLib struct {
	Name  string
	Items []model.MediaLibrary
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
	// AV 真实封面（/av:<完整URL>）：优先海报缓存，未命中直连下载
	if strings.HasPrefix(path, "/av:") {
		coverURL := strings.TrimPrefix(path, "/av:")
		if data := readPosterCache(coverURL); len(data) > 0 {
			if im, _, err := image.Decode(bytes.NewReader(data)); err == nil {
				return im
			}
		}
		resp, err := (&http.Client{Timeout: 15 * time.Second}).Get(coverURL)
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
	u := tmdbImageBase() + "/t/p/w300" + path
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(u)
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
func (h *Handler) coverPushEmby(name string, pngData []byte) {
	base, apiKey, ok := h.embyServerInfo()
	if !ok || apiKey == "" {
		return
	}
	client := &http.Client{Timeout: 15 * time.Second}
	// 媒体库列表 → 名称匹配 ItemId
	resp, err := client.Get(base + "/Library/VirtualFolders?api_key=" + url.QueryEscape(apiKey))
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var folders []struct {
		Name   string `json:"Name"`
		ItemID string `json:"ItemId"`
	}
	if json.NewDecoder(resp.Body).Decode(&folders) != nil {
		return
	}
	itemID := ""
	for _, f := range folders {
		if f.Name == name && f.ItemID != "" {
			itemID = f.ItemID
			break
		}
	}
	if itemID == "" {
		log.Printf("[封面生成] ○ Emby 中未找到同名媒体库「%s」，跳过推送", name)
		return
	}
	req, err := http.NewRequest(http.MethodPost,
		base+"/Items/"+itemID+"/Images/Primary?api_key="+url.QueryEscape(apiKey),
		bytes.NewReader(pngData))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "image/png")
	if resp2, err := client.Do(req); err == nil {
		resp2.Body.Close()
		if resp2.StatusCode >= 400 {
			log.Printf("[封面生成] ✗ Emby 推送「%s」失败: HTTP %d", name, resp2.StatusCode)
		} else {
			log.Printf("[封面生成] ✓ 已推送 Emby 媒体库「%s」封面", name)
		}
	}
}

// runCoverGen 生成全部媒体库封面；返回（生成数、库名列表、跳过的库名、错误）
func (h *Handler) runCoverGen() (int, []string, []string, error) {
	cfg := h.loadCoverGenCfg()
	libs := h.coverCollectLibs(cfg)
	if len(libs) == 0 {
		return 0, nil, nil, fmt.Errorf("没有可用的媒体库分类（先完成整理入库，或检查黑名单）")
	}
	outDir := coverOutDir(h.Config.DataDir)
	_ = os.MkdirAll(outDir, 0o777)
	done, skipped := []string{}, []string{}
	for _, lib := range libs {
		var imgs []image.Image
		for _, it := range lib.Items {
			if len(imgs) >= 5 {
				break
			}
			if im := coverFetchPoster(it.PosterPath); im != nil {
				imgs = append(imgs, im)
			}
		}
		if len(imgs) < 3 {
			log.Printf("[封面生成] ○ %s：可用海报不足 3 张，跳过", lib.Name)
			skipped = append(skipped, lib.Name)
			continue
		}
		data, err := h.coverRender(lib.Name, imgs)
		if err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(outDir, coverSafeName(lib.Name)+".png"), data, 0o644); err != nil {
			continue
		}
		h.coverPushEmby(lib.Name, data)
		done = append(done, lib.Name)
	}
	return len(done), done, skipped, nil
}

// ==================== 调度与处理器 ====================

var coverGenLastRun string

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
	h.saveCoverGenCfg(req)
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

// CoverGenRun POST /covergen/run
func (h *Handler) CoverGenRun(c *gin.Context) {
	go func() {
		defer func() { recover() }()
		n, names, skipped, err := h.runCoverGen()
		if err != nil {
			NotifyMessage("", "▣ 媒体库封面生成失败: "+err.Error())
			return
		}
		NotifyMessage("", coverGenResultText(n, names, skipped)+"\n可在「扩展功能 → 媒体库海报」预览")
	}()
	c.JSON(http.StatusOK, gin.H{"message": "封面生成已开始，完成后通知"})
}

// coverGenResultText 生成结果文案（跳过的库点名原因）
func coverGenResultText(n int, names, skipped []string) string {
	b := fmt.Sprintf("▣ 媒体库封面已生成：%d 个\n%s", n, strings.Join(names, "、"))
	if len(skipped) > 0 {
		b += fmt.Sprintf("\n\n○ 跳过 %d 个（可用海报不足 3 张，多为旧数据待回填）：\n%s", len(skipped), strings.Join(skipped, "、"))
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
