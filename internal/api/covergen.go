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

//go:embed assets/notoserifsc-vf.ttf
var coverSerifFontBytes []byte

var (
	coverFontOnce      sync.Once
	coverFontObj       *opentype.Font
	coverSerifFontOnce sync.Once
	coverSerifFontObj  *opentype.Font
	coverRunMu         sync.Mutex
	coverLastRun       string
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
	if !map[string]bool{"editorial_a": true, "editorial_b": true, "editorial_c": true, "editorial_d": true, "editorial_e": true}[c.Style] {
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

func coverFaceFor(size float64, serif bool) font.Face {
	var obj *opentype.Font
	if serif {
		coverSerifFontOnce.Do(func() { coverSerifFontObj, _ = opentype.Parse(coverSerifFontBytes) })
		obj = coverSerifFontObj
	} else {
		coverFontOnce.Do(func() { coverFontObj, _ = opentype.Parse(coverFontBytes) })
		obj = coverFontObj
	}
	if obj == nil {
		return nil
	}
	face, _ := opentype.NewFace(obj, &opentype.FaceOptions{Size: size, DPI: 72})
	return face
}
func coverDrawText(dst draw.Image, s string, size float64, x, y int, c color.Color) {
	coverDrawTextFor(dst, s, size, x, y, c, false)
}
func coverDrawTextFor(dst draw.Image, s string, size float64, x, y int, c color.Color, serif bool) {
	face := coverFaceFor(size, serif)
	if face == nil {
		return
	}
	(&font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: face, Dot: fixed.P(x, y)}).DrawString(s)
}
func coverTextWidth(s string, size float64) int {
	return coverTextWidthFor(s, size, false)
}
func coverTextWidthFor(s string, size float64, serif bool) int {
	face := coverFaceFor(size, serif)
	if face == nil {
		return 0
	}
	return int((&font.Drawer{Face: face}).MeasureString(s) >> 6)
}

func coverDrawTrackedText(dst draw.Image, s string, size, tracking float64, x, y int, c color.Color, serif bool) {
	face := coverFaceFor(size, serif)
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
func coverFitSizeFor(s string, wanted, maxWidth float64, serif bool) float64 {
	minimum := wanted / 6
	for wanted > minimum && float64(coverTextWidthFor(s, wanted, serif)) > maxWidth {
		wanted -= 2
	}
	return wanted
}
func coverFitSize(s string, wanted, maxWidth float64) float64 {
	return coverFitSizeFor(s, wanted, maxWidth, false)
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
	sx, sy := float64(w)/1280, float64(h)/720
	px := func(v int) int { return int(float64(v) * sx) }
	py := func(v int) int { return int(float64(v) * sy) }
	// 海报图像留在暗部，标题所在区域单独压暗；整张图盖死会失去概念稿的电影氛围。
	if len(posters) > 0 {
		coverCrop(img, posters[0], img.Bounds())
	}
	coverFill(img, img.Bounds(), color.NRGBA{R: 8, G: 16, B: 28, A: 170})
	// 两个方向都渐隐，避免文字底板在海报图像上形成硬边矩形。
	for y := 0; y < py(580); y++ {
		fy := math.Min(1, math.Max(0, float64(py(580)-y)/float64(py(110))))
		for x := 0; x < px(650); x++ {
			fx := math.Min(1, math.Max(0, float64(px(650)-x)/float64(px(140))))
			coverBlendPixel(img, x, y, color.RGBA{R: 7, G: 15, B: 27, A: 255}, uint8(155*fx*fy))
		}
	}
	accent := color.RGBA{R: uint8(min(255, int(bg.R)+85)), G: uint8(min(255, int(bg.G)+85)), B: uint8(min(255, int(bg.B)+85)), A: 255}
	coverFill(img, image.Rect(px(62), py(116), px(148), py(118)), accent)
	coverDrawTrackedText(img, "CINEMA COLLECTION", 17*sy, 3*sy, px(62), py(100), color.NRGBA{R: 238, G: 225, B: 201, A: 210}, true)
	// 三张同宽同高，固定间距错位成阶梯；少于三张时不复制真实海报。
	rects := coverDesignARects(w, h)
	for i, p := range posters {
		if i == 3 {
			break
		}
		rect := rects[i]
		coverFill(img, rect.Add(image.Pt(px(10), py(12))), color.NRGBA{A: 105})
		coverCrop(img, p, rect)
	}
	zhSize := coverFitSizeFor(zh, 96*sy, float64(px(525)), true)
	coverDrawTextFor(img, zh, zhSize, px(62), py(390), color.RGBA{R: 248, G: 241, B: 226, A: 255}, true)
	coverFill(img, image.Rect(px(62), py(438), px(548), py(440)), accent)
	enSize := coverFitSizeFor(en, 24*sy, float64(px(440)), true)
	coverDrawTrackedText(img, en, enSize, 3*sy, px(62), py(486), color.NRGBA{R: 239, G: 226, B: 206, A: 225}, true)
}

func coverDesignB(img *image.RGBA, zh, en string, posters []image.Image) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	sx, sy := float64(w)/1280, float64(h)/720
	px := func(v int) int { return int(float64(v) * sx) }
	py := func(v int) int { return int(float64(v) * sy) }
	coverFill(img, img.Bounds(), color.RGBA{R: 246, G: 242, B: 234, A: 255})
	// 左下角用真实海报的浅色轮廓托住标题，像画册里的低对比度版画。
	if len(posters) > 0 {
		coverCrop(img, posters[0], image.Rect(0, py(490), px(690), h))
		for y := py(490); y < h; y++ {
			progress := float64(y-py(490)) / float64(h-py(490))
			a := uint8(250 - 105*progress)
			coverFill(img, image.Rect(0, y, px(690), y+1), color.NRGBA{R: 246, G: 242, B: 234, A: a})
		}
	}
	// 一枚低饱和朱红圆作为画册印记，也补足左下角的视觉重量。
	for y := py(545); y < py(675); y++ {
		for x := px(505); x < px(635); x++ {
			dx, dy := float64(x-px(570))/sx, float64(y-py(610))/sy
			if dx*dx+dy*dy <= 65*65 {
				coverBlendPixel(img, x, y, color.RGBA{R: 190, G: 55, B: 42, A: 255}, 115)
			}
		}
	}
	ink := color.RGBA{R: 35, G: 38, B: 39, A: 255}
	coverDrawTrackedText(img, "MEDIA LIBRARY", 17*sy, 3*sy, px(56), py(90), ink, true)
	coverFill(img, image.Rect(px(56), py(110), px(615), py(112)), ink)
	zhSize := coverFitSizeFor(zh, 100*sy, float64(px(590)), true)
	for dx := -1; dx <= 1; dx++ {
		coverDrawTextFor(img, zh, zhSize, px(56)+int(float64(dx)*sx), py(350), ink, true)
	}
	enSize := coverFitSizeFor(en, 26*sy, float64(px(500)), true)
	coverDrawTrackedText(img, en, enSize, 4*sy, px(57), py(405), ink, true)
	red := color.RGBA{R: 190, G: 55, B: 42, A: 255}
	coverFill(img, image.Rect(px(56), py(452), px(75), py(471)), red)
	coverFill(img, image.Rect(px(92), py(460), px(520), py(462)), color.RGBA{R: 158, G: 155, B: 148, A: 255})
	for i := 0; i < 4 && len(posters) > 0; i++ {
		p := posters[i%len(posters)]
		x := px(700 + (i%2)*276)
		y := py(40 + (i/2)*328)
		coverCrop(img, p, image.Rect(x, y, x+px(264), y+py(312)))
	}
}

func coverDesignC(img *image.RGBA, zh, en string, posters []image.Image, blur int) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	sx, sy := float64(w)/1280, float64(h)/720
	px := func(v int) int { return int(float64(v) * sx) }
	py := func(v int) int { return int(float64(v) * sy) }
	for i := 0; i < 4 && len(posters) > 0; i++ {
		p := posters[i%len(posters)]
		x0, x1 := w*i/4, w*(i+1)/4
		coverCrop(img, p, image.Rect(x0, 0, x1, h))
	}
	// 中央渐暗而两侧保留海报颜色；遮罩浓度滑块仍可调节。
	for x := 0; x < w; x++ {
		center := 1 - math.Min(1, math.Abs(float64(x)-float64(w)/2)/(float64(w)*.45))
		a := uint8(min(240, int(45+float64(blur)*.45+175*center*center)))
		coverFill(img, image.Rect(x, 0, x+1, h), color.NRGBA{R: 5, G: 10, B: 17, A: a})
	}
	zhSize := coverFitSize(zh, 105*sy, float64(px(1050)))
	zw := coverTextWidth(zh, zhSize)
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			coverDrawText(img, zh, zhSize, (w-zw)/2+int(float64(dx)*sx), py(360)+int(float64(dy)*sy), color.White)
		}
	}
	lineW := min(px(540), zw)
	coverFill(img, image.Rect((w-lineW)/2, py(397), (w+lineW)/2, py(400)), color.RGBA{R: 202, G: 134, B: 87, A: 255})
	enSize := coverFitSize(en, 31*sy, float64(px(800)))
	ew := coverTextWidth(en, enSize)
	coverDrawText(img, en, enSize, (w-ew)/2, py(455), color.RGBA{R: 245, G: 244, B: 240, A: 255})
}

