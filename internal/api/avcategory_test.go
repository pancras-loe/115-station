package api

import "testing"

func TestParseAVCategoriesFromYAML(t *testing.T) {
	src := `movie:
  电影/外语电影:
av:
  无码:
    num_prefix: 'FC2,HEYZO'
  国产:
    num_prefix: 'MD,PMC'
  有码:
    num_prefix: ''
`
	cats := parseAVCategoriesFromYAML(src)
	if len(cats) != 3 {
		t.Fatalf("want 3 av categories, got %d", len(cats))
	}
	if cats[0].Name != "无码" || len(cats[0].Prefixes) != 2 {
		t.Errorf("无码 parse wrong: %+v", cats[0])
	}
	if len(cats[2].Prefixes) != 0 {
		t.Errorf("有码 应为兜底（空前缀）: %+v", cats[2])
	}
	// 无 av 段
	if got := parseAVCategoriesFromYAML("movie:\n  电影/外语电影:\n"); got != nil {
		t.Errorf("无 av 段应返回 nil, got %v", got)
	}
}

func TestClassifyAVNumber(t *testing.T) {
	// 测试用固定分类（绕过 DB 读取）：直接构造分类切片走同序逻辑
	// classifyAVNumber 内部 loadAVCategories 走 DB——DB 未初始化时
	// 回退默认（无码/有码），恰好可用于测内置库与兜底
	cases := []struct {
		num, hint, want string
	}{
		{"FC2-PPV-123", "", "无码"},          // 内置无码库
		{"HEYZO-2345", "", "无码"},            // 内置无码库
		{"n1093", "", "无码"},                 // Tokyo Hot（N10 前缀）
		{"MDX-005", "", "有码"},               // 内置国产库 → 默认配置无"国产"分类 → 落兜底有码
		{"MIDA-732", "【某某】MIDA-732 无码破解", "无码"}, // 文件名关键词
		{"ABC-123", "", "有码"},               // 兜底
		{"START-620", "", "有码"},             // 兜底（START 不再是无码内置）
	}
	for _, c := range cases {
		if got := classifyAVNumber(c.num, c.hint); got != c.want {
			t.Errorf("classify(%q, %q) = %q, want %q", c.num, c.hint, got, c.want)
		}
	}
}

func TestCollapseDuplicateAVNum(t *testing.T) {
	cases := []struct{ in, num, want string }{
		{"ABC-123ABC-123.mp4", "ABC-123", "ABC-123.mp4"},
		{"ABC-123-ABC-123.mp4", "ABC-123", "ABC-123.mp4"},
		{"ABC-123_ABC-123.mp4", "ABC-123", "ABC-123.mp4"},
		{"ABC-123 ABC-123.mp4", "ABC-123", "ABC-123.mp4"},
		{"A/ABC-123/ABC-123-ABC-123 - 2160p.mp4", "ABC-123", "A/ABC-123/ABC-123 - 2160p.mp4"},
		{"ABC-123 - 2160p.mp4", "ABC-123", "ABC-123 - 2160p.mp4"},   // 正常名不受影响
		{"START-622-4K.mp4", "ABC-123", "START-622-4K.mp4"},          // 不同番号不误伤
	}
	for _, c := range cases {
		if got := collapseDuplicateAVNum(c.in, c.num); got != c.want {
			t.Errorf("collapseDuplicateAVNum(%q, %q) = %q, want %q", c.in, c.num, got, c.want)
		}
	}
}

func TestAVCarriesNumberPadded(t *testing.T) {
	cases := []struct {
		cleaned, num string
		want         bool
	}{
		// JUVR-303 实测误杀案例：数字段补零到 5 位 + 分卷后缀
		{"juvr00303_1_8k", "JUVR-303", true},
		{"juvr00303_2_8k", "JUVR-303", true},
		{"juvr00303", "JUVR-303", true},
		// 常规形态回归
		{"start-622", "START-622", true},
		{"start622", "START-622", true},
		{"START-622-CD1", "START-622", true},
		{"fc2_1234567", "FC2-PPV-1234567", true},
		// 防误报：补零后的数字段必须精确相等，前缀相同数字不同的番号不命中
		{"juvr00303", "JUVR-30", false},
		{"juvr00303", "JUVR-3030", false},
		{"start622", "START-623", false},
		{"midv001", "MIDV-100", false},
	}
	for _, c := range cases {
		if got := avCarriesNumber(c.cleaned, c.num); got != c.want {
			t.Errorf("avCarriesNumber(%q, %q) = %v, want %v", c.cleaned, c.num, got, c.want)
		}
	}
}
