package api

import (
	"errors"
	"testing"
)

func TestVersionNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v1.2.0", "v1.1.9", true},
		{"v1.2.0", "v1.2.0", false},
		{"v1.10.0", "v1.9.3", true},            // 按数字比，不是按字符串
		{"v1.2.0", "v1.2.0-3-gabc1234", false}, // 正式版之后的开发构建比它新
		{"v1.2.1", "v1.2.0-3-gabc1234", true},
		{"v1.2.0", "v1.2.0-rc1", true}, // 预发布比同号正式版旧
		{"v1.2.0", "v1.2.0-rc1-2-gabc1234", true},
		{"v1.2.0", "v1.1.0-5-gdeadbee-dirty", true},
		{"1.2.0", "v1.1.0", true}, // 有没有 v 前缀都认
		{"v1.2.0", "dev", false},  // 开发构建不提示
		{"v1.2.0", "abc1234", false},
		{"", "v1.0.0", false},
		// 日期写法：v26.10.9 是当天第一版，-2 -3 是同一天的第几次发版（正式版，不是预发布）
		{"v26.10.9", "v26.10.9-4", false}, // 序号版比当天第一版新，不能提示「更新」回去
		{"v26.10.9", "v26.10.9-4-1-gabc1234", false},
		{"v26.10.9-4", "v26.10.9", true},
		{"v26.10.9-4", "v26.10.9-3", true},
		{"v26.10.9-10", "v26.10.9-9", true}, // 按数字比
		{"v26.10.9-4", "v26.10.9-4", false},
		{"v26.10.9-4", "v26.10.9-3-2-gabc1234", true},
		{"v26.10.9-4", "v26.10.9-4-2-gabc1234", false},
		{"v26.10.9-4", "v26.10.9-5-gabc1234", true}, // v26.10.9 之后 5 个提交的构建，仍不如 -4
		{"v26.10.10", "v26.10.9-7", true},
		{"v26.10.9-2", "v26.10.9-rc1", true},
	}
	for _, c := range cases {
		if got := versionNewer(c.latest, c.current); got != c.want {
			t.Errorf("versionNewer(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestUpdateStatus(t *testing.T) {
	oldFetch, oldVer := fetchLatestRelease, buildVersion
	defer func() {
		fetchLatestRelease, buildVersion = oldFetch, oldVer
		updateLatest, updateErr = nil, ""
	}()

	buildVersion = "v1.0.0"
	fetchLatestRelease = func() (*githubRelease, error) {
		return &githubRelease{TagName: "v1.1.0", Body: "修了点东西", HTMLURL: "https://example/r"}, nil
	}
	checkUpdate()
	st := currentUpdateStatus()
	if !st.HasUpdate || st.Latest != "v1.1.0" || st.Notes != "修了点东西" || !st.Comparable {
		t.Fatalf("有新版本时状态不对：%+v", st)
	}

	// 检查失败：保留上次拉到的版本，只报错误
	fetchLatestRelease = func() (*githubRelease, error) { return nil, errors.New("超时") }
	checkUpdate()
	st = currentUpdateStatus()
	if st.Error != "超时" || st.Latest != "v1.1.0" {
		t.Fatalf("检查失败应保留旧结果：%+v", st)
	}

	// 仓库还没发过版（404 → nil, nil）
	fetchLatestRelease = func() (*githubRelease, error) { return nil, nil }
	checkUpdate()
	if st = currentUpdateStatus(); st.HasUpdate || st.Latest != "" || st.Error != "" {
		t.Fatalf("没有 Release 时不应提示：%+v", st)
	}
}
