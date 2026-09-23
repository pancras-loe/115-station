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

//go:embed assets/lxgwwenkai-medium.ttf
var coverWenKaiBytes []byte

//go:embed assets/smileysans-oblique.ttf
var coverSmileyBytes []byte

//go:embed assets/zcoolxiaowei-regular.ttf
var coverXiaoWeiBytes []byte

type coverFont uint8

const (
	coverWenKai coverFont = iota
	coverSmiley
	coverXiaoWei
)

var (
	coverFontOnce [3]sync.Once
	coverFontObj  [3]*opentype.Font
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
	return coverGenCfg{Enabled: true, Cron: "0 0 * * *", Style: "editorial_c", Strategy: "added", Resolution: "720p", PosterCount: 6, Background: "auto", Blur: 36, ColorRatio: .72, UsePrimary: true}
}

func normalizeCoverGenCfg(c coverGenCfg) coverGenCfg {
	if c.Cron == "" {
		c.Cron = "0 0 * * *"
	}
	if !map[string]bool{"editorial_a": true, "editorial_b": true, "editorial_c": true, "editorial_d": true, "editorial_e": true, "editorial_f": true, "editorial_g": true, "editorial_h": true, "editorial_i": true}[c.Style] {
		c.Style = "editorial_c"
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

func coverFaceFor(size float64, family coverFont) font.Face {
	coverFontOnce[family].Do(func() {
		var data []byte
		switch family {
		case coverWenKai:
			data = coverWenKaiBytes
		case coverSmiley:
			data = coverSmileyBytes
		case coverXiaoWei:
			data = coverXiaoWeiBytes
		}
		coverFontObj[family], _ = opentype.Parse(data)
	})
	obj := coverFontObj[family]
	if obj == nil {
		return nil
	}
	face, _ := opentype.NewFace(obj, &opentype.FaceOptions{Size: size, DPI: 72})
	return face
}
func coverDrawText(dst draw.Image, s string, size float64, x, y int, c color.Color) {
	coverDrawTextFor(dst, s, size, x, y, c, coverSmiley)
}
func coverDrawTextFor(dst draw.Image, s string, size float64, x, y int, c color.Color, family coverFont) {
	face := coverFaceFor(size, family)
	if face == nil {
		return
	}
	(&font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: face, Dot: fixed.P(x, y)}).DrawString(s)
}
func coverTextWidth(s string, size float64) int {
	return coverTextWidthFor(s, size, coverSmiley)
}
func coverTextWidthFor(s string, size float64, family coverFont) int {
	face := coverFaceFor(size, family)
	if face == nil {
		return 0
	}
	return int((&font.Drawer{Face: face}).MeasureString(s) >> 6)
}

func coverDrawTrackedText(dst draw.Image, s string, size, tracking float64, x, y int, c color.Color, family coverFont) {
	face := coverFaceFor(size, family)
	if face == nil {
		return
	}
	d := font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: face}
	for _, r := range s {
		glyph := string(r)
		d.Dot = fixed.P(x, y)
		d.DrawString(glyph)
		x += int(d.MeasureString(glyph)>>6) + int(tracking)
	}
}

// 长库名不能越过海报区；只缩小字号，不截断用户自定义标题。
func coverFitSizeFor(s string, wanted, maxWidth float64, family coverFont) float64 {
	minimum := wanted / 6
	for wanted > minimum && float64(coverTextWidthFor(s, wanted, family)) > maxWidth {
		wanted -= 2
	}
	return wanted
}
func coverFitSize(s string, wanted, maxWidth float64) float64 {
	return coverFitSizeFor(s, wanted, maxWidth, coverSmiley)
}

func coverFill(img draw.Image, rect image.Rectangle, c color.Color) {
	draw.Draw(img, rect, image.NewUniform(c), image.Point{}, draw.Over)
}

func coverBlendPixel(img *image.RGBA, x, y int, c color.RGBA, alpha uint8) {
	old := img.RGBAAt(x, y)
	a, inv := int(alpha), 255-int(alpha)
	img.SetRGBA(x, y, color.RGBA{
		R: uint8((int(old.R)*inv + int(c.R)*a) / 255),
		G: uint8((int(old.G)*inv + int(c.G)*a) / 255),
		B: uint8((int(old.B)*inv + int(c.B)*a) / 255),
		A: 255,
	})
}