func coverDesignD(img *image.RGBA, zh, en string, posters []image.Image) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	sx, sy := float64(w)/1280, float64(h)/720
	px := func(v int) int { return int(float64(v) * sx) }
	py := func(v int) int { return int(float64(v) * sy) }
	if len(posters) > 0 {
		coverCrop(img, posters[0], image.Rect(0, 0, px(765), h))
	}
	ink := color.RGBA{R: 14, G: 29, B: 51, A: 255}
	coverFill(img, image.Rect(px(765), 0, w, h), ink)
	// 两条半透明接缝把图片和色块连起来，而不在主视觉上盖硬阴影。
	coverFill(img, image.Rect(px(746), 0, px(765), h), color.NRGBA{R: 18, G: 38, B: 65, A: 92})
	coverFill(img, image.Rect(px(765), 0, px(780), h), color.NRGBA{R: 240, G: 230, B: 211, A: 27})
	cream := color.RGBA{R: 251, G: 243, B: 227, A: 255}
	copper := color.RGBA{R: 204, G: 136, B: 91, A: 255}
	lines := []string{zh}
	runes := []rune(zh)
	if len(runes) >= 4 {
		mid := len(runes) / 2
		lines = []string{string(runes[:mid]), string(runes[mid:])}
	}
	if len(lines) == 2 {
		for i, line := range lines {
			size := coverFitSizeFor(line, 135*sy, float64(px(425)), true)
			coverDrawTextFor(img, line, size, px(808), py(285+i*150), cream, true)
		}
	} else {
		size := coverFitSizeFor(zh, 125*sy, float64(px(425)), true)
		coverDrawTextFor(img, zh, size, px(808), py(390), cream, true)
	}
	coverFill(img, image.Rect(px(807), py(518), px(849), py(520)), cream)
	enSize := coverFitSizeFor(en, 22*sy, float64(px(335)), true)
	coverDrawTrackedText(img, en, enSize, 2*sy, px(862), py(526), cream, true)
	coverFill(img, image.Rect(px(807), py(616), px(956), py(618)), copper)
	coverDrawTextFor(img, "04", 45*sy, px(974), py(630), copper, true)
	coverFill(img, image.Rect(px(1065), py(616), px(1225), py(618)), copper)
}

