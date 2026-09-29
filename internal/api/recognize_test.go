package api

import (
	"strings"
	"testing"
)

func TestSplitCJKLatin(t *testing.T) {
	cases := []struct {
		title, cjk, latin string
	}{
		// 用户实测场景：中文名 + 方括号标注 + 英文名
		{"骗不了人的男人[国日多音轨+中文字幕] Softie Conman", "骗不了人的男人", "Softie Conman"},
		// 纯中文名 + 英文名，空格分隔
		{"骗不了人的男人 Softie Conman", "骗不了人的男人", "Softie Conman"},
		// 纯中文 → latin 为空（不触发拆分）
		{"骗不了人的男人", "骗不了人的男人", ""},
		// 纯英文 → cjk 为空
		{"The Shawshank Redemption", "", "The Shawshank Redemption"},
		// 中文段取最长；空格会把相邻中文段连成一个整体（无法区分时宁可整段搜索）
		{"高清影视之家发布 骗不了人的男人 Softie Conman", "高清影视之家发布 骗不了人的男人", "Softie Conman"},
	}
	for _, c := range cases {
		cjk, latin := splitCJKLatin(c.title)
		if cjk != c.cjk || latin != c.latin {
			t.Errorf("splitCJKLatin(%q) = (%q, %q), want (%q, %q)", c.title, cjk, latin, c.cjk, c.latin)
		}
	}
}