func coverDesignARects(w, h int) [3]image.Rectangle {
	sx, sy := float64(w)/1280, float64(h)/720
	px := func(v int) int { return int(float64(v) * sx) }
	py := func(v int) int { return int(float64(v) * sy) }
	var rects [3]image.Rectangle
	for i := range rects {
		x, y := px(590+i*205), py(85+i*80)
		rects[i] = image.Rect(x, y, x+px(240), y+py(390))
	}
	return rects
}

func coverDesignA(img *image.RGBA, zh, en string, posters []image.Image, bg color.RGBA) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	px, py, s := coverUnits(img)
	deep := coverMix(bg, color.RGBA{R: 6, G: 10, B: 18, A: 255}, .82)
	// 背景用首张海报的大半径模糊：竖版海报直接拉满横幅会放大出马赛克，模糊后只留色彩和光影。
	if len(posters) > 0 {
		draw.Draw(img, img.Bounds(), coverBackdrop(posters[0], w, h, px(56)), image.Point{}, draw.Src)
		coverDarken(img, .62, deep, .35)
	} else {
		coverFill(img, img.Bounds(), deep)
	}
	// 标题所在的左半边从左往右渐隐压暗，不画硬边底板。
	coverShade(img, image.Rect(0, 0, px(760), h), deep, .88, 0, true)
	coverVignette(img, .6)
	// 三张同宽同高，固定间距错位成阶梯；少于三张时不复制真实海报，
	// 而是从中间那级起放，免得一张海报孤零零挂在左上角、右半边全空。
	rects := coverDesignARects(w, h)
	start := 0
	if len(posters) < 3 {
		start = 1
	}
	for i := 0; i < len(posters) && start+i < len(rects); i++ {
		coverCard(img, posters[i], rects[start+i], 10*s, .75, 0)
	}
	cream := color.RGBA{R: 248, G: 241, B: 226, A: 255}
	accent := coverAccent(posters, bg, .72)
	coverFill(img, image.Rect(px(80), py(262), px(112), py(262)+max(1, py(2))), accent)
	coverDrawTrackedText(img, "CINEMA COLLECTION", 15*s, 4*s, px(126), py(269), coverAlpha(cream, .72), coverWenKai)
	zhSize := coverFitSizeFor(zh, 100*s, float64(px(470)), coverWenKai)
	coverTextShadow(img, zh, zhSize, 0, px(78), py(385), coverWenKai, px(18), .6)
	coverDrawTextFor(img, zh, zhSize, px(78), py(385), cream, coverWenKai)
	enSize := coverFitSizeFor(en, 22*s, float64(px(420)), coverWenKai)
	coverDrawTrackedText(img, en, enSize, 5*s, px(82), py(440), coverAlpha(cream, .8), coverWenKai)
	coverGrain(img, 4)
}