func coverDesignE(img *image.RGBA, zh, en string, posters []image.Image) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	sx, sy := float64(w)/1280, float64(h)/720
	px := func(v int) int { return int(float64(v) * sx) }
	py := func(v int) int { return int(float64(v) * sy) }
	coverFill(img, img.Bounds(), color.RGBA{R: 22, G: 20, B: 18, A: 255})
	// 稀疏颗粒只给纯色留白增加纸感，不遮住海报和标题。
	for y := py(6); y < h; y += max(1, py(13)) {
		for x := px(6); x < w; x += max(1, px(13)) {
			if (x*31+y*17)%7 == 0 {
				coverBlendPixel(img, x, y, color.RGBA{R: 164, G: 128, B: 89, A: 255}, 35)
			}
		}
	}
	cream := color.RGBA{R: 241, G: 224, B: 196, A: 255}
	copper := color.RGBA{R: 201, G: 133, B: 85, A: 255}
	zhSize := coverFitSizeFor(zh, 83*sy, float64(px(640)), true)
	coverDrawTextFor(img, zh, zhSize, px(60), py(145), cream, true)
	enSize := coverFitSizeFor(en, 21*sy, float64(px(510)), true)
	coverDrawTrackedText(img, en, enSize, 4*sy, px(62), py(190), cream, true)
	lineX := px(86) + coverTextWidthFor(zh, zhSize, true)
	if lineX < px(1210) {
		coverFill(img, image.Rect(lineX, py(149), px(1210), py(151)), copper)
	}
	coverFill(img, image.Rect(px(1210), py(138), px(1224), py(151)), copper)
	coverFill(img, image.Rect(0, py(220), w, py(543)), color.RGBA{R: 7, G: 7, B: 7, A: 255})
	for x := px(7); x < w; x += max(1, px(35)) {
		coverFill(img, image.Rect(x, py(230), x+px(13), py(242)), color.RGBA{R: 138, G: 119, B: 99, A: 255})
		coverFill(img, image.Rect(x, py(521), x+px(13), py(533)), color.RGBA{R: 138, G: 119, B: 99, A: 255})
	}
	const count = 5
	gap := px(11)
	frameW := (w - gap*(count-1)) / count
	for i := 0; i < count && len(posters) > 0; i++ {
		x := i * (frameW + gap)
		coverCrop(img, posters[i%len(posters)], image.Rect(x, py(251), x+frameW, py(511)))
	}
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

var coverSampleStyles = []string{"editorial_a", "editorial_b", "editorial_c", "editorial_d", "editorial_e"}

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
