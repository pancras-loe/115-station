package api

// 媒体库海报生成器。设计参考 MoviePilot MediaCoverGenerator，但保留本站的
// 静态 PNG、Emby 上传和本地台账降级契约，不复制其插件框架。

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
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
	coverRunMu    sync.Mutex
	coverLastRun  string
)

type coverGenCfg struct {
	Enabled     bool    `json:"enabled"`
	Cron        string  `json:"cron"`
	Style       string  `json:"style"`
	Strategy    string  `json:"strategy"`
	Include     string  `json:"include"`
	Blacklist   string  `json:"blacklist"`
	Titles      string  `json:"titles"`
	Resolution  string  `json:"resolution"`
	PosterCount int     `json:"poster_count"`
	Background  string  `json:"background"`
	CustomColor string  `json:"custom_color"`
	Blur        int     `json:"blur"`
	ColorRatio  float64 `json:"color_ratio"`
	UsePrimary  bool    `json:"use_primary"`
	Advanced    string  `json:"advanced,omitempty"`
}

func defaultCoverGenCfg() coverGenCfg {
	return coverGenCfg{Enabled: true, Cron: "0 0 * * *", Style: "static_1", Strategy: "added", Resolution: "720p", PosterCount: 6, Background: "auto", Blur: 36, ColorRatio: .72, UsePrimary: true}
}

func normalizeCoverGenCfg(c coverGenCfg) coverGenCfg {
	if c.Cron == "" {
		c.Cron = "0 0 * * *"
	}
	switch c.Style {
	case "1", "2", "3", "4":
		c.Style = "static_" + c.Style
	}
	if !map[string]bool{"static_1": true, "static_2": true, "static_3": true, "static_4": true, "random": true}[c.Style] {
		c.Style = "static_1"
	}
	if !map[string]bool{"added": true, "release": true, "title": true, "rating": true}[c.Strategy] {
		c.Strategy = "added"
	}
	if !map[string]bool{"480p": true, "720p": true, "1080p": true}[c.Resolution] {
		c.Resolution = "720p"
	}
	if c.PosterCount < 1 || c.PosterCount > 12 {
		c.PosterCount = 6
	}
	if !map[string]bool{"auto": true, "poster": true, "custom": true}[c.Background] {
		c.Background = "auto"
	}
	if c.Blur < 0 || c.Blur > 100 {
		c.Blur = 36
	}
	if c.ColorRatio < 0 || c.ColorRatio > 1 {
		c.ColorRatio = .72
	}
	if c.CustomColor == "" {
		c.CustomColor = "#263445"
	}
	return c
}

func (h *Handler) loadCoverGenCfg() coverGenCfg {
	c := defaultCoverGenCfg()
	if v := h.Config.GetSetting("covergen"); v != "" {
		_ = json.Unmarshal([]byte(v), &c)
	}
	return normalizeCoverGenCfg(c)
}

func (h *Handler) saveCoverGenCfg(c coverGenCfg) error {
	b, _ := json.Marshal(normalizeCoverGenCfg(c))
	return h.Config.SaveSetting("covergen", string(b))
}

type coverLib struct {
	Name, ItemID string
	Items        []model.MediaLibrary
	PosterIDs    []string
}

func coverLines(s string) map[string]bool {
	out := map[string]bool{}
	for _, v := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' }) {
		if v = strings.TrimSpace(v); v != "" {
			out[v] = true
		}
	}
	return out
}

func coverAllowed(name string, cfg coverGenCfg) bool {
	include, exclude := coverLines(cfg.Include), coverLines(cfg.Blacklist)
	return !exclude[name] && (len(include) == 0 || include[name])
}

// titles 每行：媒体库名=中文标题|英文副标题。
func coverTitle(name, raw string) (string, string) {
	zh, en := name, coverEnglishName(name)
	for _, line := range strings.Split(raw, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) != name {
			continue
		}
		mapped := strings.SplitN(strings.TrimSpace(parts[1]), "|", 2)
		if v := strings.TrimSpace(mapped[0]); v != "" {
			zh = v
		}
		if len(mapped) == 2 {
			if v := strings.TrimSpace(mapped[1]); v != "" {
				en = v
			}
		}
		break
	}
	return zh, en
}