func coverDesignB(img *image.RGBA, zh, en string, posters []image.Image) {
	h := img.Bounds().Dy()
	px, py, s := coverUnits(img)
	paper := color.RGBA{R: 245, G: 241, B: 233, A: 255}
	ink := color.RGBA{R: 34, G: 36, B: 38, A: 255}
	coverFill(img, img.Bounds(), paper)
	// 左下角的海报底纹：首张海报转成单色版画，上沿和右沿都渐隐，低对比地托住标题区。
	// 原先直接贴彩色海报再盖一层白、只有纵向渐变，右侧留下一道硬边，看着像没渲染完。
	if len(posters) > 0 {
		band := image.Rect(0, py(430), px(700), h)
		tmp := image.NewRGBA(image.Rect(0, 0, band.Dx(), band.Dy()))
		coverCrop(tmp, posters[0], tmp.Bounds())
		tone := color.RGBA{R: 118, G: 110, B: 100, A: 255}
		for y := 0; y < band.Dy(); y++ {
			fy := coverSmooth(0, .6, float64(y)/float64(band.Dy()))
			for x := 0; x < band.Dx(); x++ {
				fx := 1 - coverSmooth(.55, 1, float64(x)/float64(band.Dx()))
				i := tmp.PixOffset(x, y)
				l := (.299*float64(tmp.Pix[i]) + .587*float64(tmp.Pix[i+1]) + .114*float64(tmp.Pix[i+2])) / 255
				coverBlendPixel(img, band.Min.X+x, band.Min.Y+y, coverMix(tone, paper, l), uint8(255*.5*fx*fy))
			}
		}
	}
	coverGrain(img, 5)
	for i := 0; i < 4 && len(posters) > 0; i++ {
		x := px(708 + (i%2)*270)
		y := py(48 + (i/2)*320)
		coverCard(img, posters[i%len(posters)], image.Rect(x, y, x+px(254), y+py(304)), 8*s, .22, 0)
	}
	coverDrawTrackedText(img, "MEDIA LIBRARY", 15*s, 4*s, px(72), py(180), ink, coverWenKai)
	coverFill(img, image.Rect(px(72), py(200), px(620), py(200)+max(1, py(2))), ink)
	zhSize := coverFitSizeFor(zh, 100*s, float64(px(560)), coverWenKai)
	// 霞鹜文楷 Medium 做大标题略单薄，横向叠一像素加粗；纵向也叠会把横画糊成一团。
	coverDrawTextFor(img, zh, zhSize, px(70), py(360), ink, coverWenKai)
	coverDrawTextFor(img, zh, zhSize, px(70)+max(1, px(1)), py(360), ink, coverWenKai)
	enSize := coverFitSizeFor(en, 24*s, float64(px(540)), coverWenKai)
	coverDrawTrackedText(img, en, enSize, 5*s, px(73), py(415), ink, coverWenKai)
	// 朱红印章里放标题首字，取代原先孤零零的红色方块。
	red := color.RGBA{R: 184, G: 52, B: 40, A: 255}
	seal := image.Rect(px(72), py(470), px(124), py(522))
	coverRoundFill(img, seal, 6*s, red)
	if runes := []rune(zh); len(runes) > 0 {
		ch := string(runes[0])
		size := 32 * s
		cw := coverTextWidthFor(ch, size, coverWenKai)
		coverDrawTextFor(img, ch, size, seal.Min.X+(seal.Dx()-cw)/2, seal.Min.Y+seal.Dy()/2+int(size*.36), paper, coverWenKai)
	}
	coverFill(img, image.Rect(px(146), py(496), px(620), py(496)+max(1, py(1))), color.RGBA{R: 160, G: 156, B: 148, A: 255})
}

func coverDesignC(img *image.RGBA, zh, en string, posters []image.Image, blur int) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	px, py, s := coverUnits(img)
	// 四列海报两侧各外延一段、在接缝处交叠羽化；原先的硬接缝在暗部尤其扎眼。
	feather := px(40)
	for col := 0; col < 4 && len(posters) > 0; col++ {
		x0, x1 := w*col/4, w*(col+1)/4
		if col == 0 {
			coverCrop(img, posters[0], image.Rect(0, 0, x1+feather, h))
			continue
		}
		tmp := image.NewRGBA(image.Rect(0, 0, x1-x0+2*feather, h))
		coverCrop(tmp, posters[col%len(posters)], tmp.Bounds())
		for y := 0; y < h; y++ {
			for x := 0; x < tmp.Rect.Dx() && x0-feather+x < w; x++ {
				i := tmp.PixOffset(x, y)
				a := coverSmooth(0, float64(2*feather), float64(x))
				coverBlendPixel(img, x0-feather+x, y, color.RGBA{R: tmp.Pix[i], G: tmp.Pix[i+1], B: tmp.Pix[i+2], A: 255}, uint8(255*a))
			}
		}
	}
	// 整体遮罩随「遮罩浓度」走；标题背后再压一团椭圆暗部，两侧仍能看清海报。
	deep := color.RGBA{R: 5, G: 9, B: 16, A: 255}
	coverFill(img, img.Bounds(), coverAlpha(deep, float64(25+blur*11/10)/255))
	coverSpot(img, w/2, py(385), px(600), py(520), deep, .78)
	coverVignette(img, .25)
	zhSize := coverFitSize(zh, 112*s, float64(px(1000)))
	zw := coverTextWidth(zh, zhSize)
	zx, zy := (w-zw)/2, py(372)
	coverTextShadow(img, zh, zhSize, 0, zx, zy, coverSmiley, px(24), .7)
	coverDrawText(img, zh, zhSize, zx, zy, color.White)
	coverDrawText(img, zh, zhSize, zx+max(1, px(1)), zy, color.White)
	// 点缀线取自海报主色，两端渐隐。
	accent := coverAccent(posters, color.RGBA{R: 202, G: 134, B: 87, A: 255}, .66)
	lineW := min(px(420), zw)
	for x := 0; x < lineW; x++ {
		a := math.Sin(math.Pi * (float64(x) + .5) / float64(lineW))
		for y := py(404); y < py(404)+max(2, py(3)); y++ {
			coverBlendPixel(img, (w-lineW)/2+x, y, accent, uint8(255*a))
		}
	}
	enSize := coverFitSize(en, 24*s, float64(px(800)))
	ew := coverTrackedWidth(en, enSize, 5*s, coverSmiley)
	coverTextShadow(img, en, enSize, 5*s, (w-ew)/2, py(458), coverSmiley, px(10), .6)
	coverDrawTrackedText(img, en, enSize, 5*s, (w-ew)/2, py(458), color.NRGBA{R: 245, G: 244, B: 240, A: 225}, coverSmiley)
	coverGrain(img, 3)
}