func TestStripReleaseAds(t *testing.T) {
	cases := []struct{ in, want string }{
		// 全角括号广告块 + 域名（点号保留，由 parseFileName 统一替换为空格）
		{"【高清影视之家发布 www.SSDSSE.com】骗不了人的男人.Softie.Conman.2022.1080p", "骗不了人的男人.Softie.Conman.2022.1080p"},
		// 裸域名
		{"骗不了人的男人 www.4k688.com.mkv", "骗不了人的男人 .mkv"},
		// 干净名字不受影响（Softie.Conman 的 .Conman 不是 TLD）
		{"Softie.Conman.2022.1080p.MyTVSuper.WEB-DL.H265", "Softie.Conman.2022.1080p.MyTVSuper.WEB-DL.H265"},
	}
	for _, c := range cases {
		if got := stripReleaseAds(c.in); got != c.want {
			t.Errorf("stripReleaseAds(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseFileNameAdPrefix(t *testing.T) {
	// 广告块剥离后，标题应从真实片名开始提取
	p := parseFileName("【高清影视之家发布 www.SSDSSE.com】骗不了人的男人.Softie.Conman.2022.1080p.MyTVSuper.WEB-DL.H265.AAC-QuickIO.mkv")
	for _, bad := range []string{"高清影视之家", "SSDSSE", "www"} {
		if strings.Contains(p.Title, bad) {
			t.Errorf("标题仍含广告 %q: %q", bad, p.Title)
		}
	}
	if p.Year != "2022" {
		t.Errorf("年份解析错误: %q", p.Year)
	}
	if p.Resolution != "1080P" {
		t.Errorf("分辨率解析错误: %q", p.Resolution)
	}
}

func TestIsAdOnlyVideo(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		// 用户实测的引流视频：文件名整体是广告
		{"【更多无水印蓝光原盘请访问 www.BBQDDQ.com】【更多无水印蓝光原盘请访问 www.BBQDDQ.com】.MP4", true},
		{"【更多无水印高清电影请访问 www.BBQDDQ.com】.MKV", true},
		// 正片不受影响
		{"骗不了人的男人.Softie.Conman.2022.1080p.MyTVSuper.WEB-DL.H265.AAC-QuickIO.mkv", false},
		{"Up.2009.1080p.BluRay.x264.mkv", false},
		// 站点水印前缀 + 番号：正片（START-635 实测误杀案例，水印剥掉后是干净番号）
		{"4k688.com@START-635.mp4", false},
		{"www.4k688.com@START-635.mp4", false},
		{"4k688.com-START-635.mp4", false},
		// 域名在中间剥不掉、或剥完剩广告词：真广告（START-635 离线包同款）
		{"18+游戏大全(996gg.cc)-七龍珠H版-三國志H版-三國群淫傳等.mp4", true},
		{"4k688.com@免费观看手机看片.mp4", true},
	}
	for _, c := range cases {
		if got := isAdOnlyVideo(c.name); got != c.want {
			t.Errorf("isAdOnlyVideo(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestParseEpisodeOnly(t *testing.T) {
	cases := []struct {
		name            string
		title           string
		season, episode int
		isTV            bool
	}{
		// 用户实测的剧集命名：EP 编号
		{"Baby.Walkure.Everyday.EP08.1080p.MAX.WEB-DL.DDP2.0.H.264-MagicStar.mkv", "Baby Walkure Everyday", 1, 8, true},
		{"Baby.Walkure.Everyday.EP12.1080p.MAX.WEB-DL.DDP2.0.H.264-MagicStar.mkv", "Baby Walkure Everyday", 1, 12, true},
		// E01 简写与中文集数
		{"Some.Show.E01.720p.mkv", "Some Show", 1, 1, true},
		{"某剧.第01集.1080p.mkv", "某剧", 1, 1, true},
		// S01E02 优先且不受影响
		{"Show.S02E03.1080p.mkv", "Show", 2, 3, true},
		// 电影防误伤：WALL.E / E.T 等含 E 的片名不得命中
		{"WALL.E.2008.1080p.BluRay.x264.mkv", "WALL E", 0, 0, false},
		{"E.T.1982.720p.BluRay.mkv", "E T", 0, 0, false},
		{"Edge.of.Tomorrow.2014.1080p.mkv", "Edge of Tomorrow", 0, 0, false},
	}
	for _, c := range cases {
		p := parseFileName(c.name)
		if p.Title != c.title || p.Season != c.season || p.Episode != c.episode || p.IsTV != c.isTV {
			t.Errorf("parseFileName(%q) = (title=%q S=%d E=%d TV=%v), want (%q S=%d E=%d TV=%v)",
				c.name, p.Title, p.Season, p.Episode, p.IsTV, c.title, c.season, c.episode, c.isTV)
		}
	}
}

func TestParseBracketEpisode(t *testing.T) {
	cases := []struct {
		name            string
		title           string
		season, episode int
		isTV            bool
	}{
		// 用户实测的动漫字幕组命名
		{"[Sakurato] Hamidashi Creative! [01v2][AVC-8bit 1080p AAC][CHT].mp4", "Hamidashi Creative!", 1, 1, true},
		{"[Sakurato] Hamidashi Creative! [12v2][AVC-8bit 1080p AAC][CHT].mp4", "Hamidashi Creative!", 1, 12, true},
		// 方括号裸集数
		{"[Sub] Some Anime [05][1080p].mkv", "Some Anime", 1, 5, true},
		// 防误伤：方括号年份不是集数；[REC] 电影名不剥前缀
		{"Some Movie [2001] Remastered.mkv", "", 0, 0, false},
		{"[REC].2007.720p.BluRay.mkv", "", 0, 0, false},
	}
	for _, c := range cases {
		p := parseFileName(c.name)
		if c.title != "" { // 只校验有预期标题的用例（防误伤用例只看季集）
			if p.Title != c.title {
				t.Errorf("parseFileName(%q).Title = %q, want %q", c.name, p.Title, c.title)
			}
		}
		if p.Season != c.season || p.Episode != c.episode || p.IsTV != c.isTV {
			t.Errorf("parseFileName(%q) = S=%d E=%d TV=%v, want S=%d E=%d TV=%v",
				c.name, p.Season, p.Episode, p.IsTV, c.season, c.episode, c.isTV)
		}
	}
	// 海报.png 应识别为标准图片（入库保留）而非垃圾
	if classifyFile("海报.png") != FileTypeStdImage {
		t.Errorf("classifyFile(海报.png) 应为标准图片")
	}
}

func TestIsStandardMediaImageName(t *testing.T) {
	yes := []string{"poster.jpg", "fanart.jpg", "banner.png", "logo.jpg",
		"season01-poster.jpg", "season-specials-poster.jpg", "clearart.png", "folder.jpg", "disc.png"}
	no := []string{"随便截图.jpg", "IMG_20240101.jpg", "video.mkv", "tvshow.nfo"}
	for _, n := range yes {
		if !isStandardMediaImageName(n) {
			t.Errorf("应识别为标准图片: %s", n)
		}
	}
	for _, n := range no {
		if isStandardMediaImageName(n) {
			t.Errorf("不应识别为标准图片: %s", n)
		}
	}
}