func (h *Handler) coverEmbyLibs(cfg coverGenCfg) ([]coverLib, error) {
	base, key, ok := h.embyServerInfo()
	if !ok || key == "" {
		return nil, nil
	}
	resp, err := embyRequest(http.MethodGet, base, key, "/Library/VirtualFolders", nil, nil)
	if err != nil {
		return nil, fmt.Errorf("连接 Emby 失败：请检查服务器配置")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("获取 Emby 媒体库失败：HTTP %d", resp.StatusCode)
	}
	var folders []struct{ Name, ItemID, CollectionType string }
	if json.NewDecoder(resp.Body).Decode(&folders) != nil {
		return nil, fmt.Errorf("Emby 媒体库响应无法解析")
	}
	libs := []coverLib{}
	for _, f := range folders {
		if f.Name == "" || f.ItemID == "" || !coverAllowed(f.Name, cfg) {
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
		q := url.Values{"ParentId": {f.ItemID}, "Recursive": {"true"}, "IncludeItemTypes": {embyCountTypes(f.CollectionType)}, "IsVirtualItem": {"false"}, "ImageTypes": {"Primary"}, "SortBy": {sortBy}, "SortOrder": {order}, "Limit": {strconv.Itoa(cfg.PosterCount)}}
		r, err := embyRequest(http.MethodGet, base, key, "/Items", q, nil)
		if err != nil {
			return nil, fmt.Errorf("获取媒体库《%s》项目失败", f.Name)
		}
		var payload struct {
			Items []struct {
				ID string `json:"Id"`
			} `json:"Items"`
		}
		decodeErr := json.NewDecoder(r.Body).Decode(&payload)
		r.Body.Close()
		if r.StatusCode != http.StatusOK || decodeErr != nil {
			return nil, fmt.Errorf("获取媒体库《%s》项目失败：HTTP %d", f.Name, r.StatusCode)
		}
		lib := coverLib{Name: f.Name, ItemID: f.ItemID}
		for _, item := range payload.Items {
			if item.ID != "" {
				lib.PosterIDs = append(lib.PosterIDs, item.ID)
			}
		}
		libs = append(libs, lib)
	}
	return libs, nil
}

func (h *Handler) coverEmbyPoster(id string) image.Image {
	base, key, _ := h.embyServerInfo()
	r, err := embyRequest(http.MethodGet, base, key, "/Items/"+url.PathEscape(id)+"/Images/Primary", url.Values{"MaxWidth": {"500"}}, nil)
	if err != nil {
		return nil
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return nil
	}
	im, _, _ := image.Decode(io.LimitReader(r.Body, 12<<20))
	return im
}

func coverSortItems(items []model.MediaLibrary, strategy string) {
	switch strategy {
	case "title":
		sort.SliceStable(items, func(i, j int) bool { return items[i].Title < items[j].Title })
	case "release":
		sort.SliceStable(items, func(i, j int) bool { return items[i].Year > items[j].Year })
	case "rating":
		sort.SliceStable(items, func(i, j int) bool { return items[i].VoteAverage > items[j].VoteAverage })
	default:
		sort.SliceStable(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	}
}

// 保留旧内部调用签名，避免同包扩展和既有测试因重构失效。
func (h *Handler) coverSortItems(items []model.MediaLibrary, strategy string) {
	coverSortItems(items, strategy)
}

func (h *Handler) coverCollectLibs(cfg coverGenCfg) []coverLib {
	var rows []model.MediaLibrary
	h.DB.Where("poster_path <> ''").Order("created_at ASC").Find(&rows)
	groups := map[string]*coverLib{}
	for _, row := range rows {
		if row.Category == "" || !coverAllowed(row.Category, cfg) {
			continue
		}
		if groups[row.Category] == nil {
			groups[row.Category] = &coverLib{Name: row.Category}
		}
		groups[row.Category].Items = append(groups[row.Category].Items, row)
	}
	libs := []coverLib{}
	for _, lib := range groups {
		coverSortItems(lib.Items, cfg.Strategy)
		if len(lib.Items) > cfg.PosterCount {
			lib.Items = lib.Items[:cfg.PosterCount]
		}
		libs = append(libs, *lib)
	}
	sort.Slice(libs, func(i, j int) bool { return libs[i].Name < libs[j].Name })
	return libs
}

func coverFetchPoster(path string) image.Image {
	base := strings.TrimSuffix(tmdbImageBase(), "/t/p")
	client := &http.Client{Timeout: 12 * time.Second}
	proxyURL := getProxyURL()
	var tc model.TmdbConfig
	if model.DB != nil && model.DB.First(&tc).Error == nil && tc.EnableProxy && tc.ProxyUrl != "" {
		proxyURL = tc.ProxyUrl
	}
	if proxyURL != "" {
		if proxy, err := parseProxyURL(proxyURL); err == nil {
			client.Transport = &http.Transport{Proxy: proxy}
			defer client.CloseIdleConnections()
		}
	}
	resp, err := client.Get(base + "/t/p/w500/" + strings.TrimPrefix(path, "/"))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	im, _, _ := image.Decode(io.LimitReader(resp.Body, 12<<20))
	return im
}

func coverResolution(v string) (int, int) {
	switch v {
	case "480p":
		return 854, 480
	case "1080p":
		return 1920, 1080
	default:
		return 1280, 720
	}
}

func coverCrop(dst draw.Image, src image.Image, rect image.Rectangle) {
	if src == nil || rect.Empty() {
		return
	}
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	scale := math.Max(float64(rect.Dx())/float64(sw), float64(rect.Dy())/float64(sh))
	dw, dh := int(float64(sw)*scale)+1, int(float64(sh)*scale)+1
	scaled := image.NewRGBA(image.Rect(0, 0, dw, dh))
	xdraw.CatmullRom.Scale(scaled, scaled.Bounds(), src, src.Bounds(), draw.Src, nil)
	draw.Draw(dst, rect, scaled, image.Pt(max(0, (dw-rect.Dx())/2), max(0, (dh-rect.Dy())/2)), draw.Src)
}

func coverFace(size float64) font.Face {
	coverFontOnce.Do(func() { coverFontObj, _ = opentype.Parse(coverFontBytes) })
	if coverFontObj == nil {
		return nil
	}
	face, _ := opentype.NewFace(coverFontObj, &opentype.FaceOptions{Size: size, DPI: 72})
	return face
}
func coverDrawText(dst draw.Image, s string, size float64, x, y int, c color.Color) {
	face := coverFace(size)
	if face == nil {
		return
	}
	(&font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: face, Dot: fixed.P(x, y)}).DrawString(s)
}
func coverTextWidth(s string, size float64) int {
	face := coverFace(size)
	if face == nil {
		return 0
	}
	return int((&font.Drawer{Face: face}).MeasureString(s) >> 6)
}

func coverEnglishName(name string) string {
	switch {
	case strings.Contains(name, "动漫"), strings.Contains(name, "动画"):
		return "ANIMATION"
	case strings.Contains(name, "纪录"):
		return "DOCUMENTARY"
	case strings.Contains(name, "综艺"):
		return "VARIETY SHOW"
	case strings.Contains(name, "剧集"), strings.Contains(name, "电视剧"):
		return "TV SERIES"
	case strings.Contains(name, "电影"):
		return "MOVIES"
	}
	return "MEDIA LIBRARY"
}
func coverSpaced(s string) string {
	r := []rune(strings.ReplaceAll(s, " ", "  "))
	parts := make([]string, len(r))
	for i, v := range r {
		parts[i] = string(v)
	}
	return strings.Join(parts, " ")
}

var coverPalette = []color.RGBA{{222, 92, 116, 255}, {111, 100, 180, 255}, {56, 137, 180, 255}, {53, 153, 134, 255}, {217, 143, 73, 255}, {72, 92, 111, 255}}

func coverHash(s string) uint32 {
	h := uint32(2166136261)
	for i := range s {
		h = (h ^ uint32(s[i])) * 16777619
	}
	return h
}

var coverHexRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func coverParseHex(s string) (color.RGBA, bool) {
	if !coverHexRe.MatchString(s) {
		return color.RGBA{}, false
	}
	v, e := strconv.ParseUint(s[1:], 16, 32)
	return color.RGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}, e == nil
}
func coverAverage(im image.Image) color.RGBA {
	if im == nil {
		return color.RGBA{45, 60, 75, 255}
	}
	b := im.Bounds()
	var r, g, bl, n uint64
	sx, sy := max(1, b.Dx()/32), max(1, b.Dy()/32)
	for y := b.Min.Y; y < b.Max.Y; y += sy {
		for x := b.Min.X; x < b.Max.X; x += sx {
			rr, gg, bb, _ := im.At(x, y).RGBA()
			r += uint64(rr >> 8)
			g += uint64(gg >> 8)
			bl += uint64(bb >> 8)
			n++
		}
	}
	return color.RGBA{uint8(r / n), uint8(g / n), uint8(bl / n), 255}
}
func coverBackground(cfg coverGenCfg, name string, posters []image.Image) color.RGBA {
	base := coverPalette[coverHash(name)%uint32(len(coverPalette))]
	if cfg.Background == "custom" {
		if c, ok := coverParseHex(cfg.CustomColor); ok {
			base = c
		}
	} else if cfg.Background == "poster" && len(posters) > 0 {
		base = coverAverage(posters[0])
	}
	r := cfg.ColorRatio
	return color.RGBA{uint8(float64(base.R)*r + 20*(1-r)), uint8(float64(base.G)*r + 25*(1-r)), uint8(float64(base.B)*r + 32*(1-r)), 255}
}