func coverDesignD(img *image.RGBA, zh, en string, posters []image.Image) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	px, py, s := coverUnits(img)
	ink := color.RGBA{R: 14, G: 29, B: 51, A: 255}
	split := px(765)
	coverFill(img, img.Bounds(), ink)
	if len(posters) > 0 {
		coverCrop(img, posters[0], image.Rect(0, 0, split, h))
		coverShade(img, image.Rect(0, h*3/5, split, h), color.RGBA{A: 255}, 0, .35, false)
		// 主视觉在最后 220px 里渐隐进标题栏，替代原先两条半透明接缝。
		coverShade(img, image.Rect(split-px(220), 0, split, h), ink, 0, 1, true)
	}
	coverShade(img, image.Rect(split, 0, w, h), color.RGBA{R: 5, G: 12, B: 24, A: 255}, 0, .5, false)
	cream := color.RGBA{R: 251, G: 243, B: 227, A: 255}
	accent := coverAccent(posters, color.RGBA{R: 204, G: 136, B: 91, A: 255}, .68)
	lines := []string{zh}
	if runes := []rune(zh); len(runes) >= 4 {
		mid := len(runes) / 2
		lines = []string{string(runes[:mid]), string(runes[mid:])}
	}
	x := px(808)
	if len(lines) == 2 {
		for i, line := range lines {
			size := coverFitSizeFor(line, 135*s, float64(px(425)), coverSmiley)
			coverDrawTextFor(img, line, size, x, py(272+i*150), cream, coverSmiley)
		}
	} else {
		size := coverFitSizeFor(zh, 125*s, float64(px(425)), coverSmiley)
		coverDrawTextFor(img, zh, size, x, py(360), cream, coverSmiley)
	}
	// 底部饰线：横线 — 英文名 — 横线。原先这里是一个写死的「04」，和库毫无关系。
	enSize := coverFitSizeFor(en, 20*s, float64(px(300)), coverSmiley)
	ew := coverTrackedWidth(en, enSize, 3*s, coverSmiley)
	right := px(1225)
	ex := x + (right-x-ew)/2
	ly := py(552)
	thick := max(1, py(2))
	coverFill(img, image.Rect(x, ly, ex-px(22), ly+thick), accent)
	coverFill(img, image.Rect(ex+ew+px(22), ly, right, ly+thick), accent)
	coverDrawTrackedText(img, en, enSize, 3*s, ex, ly+int(enSize*.36), cream, coverSmiley)
	coverGrain(img, 3)
}

