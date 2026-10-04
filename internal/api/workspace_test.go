package api

import (
	"strings"
	"testing"
)

// fakeResolve 按 cid → 路径表解析，并记下每次调用（验证查询数）
type fakeResolve struct {
	paths map[string]string
	calls []string
}

func (f *fakeResolve) fn(cid string, fresh bool) (string, error) {
	tag := cid
	if fresh {
		tag += "!"
	}
	f.calls = append(f.calls, tag)
	p, ok := f.paths[cid]
	if !ok {
		return "", errDirGone
	}
	return p, nil
}

func wsSlotsOf(lib, share, pending, existing, redundant string) []wsSlot {
	return []wsSlot{
		{"library", "115 媒体库目录", lib},
		{"share", "转存目录", share},
		{"pending", "待整理目录", pending},
		{"existing", "已存在目录", existing},
		{"redundant", "冗余目录", redundant},
	}
}

func TestWsContains(t *testing.T) {
	cases := []struct {
		b, a string
		want bool
	}{
		{"/影视", "/影视", true},
		{"/影视", "/影视/剧集", true},
		{"/影视", "/影视库", false}, // 前缀相同但不是子目录
		{"/", "/任何", true},
		{"/影视/剧集", "/影视", false},
	}
	for _, c := range cases {
		if got := wsContains(c.b, c.a); got != c.want {
			t.Errorf("wsContains(%q,%q)=%v want %v", c.b, c.a, got, c.want)
		}
	}
}

func TestCheckWorkspace_NoChangeNoQuery(t *testing.T) {
	f := &fakeResolve{paths: map[string]string{"1": "/影视", "2": "/影视/待整理"}}
	// 即使两者重叠，没改动也不查、不拦
	err := checkWorkspaceSlots(wsSlotsOf("1", "", "2", "", ""), map[string]bool{}, f.fn)
	if err != nil || len(f.calls) != 0 {
		t.Fatalf("err=%v calls=%v", err, f.calls)
	}
}

func TestCheckWorkspace_SameCidNoQuery(t *testing.T) {
	f := &fakeResolve{paths: map[string]string{}}
	err := checkWorkspaceSlots(wsSlotsOf("1", "5", "5", "", ""), map[string]bool{"pending": true}, f.fn)
	if err == nil || !strings.Contains(err.Error(), "同一个目录") {
		t.Fatalf("err=%v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("同 cid 不该打接口: %v", f.calls)
	}
}

func TestCheckWorkspace_WorkspaceInsideLibrary(t *testing.T) {
	f := &fakeResolve{paths: map[string]string{"1": "/未处理影视", "2": "/未处理影视/转存"}}
	err := checkWorkspaceSlots(wsSlotsOf("1", "2", "", "", ""), map[string]bool{"share": true}, f.fn)
	if err == nil || !strings.Contains(err.Error(), "转存目录") || !strings.Contains(err.Error(), "之内") {
		t.Fatalf("err=%v", err)
	}
}

func TestCheckWorkspace_LibraryInsideWorkspace(t *testing.T) {
	f := &fakeResolve{paths: map[string]string{"1": "/下载/影视库", "2": "/下载"}}
	err := checkWorkspaceSlots(wsSlotsOf("1", "", "2", "", ""), map[string]bool{"library": true}, f.fn)
	if err == nil || !strings.Contains(err.Error(), "115 媒体库目录（/下载/影视库）位于 待整理目录") {
		t.Fatalf("err=%v", err)
	}
}

func TestCheckWorkspace_FreshOnlyForChanged(t *testing.T) {
	f := &fakeResolve{paths: map[string]string{"1": "/影视", "2": "/S/转存", "3": "/S/待整理"}}
	err := checkWorkspaceSlots(wsSlotsOf("1", "2", "3", "", ""), map[string]bool{"pending": true}, f.fn)
	if err != nil {
		t.Fatal(err)
	}
	fresh := 0
	for _, c := range f.calls {
		if strings.HasSuffix(c, "!") {
			fresh++
		}
	}
	if fresh != 1 || len(f.calls) != 3 {
		t.Fatalf("每个目录至多一次、只有改动的强制重查: %v", f.calls)
	}
}

func TestCheckWorkspace_UnchangedPairIgnored(t *testing.T) {
	// 已存在与冗余旧配置重叠，但这次只改了转存，不拦
	f := &fakeResolve{paths: map[string]string{"1": "/影视", "2": "/S/转存", "4": "/S/x", "5": "/S/x/y"}}
	err := checkWorkspaceSlots(wsSlotsOf("1", "2", "", "4", "5"), map[string]bool{"share": true}, f.fn)
	if err != nil {
		t.Fatal(err)
	}
}

func TestCheckWorkspace_GoneDirs(t *testing.T) {
	f := &fakeResolve{paths: map[string]string{"1": "/影视"}}
	// 改动过的目录不存在 → 拒绝
	err := checkWorkspaceSlots(wsSlotsOf("1", "9", "", "", ""), map[string]bool{"share": true}, f.fn)
	if err == nil || !strings.Contains(err.Error(), "已不存在") {
		t.Fatalf("err=%v", err)
	}
	// 没改动的旧目录不存在 → 跳过它，不拦这次保存
	f2 := &fakeResolve{paths: map[string]string{"1": "/影视", "2": "/S/转存"}}
	if err := checkWorkspaceSlots(wsSlotsOf("1", "2", "9", "", ""), map[string]bool{"share": true}, f2.fn); err != nil {
		t.Fatal(err)
	}
}

func TestCheckWorkspace_RootCid(t *testing.T) {
	f := &fakeResolve{paths: map[string]string{"1": "/影视"}}
	err := checkWorkspaceSlots(wsSlotsOf("1", "0", "", "", ""), map[string]bool{"share": true}, f.fn)
	if err == nil || !strings.Contains(err.Error(), "网盘根") {
		t.Fatalf("网盘根不能作为工作目录, err=%v", err)
	}
	if len(f.calls) != 0 { // 直接拦下，不打接口
		t.Fatalf("calls=%v", f.calls)
	}
	// 只配了这一个目录时互斥比较无从拦起，同样要拒绝
	if err := checkWorkspaceSlots(wsSlotsOf("", "0", "", "", ""), map[string]bool{"share": true}, f.fn); err == nil {
		t.Fatal("单独把转存目录选成网盘根也必须拒绝")
	}
	// 没改动的旧配置是根不拦（别因为它存不了别的目录）
	if err := checkWorkspaceSlots(wsSlotsOf("1", "0", "2", "", ""), map[string]bool{"pending": true}, (&fakeResolve{paths: map[string]string{"1": "/影视", "2": "/待整理"}}).fn); err != nil && strings.Contains(err.Error(), "不能选网盘根") {
		t.Fatalf("未改动的根不该被这条拦下: %v", err)
	}
}

func TestWorkspaceSlotsFromSettings(t *testing.T) {
	vals := map[string]string{
		"full":      `{"cid":"1","local_path":"/media"}`,
		"share":     `{"folder":"2"}`,
		"org-basic": `{"pending":"3","existing":"","redundant":"5","enrich":{}}`,
	}
	s := workspaceSlots(func(k string) string { return vals[k] })
	got := []string{}
	for _, x := range s {
		got = append(got, x.key+"="+x.cid)
	}
	if strings.Join(got, ",") != "library=1,share=2,pending=3,existing=,redundant=5" {
		t.Fatalf("got %v", got)
	}
}