func coverRender(cfg coverGenCfg, name string, posters []image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, coverCompose(cfg, name, posters)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// 半透明色一律用 color.NRGBA：color.RGBA 是预乘 alpha，写成 {255,255,255,205}
// 这种「白色 80%」是非法值，合成出来文字带彩边、叠色发黑、沉浸背景的遮罩把海报整个盖死。
func coverCompose(cfg coverGenCfg, name string, posters []image.Image) *image.RGBA {
	w, h := coverResolution(cfg.Resolution)
	style := cfg.Style
	if style == "random" {
		style = fmt.Sprintf("static_%d", 1+coverHash(name)%4)
	}
	zh, en := coverTitle(name, cfg.Titles)
	bg := coverBackground(cfg, name, posters)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	sx, sy := float64(w)/1280, float64(h)/720
	text := func(s string, size float64, x, y int, c color.Color) {
		coverDrawText(img, s, size*sy, int(float64(x)*sx), int(float64(y)*sy), c)
	}
	switch style {
	case "static_2":
		if len(posters) > 0 {
			coverCrop(img, posters[0], image.Rect(w*43/100, 0, w, h))
		}
		draw.Draw(img, image.Rect(0, 0, w*58/100, h), image.NewUniform(bg), image.Point{}, draw.Src)
		text(zh, 88, 72, 326, color.White)
		text(en, 31, 76, 382, color.NRGBA{255, 255, 255, 205})
	case "static_3":
		pw, ph := w/5, h*49/100
		for i, p := range posters {
			if i >= 6 {
				break
			}
			col, row := i%3, i/3
			x, y := w*48/100+col*(pw*4/5), -ph/5+row*(ph*4/5)
			coverCrop(img, p, image.Rect(x, y, x+pw, y+ph))
		}
		text(zh, 82, 68, 335, color.White)
		text(en, 29, 72, 390, color.NRGBA{255, 255, 255, 205})
	case "static_4":
		if len(posters) > 0 {
			coverCrop(img, posters[0], img.Bounds())
		}
		draw.Draw(img, img.Bounds(), image.NewUniform(color.NRGBA{bg.R, bg.G, bg.B, uint8(min(245, 150+cfg.Blur))}), image.Point{}, draw.Over)
		zw := coverTextWidth(zh, 96*sy)
		text(zh, 96, int((float64(w-zw)/2)/sx), 350, color.NRGBA{255, 255, 255, 240})
		ew := coverTextWidth(en, 32*sy)
		text(en, 32, int((float64(w-ew)/2)/sx), 414, color.NRGBA{255, 255, 255, 220})
	default:
		pw, ph := int(270*sx), int(405*sy)
		for i, p := range posters {
			if i >= 5 {
				break
			}
			x, y := int((590+float64(i)*125)*sx), int((30+float64(i)*60)*sy)
			coverCrop(img, p, image.Rect(x, y, x+pw, y+ph))
		}
		draw.Draw(img, image.Rect(int(76*sx), int(430*sy), int(86*sx), int(605*sy)), image.NewUniform(color.NRGBA{255, 255, 255, 230}), image.Point{}, draw.Over)
		text(zh, 88, 106, 550, color.White)
		text(coverSpaced(en), 29, 109, 610, color.NRGBA{255, 255, 255, 210})
	}
	return img
}

func (h *Handler) coverRenderWith(style, name string, posters []image.Image) ([]byte, error) {
	cfg := defaultCoverGenCfg()
	cfg.Style = style
	return coverRender(normalizeCoverGenCfg(cfg), name, posters)
}

func coverOutDir(dataDir string) string { return filepath.Join(dataDir, "library-covers") }

var coverNameRe = regexp.MustCompile(`[^\w\p{Han}]+`)

func coverSafeName(name string) string {
	return strings.Trim(coverNameRe.ReplaceAllString(name, "_"), "_")
}

func (h *Handler) coverPushEmby(name, itemID string, data []byte) error {
	base, key, ok := h.embyServerInfo()
	if !ok || key == "" {
		return nil
	}
	if itemID == "" {
		itemID = embyVirtualFolderIds(base, key)[name]
	}
	if itemID == "" {
		return fmt.Errorf("未找到同名 Emby 媒体库")
	}
	req, err := http.NewRequest(http.MethodPost, base+"/Items/"+url.PathEscape(itemID)+"/Images/Primary?api_key="+url.QueryEscape(key), bytes.NewReader([]byte(base64.StdEncoding.EncodeToString(data))))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "image/png")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("连接 Emby 失败")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("上传失败：HTTP %d", resp.StatusCode)
	}
	log.Printf("[媒体库海报] ✓ 已推送 Emby 媒体库《%s》", name)
	return nil
}