func coverDesignE(img *image.RGBA, zh, en string, posters []image.Image) {
	w := img.Bounds().Dx()
	px, py, s := coverUnits(img)
	coverFill(img, img.Bounds(), color.RGBA{R: 22, G: 20, B: 18, A: 255})
	accent := coverAccent(posters, color.RGBA{R: 201, G: 133, B: 85, A: 255}, .62)
	// 胶片背后打一团放映机的暖光，纯色底看起来是空的。
	coverSpot(img, w/2, py(424), px(860), py(340), accent, .14)
	coverGrain(img, 5)
	cream := color.RGBA{R: 241, G: 224, B: 196, A: 255}
	zhSize := coverFitSizeFor(zh, 80*s, float64(px(640)), coverXiaoWei)
	coverDrawTextFor(img, zh, zhSize, px(62), py(128), cream, coverXiaoWei)
	enSize := coverFitSizeFor(en, 20*s, float64(px(510)), coverXiaoWei)
	coverDrawTrackedText(img, en, enSize, 4*s, px(64), py(172), coverAlpha(cream, .82), coverXiaoWei)
	if lineX := px(88) + coverTextWidthFor(zh, zhSize, coverXiaoWei); lineX < px(1200) {
		coverFill(img, image.Rect(lineX, py(122), px(1204), py(122)+max(1, py(2))), accent)
	}
	coverFill(img, image.Rect(px(1204), py(113), px(1216), py(125)), accent)
	// 胶片占画面下部三分之二：原先胶片缩在中间、底部空出 180px。
	top, bot := py(214), py(634)
	coverFill(img, image.Rect(0, top, w, bot), color.RGBA{R: 8, G: 8, B: 8, A: 255})
	hole := color.RGBA{R: 118, G: 104, B: 90, A: 255}
	for x := px(10); x < w; x += max(1, px(35)) {
		coverRoundFill(img, image.Rect(x, top+py(12), x+px(14), top+py(25)), 2.5*s, hole)
		coverRoundFill(img, image.Rect(x, bot-py(25), x+px(14), bot-py(12)), 2.5*s, hole)
	}
	const count = 5
	gap := px(11)
	frameW := (w - gap*(count-1)) / count
	for i := 0; i < count && len(posters) > 0; i++ {
		x := i * (frameW + gap)
		coverCard(img, posters[i%len(posters)], image.Rect(x, top+py(38), x+frameW, bot-py(38)), 3*s, 0, 0)
	}
	coverVignette(img, .35)
}

// F · 倾斜海报墙：右侧一整面错位排列、顺时针倾斜的海报墙，左侧压暗放标题。
// 海报墙是底纹，少于一屏时循环复用海报；相邻两格（横竖都算）不会是同一张。
func coverDesignF(img *image.RGBA, zh, en string, posters []image.Image, bg color.RGBA) {
	h := img.Bounds().Dy()
	px, py, s := coverUnits(img)
	deep := coverMix(bg, color.RGBA{R: 6, G: 9, B: 16, A: 255}, .8)
	coverFill(img, img.Bounds(), deep)
	if len(posters) > 0 {
		// 先在正放的画布上排好整面墙再整体旋转一次：逐张旋转慢，相邻两张的接缝还会转出细缝。
		const cols, rows = 4, 6
		cw, ch, gap := px(196), py(294), px(20)
		wall := image.NewRGBA(image.Rect(0, 0, cols*(cw+gap)+gap, (rows-1)*(ch+gap)))
		coverFill(wall, wall.Bounds(), deep)
		for c := 0; c < cols; c++ {
			// 每列错开三分之一张，墙面才不像整齐的表格。
			off := -((c*2)%3)*ch/3 - gap
			for r := 0; r < rows; r++ {
				x, y := gap+c*(cw+gap), off+r*(ch+gap)
				coverCard(wall, posters[(r*cols+c)%len(posters)], image.Rect(x, y, x+cw, y+ch), 10*s, .45, 0)
			}
		}
		coverDarken(wall, .88, deep, .08)
		coverRotate(img, wall, px(930), py(360), 12, 0)
	}
	// 左侧 360px 实色，再往右 500px 渐隐进海报墙；标题压在实色区，任何海报都不影响可读性。
	coverFill(img, image.Rect(0, 0, px(360), h), deep)
	coverShade(img, image.Rect(px(360), 0, px(860), h), deep, 1, 0, true)
	coverVignette(img, .4)
	cream := color.RGBA{R: 250, G: 246, B: 238, A: 255}
	accent := coverAccent(posters, color.RGBA{R: 214, G: 150, B: 96, A: 255}, .66)
	coverFill(img, image.Rect(px(80), py(288), px(112), py(288)+max(1, py(3))), accent)
	coverDrawTrackedText(img, "FEATURED", 15*s, 5*s, px(126), py(296), coverAlpha(cream, .7), coverSmiley)
	zhSize := coverFitSize(zh, 108*s, float64(px(400)))
	coverDrawText(img, zh, zhSize, px(78), py(408), cream)
	coverDrawText(img, zh, zhSize, px(78)+max(1, px(1)), py(408), cream)
	enSize := coverFitSize(en, 22*s, float64(px(400)))
	coverDrawTrackedText(img, en, enSize, 5*s, px(82), py(460), coverAlpha(cream, .8), coverSmiley)
	coverGrain(img, 3)
}

