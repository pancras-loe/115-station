package api

import (
	"testing"

	"115-station/internal/model"
)

// 目录上的别的名字：包装目录不算，和片名一样的不重复
func TestAltTitlesSkipWrapper(t *testing.T) {
	dirs := pathDirs("Pegasus.2019.1080p", "飞驰人生 (2019) ·订阅38-9")
	got, _ := mergePathContext(parseFileName("Pegasus.2019.1080p.mkv"), parseDirs(dirs, nil), dirs)
	if len(got.Alts) != 0 {
		t.Fatalf("包装目录与同名目录都不该进 Alts，得到 %+v", got.Alts)
	}
	dirs = pathDirs("天马行空.1990/Pegasus.2019.1080p")
	got, _ = mergePathContext(parseFileName("Pegasus.2019.1080p.mkv"), parseDirs(dirs, nil), dirs)
	if len(got.Alts) != 1 || got.Alts[0].Title != "天马行空" || got.Alts[0].Year != "1990" {
		t.Fatalf("Alts 期望只有 天马行空 1990，得到 %+v", got.Alts)
	}
}

func TestSubWrapperIDOf(t *testing.T) {
	if id := subWrapperIDOf([]string{"Pegasus.2019", "飞驰人生 (2019) ·订阅38-9"}); id != 38 {
		t.Fatalf("得到 %d，期望 38", id)
	}
	if id := subWrapperIDOf([]string{"三体 (2023) S01 ·订阅12-3"}); id != 12 {
		t.Fatalf("得到 %d，期望 12", id)
	}
	if subWrapperIDOf([]string{"订阅38-9", "飞驰人生"}) != 0 || isSubWrapperName("我的订阅1-2 合集") {
		t.Fatal("不是包装目录的名字不该认")
	}
}

// 订阅核对只认内容自己写的名字与年份，包装目录的不算
func TestSubHintMatch(t *testing.T) {
	movie := &model.Subscription{TmdbID: 575219, MediaType: "movie", Title: "飞驰人生", Year: "2019"}
	names := []string{"飞驰人生", "飞驰人生", "Pegasus"}
	ev := func(file, path, parent string) []subEvidence {
		dirs := pathDirs(path, parent)
		return subEvidenceOf(parseFileName(file), parseDirs(dirs, nil), dirs)
	}
	cases := []struct {
		name string
		ev   []subEvidence
		sub  *model.Subscription
		want bool
	}{
		{"英文名与年份都对得上", ev("Pegasus.2019.2160p.mp4", "Pegasus.2019.2160p", "飞驰人生 (2019) ·订阅38-9"), movie, true},
		{"年份差一年也算", ev("Pegasus.2020.1080p.mp4", "", "飞驰人生 (2019) ·订阅38-9"), movie, true},
		{"年份对不上", ev("Pegasus.2009.1080p.mp4", "", "飞驰人生 (2019) ·订阅38-9"), movie, false},
		{"内容没写年份：不认", ev("Pegasus.1080p.mp4", "", "飞驰人生 (2019) ·订阅38-9"), movie, false},
		{"片名只是包含：不认", ev("Pegasus.Pony.With.a.Broken.Wing.2019.mkv", "", "飞驰人生 (2019) ·订阅38-9"), movie, false},
		{"只有包装目录写了名字：不认", ev("01.mkv", "", "飞驰人生 (2019) ·订阅38-9"), movie, false},
		// 凡人修仙传：订阅 2020 动画，内容是 2025 真人剧（subtwin.go 的现场）
		{"同名剧年份不同", ev("凡人修仙传.2025.E01.2160p.mkv", "", "凡人修仙传 (2020) S01 ·订阅7-1"),
			&model.Subscription{TmdbID: 106449, MediaType: "tv", Title: "凡人修仙传", Year: "2020"}, false},
		{"剧集后面的季年份晚于首播", ev("The.Boys.S04E01.2024.mkv", "", "黑袍纠察队 (2019) S04 ·订阅9-1"),
			&model.Subscription{TmdbID: 76479, MediaType: "tv", Title: "黑袍纠察队", Year: "2019"}, true},
	}
	for _, c := range cases {
		n := names
		if c.sub.TmdbID == 106449 {
			n = []string{"凡人修仙传", "凡人修仙传"}
		}
		if c.sub.TmdbID == 76479 {
			n = []string{"黑袍纠察队", "The Boys"}
		}
		_, got := subHintMatch(c.ev, c.sub, n)
		if got != c.want {
			t.Errorf("%s：得到 %v，期望 %v", c.name, got, c.want)
		}
	}
}