// coverLibraries 按配置列出要生成的媒体库：配置了 Emby 用 Emby 的库，否则退回本地整理台账。
func (h *Handler) coverLibraries(cfg coverGenCfg) ([]coverLib, error) {
	if _, key, ok := h.embyServerInfo(); !ok || key == "" {
		return h.coverCollectLibs(cfg), nil
	}
	return h.coverEmbyLibs(cfg)
}

func (h *Handler) coverPosters(cfg coverGenCfg, lib coverLib) []image.Image {
	imgs := []image.Image{}
	for _, id := range lib.PosterIDs {
		if len(imgs) >= cfg.PosterCount {
			break
		}
		if im := h.coverEmbyPoster(id); im != nil {
			imgs = append(imgs, im)
		}
	}
	for _, item := range lib.Items {
		if len(imgs) >= cfg.PosterCount {
			break
		}
		if im := coverFetchPoster(item.PosterPath); im != nil {
			imgs = append(imgs, im)
		}
	}
	return imgs
}

func (h *Handler) runCoverGen() (int, []string, []string, error) {
	if !coverRunMu.TryLock() {
		return 0, nil, nil, fmt.Errorf("媒体库海报正在生成，请稍候")
	}
	defer coverRunMu.Unlock()
	cfg := h.loadCoverGenCfg()
	libs, err := h.coverLibraries(cfg)
	if err != nil {
		return 0, nil, nil, err
	}
	if len(libs) == 0 {
		return 0, nil, nil, fmt.Errorf("没有可用媒体库，请检查包含/排除设置与 Emby 配置")
	}
	outDir := coverOutDir(h.Config.DataDir)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return 0, nil, nil, fmt.Errorf("创建海报目录失败：%w", err)
	}
	done, skipped := []string{}, []string{}
	for _, lib := range libs {
		imgs := h.coverPosters(cfg, lib)
		if len(imgs) == 0 {
			skipped = append(skipped, lib.Name+"：没有可用海报")
			continue
		}
		data, err := coverRender(cfg, lib.Name, imgs)
		if err != nil {
			skipped = append(skipped, lib.Name+"：渲染失败")
			continue
		}
		if err := os.WriteFile(filepath.Join(outDir, coverSafeName(lib.Name)+".png"), data, 0o644); err != nil {
			skipped = append(skipped, lib.Name+"：保存失败")
			continue
		}
		if err := h.coverPushEmby(lib.Name, lib.ItemID, data); err != nil {
			skipped = append(skipped, lib.Name+"："+err.Error())
		}
		done = append(done, lib.Name)
	}
	if len(done) == 0 {
		return 0, done, skipped, fmt.Errorf("未生成任何海报：%s", strings.Join(skipped, "；"))
	}
	return len(done), done, skipped, nil
}

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
			if !cfg.Enabled || cfg.Cron == "" || !CronMatch(cfg.Cron, time.Now()) {
				continue
			}
			key := time.Now().Format("2006-01-02 15:04")
			if coverLastRun == key {
				continue
			}
			coverLastRun = key
			go func() {
				defer func() { _ = recover() }()
				n, names, skipped, err := h.runCoverGen()
				if err != nil {
					NotifyMessage("", "✗ 媒体库海报生成失败："+err.Error())
					return
				}
				NotifyMessage("", coverGenResultText(n, names, skipped))
			}()
		}
	}()
	log.Println("[媒体库海报] ✓ 定时任务已启动")
}
func (h *Handler) CoverGenGetConfig(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"data": h.loadCoverGenCfg()})
}
func (h *Handler) CoverGenSaveConfig(c *gin.Context) {
	var req coverGenCfg
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数无效"})
		return
	}
	if req.Background == "custom" {
		if _, ok := coverParseHex(req.CustomColor); !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "自定义背景色必须是 #RRGGBB"})
			return
		}
	}
	if err := h.saveCoverGenCfg(req); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败：" + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}