// G · 拍立得：海报做成白框相纸，散落在首张海报的模糊背景上，标题在左侧。
// 只放真实海报，不循环复用：散落的相片里出现两张一样的会很假。
func coverDesignG(img *image.RGBA, zh, en string, posters []image.Image, bg color.RGBA) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	px, py, s := coverUnits(img)
	deep := coverMix(bg, color.RGBA{R: 14, G: 12, B: 12, A: 255}, .8)
	if len(posters) > 0 {
		draw.Draw(img, img.Bounds(), coverBackdrop(posters[0], w, h, px(64)), image.Point{}, draw.Src)
		coverDarken(img, .55, deep, .3)
	} else {
		coverFill(img, img.Bounds(), deep)
	}
	coverShade(img, image.Rect(0, 0, px(680), h), deep, .7, 0, true)
	coverVignette(img, .5)
	// 按重要程度排：第一张居中压在最上面，其余往两侧、再往下方散开。
	// 三张以内排成一行、垂直居中；四张起才分两行，否则一行时整组偏上、下半屏空着。
	type slot struct {
		x, y int
		deg  float64
	}
	slots := []slot{{880, 360, -3}, {662, 330, -9}, {1092, 345, 7}}
	if len(posters) > 3 {
		slots = []slot{{880, 290, -3}, {662, 254, -9}, {1092, 272, 7}, {742, 498, 6}, {1040, 508, -5}}
	}
	for i := min(len(posters), len(slots)) - 1; i >= 0; i-- {
		layer := coverPolaroid(posters[i], px(186), py(246), px(12), py(46))
		coverRotate(img, layer, px(slots[i].x), py(slots[i].y), slots[i].deg, .6)
	}
	cream := color.RGBA{R: 248, G: 241, B: 226, A: 255}
	accent := coverAccent(posters, bg, .72)
	coverFill(img, image.Rect(px(80), py(270), px(112), py(270)+max(1, py(2))), accent)
	coverDrawTrackedText(img, "SELECTED WORKS", 15*s, 4*s, px(126), py(277), coverAlpha(cream, .72), coverWenKai)
	zhSize := coverFitSizeFor(zh, 92*s, float64(px(410)), coverWenKai)
	coverTextShadow(img, zh, zhSize, 0, px(78), py(388), coverWenKai, px(18), .6)
	coverDrawTextFor(img, zh, zhSize, px(78), py(388), cream, coverWenKai)
	enSize := coverFitSizeFor(en, 22*s, float64(px(400)), coverWenKai)
	coverDrawTrackedText(img, en, enSize, 5*s, px(82), py(440), coverAlpha(cream, .8), coverWenKai)
	coverGrain(img, 4)
}

// H · 大字海报：以字为主角。纯色底上一张主海报，巨大的标题从左侧压过海报边缘，
// 海报后错开一个细线框，英文名竖排在右侧。底色跟随「背景」配置。
func coverDesignH(img *image.RGBA, zh, en string, posters []image.Image, bg color.RGBA) {
	px, py, s := coverUnits(img)
	field := coverMix(bg, color.RGBA{R: 16, G: 18, B: 22, A: 255}, .35)
	coverFill(img, img.Bounds(), field)
	coverShade(img, img.Bounds(), color.RGBA{A: 255}, 0, .35, false)
	coverGrain(img, 5)
	cream := color.RGBA{R: 250, G: 244, B: 232, A: 255}
	card := image.Rect(px(820), py(96), px(1150), py(591))
	coverOutline(img, card.Add(image.Pt(px(22), py(22))), max(1, px(2)), coverAlpha(cream, .5))
	if len(posters) > 0 {
		coverCard(img, posters[0], card, 6*s, .5, 0)
	}
	coverDrawTrackedText(img, "MEDIA LIBRARY", 15*s, 5*s, px(82), py(120), coverAlpha(cream, .75), coverSmiley)
	// 标题有意压过海报左缘：字和图叠在一起才有海报感。只缩字号不截断，长库名最多压到海报中线。
	zhSize := coverFitSize(zh, 190*s, float64(px(900)))
	coverTextShadow(img, zh, zhSize, 0, px(76), py(420), coverSmiley, px(22), .55)
	coverDrawText(img, zh, zhSize, px(76), py(420), cream)
	coverDrawText(img, zh, zhSize, px(76)+max(1, px(1)), py(420), cream)
	enSize := coverFitSize(en, 26*s, float64(px(640)))
	coverTextShadow(img, en, enSize, 6*s, px(82), py(486), coverSmiley, px(10), .5)
	coverDrawTrackedText(img, en, enSize, 6*s, px(82), py(486), cream, coverSmiley)
	accent := coverAccent(posters, color.RGBA{R: 240, G: 190, B: 110, A: 255}, .7)
	coverFill(img, image.Rect(px(82), py(526), px(202), py(526)+max(2, py(4))), accent)
	coverVerticalText(img, en, 15*s, 6*s, px(1222), py(344), coverAlpha(cream, .7), coverSmiley)
}

