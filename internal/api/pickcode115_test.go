package api

import "testing"

// 真实数据：探测某账号「电影」库时抓到的一条记录，
// fid=3518330214302090865 / pc=bii7yrgpt7y9683of，两者必须能互推。
const (
	realPickcode = "bii7yrgpt7y9683of"
	realFileID   = uint64(3518330214302090865)
	realStable   = "02hr"
	// 同账号「影视」目录 cid 3165030031459331046 对应的目录 pickcode，
	// 已实际调用 /app/chrome/downfolders 验证接口接受
	realDirID       = uint64(3165030031459331046)
	realDirPickcode = "fazhzfrq67jc514tly"
)

func TestPickcodeToID(t *testing.T) {
	got, err := pickcodeToID115(realPickcode)
	if err != nil {
		t.Fatalf("pickcodeToID115: %v", err)
	}
	if got != realFileID {
		t.Fatalf("id = %d, 期望 %d", got, realFileID)
	}
}

func TestStablePoint(t *testing.T) {
	got, err := stablePoint115(realPickcode)
	if err != nil {
		t.Fatalf("stablePoint115: %v", err)
	}
	if got != realStable {
		t.Fatalf("不动点 = %q, 期望 %q", got, realStable)
	}
}

func TestDirPickcode(t *testing.T) {
	got, err := dirPickcode115(realDirID, realStable)
	if err != nil {
		t.Fatalf("dirPickcode115: %v", err)
	}
	if got != realDirPickcode {
		t.Fatalf("目录 pickcode = %q, 期望 %q", got, realDirPickcode)
	}
}

// 生成的目录 pickcode 必须能被自己解析回原 id，且不动点保持一致
func TestDirPickcodeRoundTrip(t *testing.T) {
	for _, id := range []uint64{1, 36, 12345, realDirID, realFileID} {
		pc, err := dirPickcode115(id, realStable)
		if err != nil {
			t.Fatalf("id=%d: %v", id, err)
		}
		back, err := pickcodeToID115(pc)
		if err != nil {
			t.Fatalf("id=%d pc=%s 回解失败: %v", id, pc, err)
		}
		if back != id {
			t.Fatalf("id=%d → %s → %d，不闭合", id, pc, back)
		}
		sp, err := stablePoint115(pc)
		if err != nil {
			t.Fatalf("id=%d pc=%s 不动点推导失败: %v", id, pc, err)
		}
		if sp != realStable {
			t.Fatalf("id=%d pc=%s 不动点 = %q, 期望 %q", id, pc, sp, realStable)
		}
	}
}

// 十张替换表都必须是 36 个互不重复字符的全排列，
// 抄错一位会让整条快速同步链路静默算出错误的 pickcode
func TestPlainTablesArePermutations(t *testing.T) {
	if len(pcPlainTab) != 10 {
		t.Fatalf("替换表数量 = %d, 期望 10", len(pcPlainTab))
	}
	for prefix, plain := range pcPlainTab {
		if len(plain) != 36 {
			t.Fatalf("表 %s 长度 = %d, 期望 36", prefix, len(plain))
		}
		seen := map[byte]bool{}
		for i := 0; i < len(plain); i++ {
			c := plain[i]
			if seen[c] {
				t.Fatalf("表 %s 中字符 %q 重复", prefix, c)
			}
			seen[c] = true
			if !isPcAlphabet(c) {
				t.Fatalf("表 %s 含非法字符 %q", prefix, c)
			}
		}
	}
	// 每张表的「'0' 加密后的字符」必须互不相同，否则无法据此反推前缀
	if len(pcSuffixHead) != 10 {
		t.Fatalf("前缀反查表数量 = %d, 期望 10（说明有表的不动点首字符撞了）", len(pcSuffixHead))
	}
}

func isPcAlphabet(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z')
}

func TestB36(t *testing.T) {
	cases := []struct {
		n uint64
		s string
	}{{0, "0"}, {1, "1"}, {35, "z"}, {36, "10"}, {realFileID, "qqavyz6xave9"}}
	for _, c := range cases {
		if got := pcB36Encode(c.n); got != c.s {
			t.Fatalf("pcB36Encode(%d) = %q, 期望 %q", c.n, got, c.s)
		}
		back, err := pcB36Decode(c.s)
		if err != nil {
			t.Fatalf("pcB36Decode(%q): %v", c.s, err)
		}
		if back != c.n {
			t.Fatalf("pcB36Decode(%q) = %d, 期望 %d", c.s, back, c.n)
		}
	}
}

func TestStablePointRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"", "abc", "!!!!!!", "zzzz"} {
		if _, err := stablePoint115(bad); err == nil {
			t.Fatalf("stablePoint115(%q) 应当报错", bad)
		}
	}
}