func (h *Handler) CoverGenRun(c *gin.Context) {
	n, names, skipped, err := h.runCoverGen()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": coverGenResultText(n, names, skipped), "warnings": skipped})
}
func coverGenResultText(n int, names, skipped []string) string {
	b := fmt.Sprintf("✓ 媒体库海报已生成：%d 个\n%s", n, strings.Join(names, "、"))
	if len(skipped) > 0 {
		b += fmt.Sprintf("\n\n○ %d 个未完成：\n%s", len(skipped), strings.Join(skipped, "；"))
	}
	return b
}
func (h *Handler) CoverGenList(c *gin.Context) {
	entries, err := os.ReadDir(coverOutDir(h.Config.DataDir))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"data": []gin.H{}})
		return
	}
	out := []gin.H{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".png" {
			continue
		}
		info, _ := entry.Info()
		stamp := ""
		if info != nil {
			stamp = info.ModTime().Format("01-02 15:04")
		}
		out = append(out, gin.H{"name": strings.TrimSuffix(entry.Name(), ".png"), "time": stamp})
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}
func (h *Handler) CoverGenClean(c *gin.Context) {
	dir := filepath.Clean(coverOutDir(h.Config.DataDir))
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取缓存失败"})
		return
	}
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".png" {
			continue
		}
		if os.Remove(filepath.Join(dir, entry.Name())) == nil {
			removed++
		}
	}
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("已清理 %d 张海报", removed)})
}
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

