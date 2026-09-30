package api

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"115-station/internal/model"

	"github.com/longbridgeapp/opencc"
)

// ==================== 演职人员：中文名与 TMDB 人物缓存 ====================
//
// TMDB 的人物 name 不随 language 本地化（「Tom Hanks」在 zh-CN 下还是 Tom Hanks），
// 中文名只能从两处取：人物翻译（translations，部分人物有 zh-CN 的 name）与别名 also_known_as。
// 别名里简繁混着来（「汤姆·汉克斯」「湯姆·漢克斯」都有），统一转简体。
// 取法参考 MoviePilot 官方插件 personmeta 的 __get_chinese_name（只读参考，未复制代码）。

var (
	t2sOnce sync.Once
	t2sConv *opencc.OpenCC
)

// toSimplified 繁转简。词典随二进制嵌入（OpenCC，Apache-2.0）；初始化失败就原样返回
func toSimplified(s string) string {
	t2sOnce.Do(func() {
		c, err := opencc.New("t2s")
		if err != nil {
			log.Printf("[演职人员] ✗ 繁简转换词典加载失败，中文名保持原样: %v", err)
			return
		}
		t2sConv = c
	})
	if t2sConv == nil || s == "" {
		return s
	}
	out, err := t2sConv.Convert(s)
	if err != nil {
		return s
	}
	return out
}

// hasHan 含汉字
func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// chineseNameOK 能当中文名用：有汉字、没有假名 / 谚文（日文、韩文的别名也常带汉字，不能当中文名）
func chineseNameOK(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || !hasHan(s) {
		return false
	}
	for _, r := range s {
		if unicode.In(r, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			return false
		}
	}
	return true
}

// tmdbPersonDetail /person/{id}?append_to_response=translations 的有用部分
type tmdbPersonDetail struct {
	ID           int      `json:"id"`
	Name         string   `json:"name"`
	AlsoKnownAs  []string `json:"also_known_as"`
	Biography    string   `json:"biography"`
	ProfilePath  string   `json:"profile_path"`
	Translations struct {
		Translations []struct {
			Country  string `json:"iso_3166_1"`
			Language string `json:"iso_639_1"`
			Data     struct {
				Name      string `json:"name"`
				Biography string `json:"biography"`
			} `json:"data"`
		} `json:"translations"`
	} `json:"translations"`
}

// zhRegionRank 中文翻译的地区优先级：大陆 / 新加坡本就是简体，港台的要转
func zhRegionRank(country string) int {
	switch strings.ToUpper(country) {
	case "CN":
		return 0
	case "SG":
		return 1
	case "TW":
		return 2
	case "HK":
		return 3
	}
	return 4
}

// pickChineseName 从 TMDB 人物详情里挑中文名（已转简体）；没有返回空串。顺序：
//  1. zh 翻译里的 name（按地区优先级）
//  2. 原名本身就是中文（华人演员常见）
//  3. 别名里已经是简体的那个（转简体前后不变）
//  4. 别名里第一个中文名，转简体
func pickChineseName(d tmdbPersonDetail) string {
	best, bestRank := "", 99
	for _, t := range d.Translations.Translations {
		if t.Language != "zh" || !chineseNameOK(t.Data.Name) {
			continue
		}
		if r := zhRegionRank(t.Country); r < bestRank {
			best, bestRank = strings.TrimSpace(t.Data.Name), r
		}
	}
	if best != "" {
		return toSimplified(best)
	}
	if chineseNameOK(d.Name) {
		return toSimplified(strings.TrimSpace(d.Name))
	}
	first := ""
	for _, n := range d.AlsoKnownAs {
		n = strings.TrimSpace(n)
		if !chineseNameOK(n) {
			continue
		}
		if toSimplified(n) == n {
			return n
		}
		if first == "" {
			first = n
		}
	}
	return toSimplified(first)
}

// pickChineseBio 中文简介：请求带了 language=zh-CN，biography 有汉字就是中文；否则看 zh 翻译
func pickChineseBio(d tmdbPersonDetail) string {
	if hasHan(d.Biography) {
		return toSimplified(strings.TrimSpace(d.Biography))
	}
	best, bestRank := "", 99
	for _, t := range d.Translations.Translations {
		if t.Language != "zh" || !hasHan(t.Data.Biography) {
			continue
		}
		if r := zhRegionRank(t.Country); r < bestRank {
			best, bestRank = strings.TrimSpace(t.Data.Biography), r
		}
	}
	return toSimplified(best)
}

// personMetaTTL 缓存多久重新问一次 TMDB：有中文名和头像的基本不会变；缺的隔一阵再看有没有人补上
const (
	personMetaTTLFull    = 180 * 24 * time.Hour
	personMetaTTLPartial = 30 * 24 * time.Hour
)

func personMetaFresh(m model.PersonMeta, now time.Time) bool {
	ttl := personMetaTTLPartial
	if m.ZhName != "" && m.ProfilePath != "" {
		ttl = personMetaTTLFull
	}
	return now.Sub(m.FetchedAt) < ttl
}

// loadPersonMeta 读缓存；过期或没有就问 TMDB 并落库。beforeFetch 在真要发 TMDB 请求前调用（调用方据此节流），可为 nil
func loadPersonMeta(tc *TmdbClient, id int, beforeFetch func()) (m model.PersonMeta, err error) {
	if id <= 0 {
		return m, fmt.Errorf("无效的 TMDB 人物 id")
	}
	if model.DB.First(&m, id).Error == nil && personMetaFresh(m, time.Now()) {
		return m, nil
	}
	if beforeFetch != nil {
		beforeFetch()
	}
	body, err := tc.get("/person/"+strconv.Itoa(id), map[string]string{
		"language": "zh-CN", "append_to_response": "translations",
	})
	if err != nil {
		return m, err
	}
	var d tmdbPersonDetail
	if err := json.Unmarshal(body, &d); err != nil {
		return m, fmt.Errorf("人物详情解析失败")
	}
	m = model.PersonMeta{
		TmdbID: id, Name: d.Name, ZhName: pickChineseName(d), ProfilePath: d.ProfilePath,
		ZhBio: pickChineseBio(d), FetchedAt: time.Now(),
	}
	model.DB.Save(&m)
	return m, nil
}

// cachedZhNames 一批 TMDB 人物 id 在缓存里的中文名（刮削写 NFO 用，不发任何 TMDB 请求）
func cachedZhNames(ids []int) map[int]string {
	out := map[int]string{}
	if len(ids) == 0 || model.DB == nil {
		return out
	}
	var rows []model.PersonMeta
	model.DB.Select("tmdb_id", "zh_name").Where("tmdb_id IN ? AND zh_name <> ''", ids).Find(&rows)
	for _, r := range rows {
		out[r.TmdbID] = r.ZhName
	}
	return out
}
