package api

import "testing"

// 真实世界番号变体矩阵：当前识别能力盘点
func TestDetectMatrix(t *testing.T) {
	cases := []struct {
		name string
		dir  string
		want string
	}{
		{"标准", "MIDV-001", "MIDV-001"},
		{"无连字符", "MIDV001", "MIDV-001"},
		{"中字后缀", "hmn-898ch", "HMN-898"},
		{"无码后缀", "MIDA001uc", "MIDA-001"},
		{"流出后缀", "SSIS-702leak", "SSIS-702"},
		{"4K后缀", "SSIS-7024k", "SSIS-702"},
		{"分卷后缀", "ABC-123-CD2", "ABC-123"},
		{"垃圾前缀", "4k688.com@START-622", "START-622"},
		{"FC2变体", "fc2_ppv_1234567", "FC2-PPV-1234567"},
		{"数字前缀系列", "259LUXU-666", "259LUXU-666"},
		{"补零番号", "JUVR-00303", "JUVR-00303"},
		{"排除前缀", "EP01xxx", ""},
		{"纯画质", "1080px265", ""},
	}
	for _, c := range cases {
		if got := detectAVNumber(c.dir, c.dir+".mp4"); got != c.want {
			t.Errorf("%-12s detectAVNumber(%q) = %q, want %q", c.name, c.dir, got, c.want)
		}
	}
}