// ============ 配置弹窗里的预览 ============
//
// 预览只出图、不落盘、不推 Emby。两种来源：
//   - 样式缩略图用合成的占位海报，不走网络，打开弹窗就能看到每种样式的构图；
//   - 「用真实海报预览」取某个库的真实海报，按弹窗里尚未保存的配置渲染。
// 两者都用 JPEG 且锁 480p/720p：预览是给眼睛看构图和配色的，没必要传几 MB 的 1080p PNG。

var coverSampleStyles = []string{"static_1", "static_2", "static_3", "static_4"}

// coverDemoPosters 合成占位海报：竖向双色渐变 + 下方一条浅色「标题带」，
// 颜色取自 coverPalette，保证几种样式里海报之间能分得开。
func coverDemoPosters(n int) []image.Image {
	out := make([]image.Image, 0, n)
	for i := 0; i < n; i++ {
		top := coverPalette[i%len(coverPalette)]
		bot := coverPalette[(i+2)%len(coverPalette)]
		img := image.NewRGBA(image.Rect(0, 0, 200, 300))
		for y := 0; y < 300; y++ {
			t := float64(y) / 299
			c := color.RGBA{
				uint8(float64(top.R)*(1-t) + float64(bot.R)*t*.6),
				uint8(float64(top.G)*(1-t) + float64(bot.G)*t*.6),
				uint8(float64(top.B)*(1-t) + float64(bot.B)*t*.6),
				255,
			}
			draw.Draw(img, image.Rect(0, y, 200, y+1), image.NewUniform(c), image.Point{}, draw.Src)
		}
		draw.Draw(img, image.Rect(24, 232, 176, 244), image.NewUniform(color.NRGBA{255, 255, 255, 150}), image.Point{}, draw.Over)
		draw.Draw(img, image.Rect(24, 254, 130, 262), image.NewUniform(color.NRGBA{255, 255, 255, 90}), image.Point{}, draw.Over)
		out = append(out, img)
	}
	return out
}

