package api

import "testing"

func TestMatchFieldCommaList(t *testing.T) {
	cases := []struct {
		name, cond, value string
		want              bool
	}{
		// 单值（原有语义不变）
		{"Movie.1080p.BluRay", "1080p", "", true},
		{"Movie.2160p.BluRay", "1080p", "", false},
		// 正值列表：任一命中即通过
		{"Movie.2160p.BluRay", "2160p,4k", "", true},
		{"Movie.1080p.BluRay", "2160p,4k", "", false},
		{"Movie.4K.BluRay", "2160p,4k", "", true},
		// 排除列表：任一命中即不通过（用户实测配置 "!DV.HDR,!DV"）
		{"Movie.1080p.BluRay.DV.HDR", "!DV.HDR,!DV", "", false},
		{"Movie.1080p.BluRay.DV", "!DV.HDR,!DV", "", false},
		{"Movie.1080p.BluRay.HDR10", "!DV.HDR,!DV", "", true},
		// 单排除（原有语义）
		{"Movie.1080p.DV", "!DV", "", false},
		{"Movie.1080p", "!DV", "", true},
		// 空 = 不限
		{"Movie.1080p", "", "", true},
		// 值字段（如发布组从提取值命中）
		{"Movie.1080p-WiKi", "WiKi", "WiKi", true},
	}
	for _, c := range cases {
		if got := matchField(c.name, c.cond, c.value); got != c.want {
			t.Errorf("matchField(%q, %q) = %v, want %v", c.name, c.cond, got, c.want)
		}
	}
}

func TestWashDecisionUserConfig(t *testing.T) {
	// 用户实际配置的优先级：WiKi1080p蓝光 > 1080p蓝光 > 2160p蓝光 > 1080pWEB > 2160pWEB
	rules := []washRule{
		{ResourceTeam: "WiKi", ResourcePix: "1080p", ResourceType: "BluRay", ResourceEffect: "!DV.HDR,!DV"},
		{ResourcePix: "1080p", ResourceType: "BluRay", ResourceEffect: "!DV.HDR,!DV"},
		{ResourcePix: "2160p", ResourceType: "BluRay", ResourceEffect: "!DV.HDR,!DV"},
		{ResourcePix: "1080p", ResourceType: "WEB-DL", ResourceEffect: "!DV.HDR,!DV"},
		{ResourcePix: "2160p", ResourceType: "WEB-DL", ResourceEffect: "!DV.HDR,!DV"},
	}
	cases := []struct {
		newName, oldName string
		want             bool // true=新版替换
	}{
		// 1080p 蓝光应替换 2160p 蓝光（用户想要的"优先保留1080P"）
		{"片名.2024.1080p.BluRay.H264", "片名.2024.2160p.BluRay.HDR", true},
		// 2160p 蓝光不应替换 1080p 蓝光
		{"片名.2024.2160p.BluRay.H264", "片名.2024.1080p.BluRay.H264", false},
		// 1080p WEB 不敌 2160p 蓝光（来源优先）
		{"片名.2024.1080p.WEB-DL.H264", "片名.2024.2160p.BluRay.H264", false},
		// WEB 内 1080p 优先
		{"片名.2024.1080p.WEB-DL.H264", "片名.2024.2160p.WEB-DL.H264", true},
		// DV 版不敌非 DV 版（排除列表生效）
		{"片名.2024.1080p.BluRay.DV.HDR", "片名.2024.1080p.BluRay.H264", false},
		// WiKi 档最高
		{"片名.2024.1080p.BluRay.WiKi", "片名.2024.1080p.BluRay.FRDS", true},
	}
	for _, c := range cases {
		if got := washDecision(c.newName, []string{c.oldName}, rules); got != c.want {
			t.Errorf("washDecision(new=%q, old=%q) = %v, want %v", c.newName, c.oldName, got, c.want)
		}
	}
}
