package api

import "testing"

func TestIsAVAdFile(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		// 用户实测的广告视频：清洗后仍残留域名和广告词
		{"18+游戏大全(996gg.cc)-七龍珠H版-三國志H版-三國群淫傳等.mp4", true},
		// 纯关键词（无域名）
		{"18+游戏大全.mp4", true},
		{"福利社导航.mp4", true},
		{"18+游戏大全(996gg.cc).mp4", true},
		// 正片：广告前缀被 sanitize 清掉后无残留 → 不是广告
		{"4k688.com@START-622.mp4", false},
		{"www.avbase.net@MIDV-001.mp4", false},
		{"【高清剧集网发布】START-622.mp4", false},
		{"START-622 - www.4k688.com.mp4", false},
		{"最新地址START-622.mp4", false},
		// 干净正片/分卷/字幕
		{"START-622.mp4", false},
		{"START-622-CD2.mp4", false},
		{"FC2-PPV-1234567.mp4", false},
		{"START-622.chs.srt", false},
	}
	for _, c := range cases {
		if got := isAVAdFile(c.name); got != c.want {
			t.Errorf("isAVAdFile(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAVCarriesNumber(t *testing.T) {
	cases := []struct {
		cleaned, avNum string
		want           bool
	}{
		// 正片及分卷变体
		{"START-622", "START-622", true},
		{"start-622-c", "START-622", true},
		{"START622", "START-622", true}, // 无连字符
		{"start_622_cd1", "START-622", true},
		// FC2 两种写法
		{"FC2-PPV-1234567", "FC2-PPV-1234567", true},
		{"FC2-1234567", "FC2-PPV-1234567", true},
		{"fc2_1234567", "FC2-PPV-1234567", true},
		// 引流视频（清洗后不带番号）
		{"18+游戏大全(996gg.cc)-七龍珠H版-三國志H版-三國群淫傳等", "START-622", false},
		{"公布", "START-622", false},
		{"预告片", "START-622", false},
	}
	for _, c := range cases {
		if got := avCarriesNumber(c.cleaned, c.avNum); got != c.want {
			t.Errorf("avCarriesNumber(%q, %q) = %v, want %v", c.cleaned, c.avNum, got, c.want)
		}
	}
}
