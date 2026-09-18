package api

import "testing"

func TestSanitizeAVFilename(t *testing.T) {
	cases := map[string]string{
		// 域名@分隔
		"4k688.com@START-622.mp4":            "START-622.mp4",
		"www.avbase.net@MIDV-001.mp4":        "MIDV-001.mp4",
		// 域名-分隔（无@）
		"javbus.com-START-100.mp4":           "START-100.mp4",
		// 全角括号广告
		"【高清剧集网发布】START-622.mp4":      "START-622.mp4",
		"【广告】MIDV-002.mp4":                "MIDV-002.mp4",
		// 半角括号广告
		"(www.4k688.com)START-101.mp4":       "START-101.mp4",
		"[4k688.com]SSIS-100.mp4":            "SSIS-100.mp4",
		// 文字广告前缀
		"高清剧集网START-622.mp4":              "START-622.mp4",
		"中文字幕MIDV-003.mp4":                "MIDV-003.mp4",
		// 域名后缀
		"START-622 - www.4k688.com.mp4":      "START-622.mp4",
		"SSIS-101_www.javbus.com.mp4":        "SSIS-101.mp4",
		// 干净的文件名不变
		"START-622.mp4":                      "START-622.mp4",
		"MIDV-001.mp4":                       "MIDV-001.mp4",
		// 字幕文件
		"4k688.com@START-622.chs.srt":        "START-622.chs.srt",
	}
	for input, want := range cases {
		if got := sanitizeAVFilename(input); got != want {
			t.Errorf("sanitizeAVFilename(%q) = %q, want %q", input, got, want)
		}
	}
}