// I · 封面流：首张海报居中最大，其余向两侧依次变小变暗，底下带倒影，标题在上方。
// 只放真实海报，缺的位置空着，不复用。
func coverDesignI(img *image.RGBA, zh, en string, posters []image.Image, bg color.RGBA) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	px, py, s := coverUnits(img)
	deep := coverMix(bg, color.RGBA{R: 6, G: 8, B: 14, A: 255}, .82)
	if len(posters) > 0 {
		draw.Draw(img, img.Bounds(), coverBackdrop(posters[0], w, h, px(64)), image.Point{}, draw.Src)
		coverDarken(img, .5, deep, .35)
	} else {
		coverFill(img, img.Bounds(), deep)
	}
	// 下半截压成「地面」，倒影才有落脚的地方。
	coverShade(img, image.Rect(0, py(430), w, h), deep, 0, .85, false)
	coverVignette(img, .45)
	// 底边对齐在同一条地平线上；绘制从最外侧往中间，中间那张压在最上面。
	slots := []struct {
		cx, top, w, h int
		dim           float64
	}{{640, 180, 240, 360, 0}, {400, 240, 200, 300, .35}, {880, 240, 200, 300, .35}, {196, 300, 160, 240, .6}, {1084, 300, 160, 240, .6}}
	for i := min(len(posters), len(slots)) - 1; i >= 0; i-- {
		sl := slots[i]
		rect := image.Rect(px(sl.cx-sl.w/2), py(sl.top), px(sl.cx+sl.w/2), py(sl.top+sl.h))
		coverReflect(img, posters[i], rect, max(2, py(6)), .38, .32, sl.dim)
		coverCard(img, posters[i], rect, 8*s, .6, sl.dim)
	}
	cream := color.RGBA{R: 248, G: 241, B: 226, A: 255}
	zhSize := coverFitSizeFor(zh, 62*s, float64(px(900)), coverWenKai)
	zw := coverTextWidthFor(zh, zhSize, coverWenKai)
	coverTextShadow(img, zh, zhSize, 0, (w-zw)/2, py(98), coverWenKai, px(16), .6)
	coverDrawTextFor(img, zh, zhSize, (w-zw)/2, py(98), cream, coverWenKai)
	enSize := coverFitSizeFor(en, 17*s, float64(px(700)), coverWenKai)
	ew := coverTrackedWidth(en, enSize, 5*s, coverWenKai)
	coverDrawTrackedText(img, en, enSize, 5*s, (w-ew)/2, py(138), coverAlpha(cream, .75), coverWenKai)
}

func coverEnglishName(name string) string {
	switch {
	case (strings.Contains(name, "动漫") || strings.Contains(name, "动画")) && strings.Contains(name, "电影"):
		return "ANIMATION FILMS"
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
	zh, en := coverTitle(name, cfg.Titles)
	bg := coverBackground(cfg, name, posters)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	switch cfg.Style {
	case "editorial_a":
		coverDesignA(img, zh, en, posters, bg)
	case "editorial_b":
		coverDesignB(img, zh, en, posters)
	case "editorial_d":
		coverDesignD(img, zh, en, posters)
	case "editorial_e":
		coverDesignE(img, zh, en, posters)
	case "editorial_f":
		coverDesignF(img, zh, en, posters, bg)
	case "editorial_g":
		coverDesignG(img, zh, en, posters, bg)
	case "editorial_h":
		coverDesignH(img, zh, en, posters, bg)
	case "editorial_i":
		coverDesignI(img, zh, en, posters, bg)
	default:
		coverDesignC(img, zh, en, posters, cfg.Blur)
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

var coverSampleStyles = []string{"editorial_a", "editorial_b", "editorial_c", "editorial_d", "editorial_e", "editorial_f", "editorial_g", "editorial_h", "editorial_i"}

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
//	{"config": {...}, "live": false}                → {"samples": {"editorial_a": dataURL, ...}}
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
