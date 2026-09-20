package api

import (
	"os"
	"testing"
	"time"

	"115-station/internal/config"
	"115-station/internal/model"
)

// setRecognize 写一份「识别规则」配置并让发布组缓存立即失效
func setRecognize(t *testing.T, json string) {
	t.Helper()
	notifyConfigSource = &config.Config{DataDir: t.TempDir(), ConfigDir: t.TempDir()}
	if err := notifyConfigSource.SaveSetting("org-recognize", json); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}
	customTeamMu.Lock()
	customTeamsAt = time.Time{}
	customTeamMu.Unlock()
	t.Cleanup(func() {
		_ = os.RemoveAll(notifyConfigSource.DataDir)
		notifyConfigSource = nil
		model.DB = nil
		customTeamMu.Lock()
		customTeams, customTeamsAt = nil, time.Time{}
		customTeamMu.Unlock()
	})
}

func TestReplaceRulesTextAndRegex(t *testing.T) {
	setRecognize(t, `{"replace_rules":[{"from":"【[^】]*】","to":"","regex":true},{"from":"4K修复版","to":"4K"},{"from":"^(?:www[.])?[A-Za-z0-9-]+[.](?:com|cc)@","to":"","regex":true},{"from":"([0-9]{4})年","to":"$1","regex":true},{"from":"[","to":"x","regex":true}]}`)

	rules := loadReplaceRules()
	// 最后一条正则编译不过，必须被丢掉而不是拖垮整组
	if len(rules) != 4 {
		t.Fatalf("loadReplaceRules 返回 %d 条，want 4（坏正则应被丢弃）", len(rules))
	}

	cases := []struct{ in, want string }{
		{"【高清影视之家】蜘蛛侠.2021.mkv", "蜘蛛侠.2021.mkv"},
		{"www.abc.com@蜘蛛侠.2021.mkv", "蜘蛛侠.2021.mkv"},
		{"abc.cc@复仇者.4K修复版.mkv", "复仇者.4K.mkv"},
		{"流浪地球.2019年.1080p.mkv", "流浪地球.2019.1080p.mkv"}, // 捕获组 $1
		{"干净的名字.2020.1080p.mkv", "干净的名字.2020.1080p.mkv"},
	}
	for _, c := range cases {
		if got := applyReplaceRules(c.in, rules); got != c.want {
			t.Errorf("applyReplaceRules(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 规则按配置顺序依次套用，后一条作用在前一条的结果上
func TestReplaceRulesOrdered(t *testing.T) {
	setRecognize(t, `{"replace_rules":[{"from":"A","to":"B"},{"from":"B","to":"C"}]}`)
	if got := applyReplaceRules("A", loadReplaceRules()); got != "C" {
		t.Errorf("链式替换 = %q, want C", got)
	}
}

func TestMinSizeFromRecognizeConfig(t *testing.T) {
	setRecognize(t, `{"min_size":120}`)
	if got := loadRecognizeConfig().MinSize; got != 120 {
		t.Errorf("MinSize = %d, want 120", got)
	}
}

func TestCustomReleaseGroups(t *testing.T) {
	setRecognize(t, `{"release_groups":["WiKi","CR","压制组"]}`)

	cases := []struct{ name, want string }{
		// 方括号里的发布组：reTeam 的「末尾 -GROUP」取不到，靠名单认出来
		{"[WiKi] Some.Show.S01E01.1080p.mkv", "WiKi"},
		// 点号分隔、大小写不敏感，回填名单里的写法
		{"Some.Show.2021.1080p.wiki.mkv", "WiKi"},
		// 完整片段才算：CRUNCHYROLL 里的 CR 不能命中（退回通用启发式取末尾的 DL）
		{"Some.Show.2021.CRUNCHYROLL.WEB-DL.mkv", "DL"},
		{"Some.Show.2021.CR.WEB-DL.mkv", "CR"},
		// 中文组名，前后是中文也算边界
		{"某剧集第01集压制组出品.mkv", "压制组"},
		// 名单没命中时退回原来的「末尾 -GROUP」启发式
		{"Some.Show.2021.1080p.WEB-DL-TnT.mkv", "TnT"},
	}
	for _, c := range cases {
		if got := ParseResourceInfo(c.name).Team; got != c.want {
			t.Errorf("ParseResourceInfo(%q).Team = %q, want %q", c.name, got, c.want)
		}
	}
}