func coverJPEGDataURL(img image.Image) (string, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		return "", err
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// 真实海报缓存：切样式、拖滑块都会重新请求预览，每次都去 Emby/TMDB 拉一遍海报太慢。
// 键带上策略与数量，这两项一改取到的海报就不同了。
type coverPosterEntry struct {
	at   time.Time
	imgs []image.Image
}

var (
	coverPosterMu    sync.Mutex
	coverPosterCache = map[string]coverPosterEntry{}
)

const coverPosterTTL = 5 * time.Minute

func (h *Handler) coverPreviewPosters(cfg coverGenCfg, lib coverLib) []image.Image {
	key := fmt.Sprintf("%s|%s|%d", lib.Name, cfg.Strategy, cfg.PosterCount)
	coverPosterMu.Lock()
	for k, e := range coverPosterCache {
		if time.Since(e.at) > coverPosterTTL {
			delete(coverPosterCache, k)
		}
	}
	e, ok := coverPosterCache[key]
	coverPosterMu.Unlock()
	if ok {
		return e.imgs
	}
	imgs := h.coverPosters(cfg, lib)
	if len(imgs) > 0 {
		coverPosterMu.Lock()
		coverPosterCache[key] = coverPosterEntry{at: time.Now(), imgs: imgs}
		coverPosterMu.Unlock()
	}
	return imgs
}

// CoverGenSample 渲染预览。body 是弹窗里当前（可能未保存）的配置：
//
//	{"config": {...}, "live": false}                → {"samples": {"static_1": dataURL, ...}}
//	{"config": {...}, "live": true, "library": "x"} → {"image": dataURL, "library": "x", "libraries": [...]}
func (h *Handler) CoverGenSample(c *gin.Context) {
	var req struct {
		Config  coverGenCfg `json:"config"`
		Live    bool        `json:"live"`
		Library string      `json:"library"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数无效"})
		return
	}
	cfg := normalizeCoverGenCfg(req.Config)
	if !req.Live {
		cfg.Resolution = "480p"
		name := strings.TrimSpace(req.Library)
		if name == "" {
			name = "电影"
		}
		posters := coverDemoPosters(6)
		samples := gin.H{}
		for _, style := range coverSampleStyles {
			cfg.Style = style
			u, err := coverJPEGDataURL(coverCompose(cfg, name, posters))
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "渲染失败"})
				return
			}
			samples[style] = u
		}
		c.JSON(http.StatusOK, gin.H{"samples": samples})
		return
	}

	libs, err := h.coverLibraries(cfg)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(libs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有可用媒体库，请检查包含/排除设置与 Emby 配置"})
		return
	}
	names := make([]string, 0, len(libs))
	lib := libs[0]
	for _, l := range libs {
		names = append(names, l.Name)
		if l.Name == req.Library {
			lib = l
		}
	}
	imgs := h.coverPreviewPosters(cfg, lib)
	if len(imgs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "媒体库《" + lib.Name + "》没有可用海报", "libraries": names})
		return
	}
	if cfg.Resolution == "1080p" {
		cfg.Resolution = "720p"
	}
	u, err := coverJPEGDataURL(coverCompose(cfg, lib.Name, imgs))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "渲染失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"image": u, "library": lib.Name, "libraries": names})
}
