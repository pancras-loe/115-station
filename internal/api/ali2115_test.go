package api

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// ---- 签名 ----

func TestSecpBasePoint(t *testing.T) {
	// 私钥 1 的公钥就是基点 G
	if got := aliPubKeyHex(big.NewInt(1)); got != strings.ToLower("79BE667EF9DCBBAC55A06295CE870B07029BFCDB2DCE28D959F2815B16F81798483ADA7726A3C4655DA4FBFC0E1108A8FD17B448A68554199C47D08FFB10D4B8") {
		t.Fatalf("1·G 不对: %s", got)
	}
	// 2G 的已知坐标
	two := secpMul(secpG(), big.NewInt(2))
	if fmt.Sprintf("%X", two.x) != "C6047F9441ED7D6D3045406E95C07CD85C778E4B8CEF3CA7ABAC09B95C709EE5" {
		t.Fatalf("2·G 不对: %X", two.x)
	}
	// n·G = 无穷远
	if !secpMul(secpG(), secpN).inf {
		t.Fatal("n·G 应该是无穷远点")
	}
}

func TestSecpSignVerify(t *testing.T) {
	dev, d := aliDeviceKey("user-123")
	sum := sha256.Sum256([]byte("user-123"))
	if dev != hex.EncodeToString(sum[:]) {
		t.Fatal("deviceId 应该是 sha256(user_id)")
	}
	q := secpMul(secpG(), d)
	digest := sha256.Sum256([]byte("hello"))
	for range 5 {
		sig, err := secpSign(d, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		if len(sig) != 65 {
			t.Fatalf("签名长度 %d", len(sig))
		}
		if s := new(big.Int).SetBytes(sig[32:64]); s.Cmp(new(big.Int).Rsh(secpN, 1)) > 0 {
			t.Fatal("s 应该在低半区")
		}
		if !secpVerify(q, digest[:], sig) {
			t.Fatal("自己签的验不过")
		}
		bad := sha256.Sum256([]byte("hellO"))
		if secpVerify(q, bad[:], sig) {
			t.Fatal("换了内容还验得过")
		}
	}
	if len(aliPubKeyHex(d)) != 128 {
		t.Fatal("公钥应是 64 字节 hex")
	}
}

// ---- 链接 ----

func TestSplitAliShare(t *testing.T) {
	cases := []struct{ in, code, link, id, pwd string }{
		{"https://www.alipan.com/s/AbCdEf12345", "", "https://www.alipan.com/s/AbCdEf12345", "AbCdEf12345", ""},
		{"「三体」https://www.aliyundrive.com/s/AbCdEf12345 提取码: x7y8 点击链接保存", "", "https://www.aliyundrive.com/s/AbCdEf12345", "AbCdEf12345", "x7y8"},
		{"alipan.com/s/AbCdEf12345/folder/6512abc", "", "https://alipan.com/s/AbCdEf12345/folder/6512abc", "AbCdEf12345", ""},
		{"https://www.alipan.com/s/AbCdEf12345?pwd=k9k9", "", "https://www.alipan.com/s/AbCdEf12345", "AbCdEf12345", "k9k9"},
		{"https://www.alipan.com/s/AbCdEf12345", " zz11 ", "https://www.alipan.com/s/AbCdEf12345", "AbCdEf12345", "zz11"},
	}
	for _, c := range cases {
		link, id, pwd := splitAliShare(c.in, c.code)
		if link != c.link || id != c.id || pwd != c.pwd {
			t.Errorf("%q → %q %q %q", c.in, link, id, pwd)
		}
	}
	if isAliShareLink("https://115.com/s/abc") || !isAliShareLink("https://www.alipan.com/s/AbCdEf12345") {
		t.Fatal("isAliShareLink 判错")
	}
}

func TestResolveAliLink(t *testing.T) {
	h := &Handler{}
	rl, err := h.resolveResourceLink(resSubmitReq{URL: "https://www.alipan.com/s/AbCdEf12345 提取码: x7y8"})
	if err != nil || rl.Action != "ali" {
		t.Fatalf("直接贴的阿里云盘分享应走秒传：%+v %v", rl, err)
	}
	// 资源站给的阿里云盘链接照旧不收
	if _, err := h.resolveResourceLink(resSubmitReq{Source: "pansou", Action: "open", URL: "https://www.alipan.com/s/AbCdEf12345"}); err == nil {
		t.Fatal("资源站的阿里云盘链接不该放行")
	}
}

// ---- 客户端（假阿里云盘） ----

func TestAliClientRefreshAndRetry(t *testing.T) {
	var mu sync.Mutex
	gen := 0
	expireOnce := true
	sessions := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/v2/account/token":
			var req map[string]string
			_ = json.Unmarshal(body, &req)
			if req["refresh_token"] != fmt.Sprintf("rt%d", gen) {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"code":"InvalidParameter.RefreshToken","message":"refresh token 不对"}`))
				return
			}
			gen++
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("at%d", gen), "refresh_token": fmt.Sprintf("rt%d", gen), "user_id": "u1", "nick_name": "小明", "default_drive_id": "d-def"})
		case "/v2/user/get":
			_, _ = w.Write([]byte(`{"user_id":"u1","nick_name":"小明","default_drive_id":"d-def","resource_drive_id":"d-res"}`))
		case "/users/v1/users/device/create_session":
			sessions++
			if r.Header.Get("X-Device-Id") == "" || len(r.Header.Get("X-Signature")) != 130 {
				w.WriteHeader(400)
				return
			}
			_, _ = w.Write([]byte(`{"success":true}`))
		case "/adrive/v3/file/list":
			if expireOnce {
				expireOnce = false
				w.WriteHeader(401)
				_, _ = w.Write([]byte(`{"code":"AccessTokenInvalid","message":"过期"}`))
				return
			}
			if r.Header.Get("Authorization") != fmt.Sprintf("Bearer at%d", gen) {
				w.WriteHeader(401)
				return
			}
			_, _ = w.Write([]byte(`{"items":[{"file_id":"f1","name":"a.mkv","type":"file","size":10,"content_hash":"ABC"}],"next_marker":""}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	oldAPI, oldAuth := aliAPIBase, aliAuthBase
	aliAPIBase, aliAuthBase = srv.URL, srv.URL
	defer func() { aliAPIBase, aliAuthBase = oldAPI, oldAuth }()

	saved := []string{}
	c, err := newAliClient("rt0", func(c *aliClient) { saved = append(saved, c.refresh) })
	if err != nil {
		t.Fatal(err)
	}
	if c.driveID != "d-res" {
		t.Fatalf("应存进资源盘，实际 %s", c.driveID)
	}
	if sessions != 1 {
		t.Fatalf("应建一次设备会话，实际 %d", sessions)
	}
	list, err := c.listDir("root")
	if err != nil || len(list) != 1 || list[0].ContentHash != "ABC" {
		t.Fatalf("token 过期后应刷新重试：%v %v", list, err)
	}
	// 每刷一次 refresh token 都要写回
	if strings.Join(saved, ",") != "rt1,rt2" {
		t.Fatalf("refresh token 轮换没写回: %v", saved)
	}
	// 拿旧 token 再建会话必然失败
	if _, err := newAliClient("rt0", nil); err == nil {
		t.Fatal("旧 refresh token 不该还能用")
	}
}

func TestAliRangeSHA1(t *testing.T) {
	data := []byte("0123456789abcdefghij")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var a, b int
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &a, &b); err != nil {
			w.WriteHeader(200)
			_, _ = w.Write(data)
			return
		}
		w.WriteHeader(206)
		_, _ = w.Write(data[a : b+1])
	}))
	defer srv.Close()
	got, err := aliRangeSHA1(srv.URL, "2-5")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha1Upper([]byte("2345"))
	if got != sum {
		t.Fatalf("区间 SHA1 不对: %s / %s", got, sum)
	}
}

// ---- 一次秒传（假阿里云盘 + 假 115） ----

type fakeAli struct {
	share   map[string][]aliFile // parent → 子项
	drive   map[string][]aliFile // 自己网盘 parent → 子项
	nextID  int
	async   map[string]bool // 这些分享文件复制时不回副本 id
	deleted []string
	noHash  map[string]bool
}

func (f *fakeAli) id() string { f.nextID++; return fmt.Sprintf("n%d", f.nextID) }

func (f *fakeAli) listShare(_, _, parent string) ([]aliFile, error) { return f.share[parent], nil }

func (f *fakeAli) findShare(id string) aliFile {
	for _, list := range f.share {
		for _, it := range list {
			if it.FileID == id {
				return it
			}
		}
	}
	return aliFile{}
}

func (f *fakeAli) saveShareFiles(_, _ string, ids []string, to string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		src := f.findShare(id)
		nid := f.id()
		cp := aliFile{FileID: nid, Name: src.Name, Size: src.Size, Type: "file", ContentHash: "H-" + src.Name}
		if f.noHash[src.Name] {
			cp.ContentHash = ""
		}
		f.drive[to] = append(f.drive[to], cp)
		if !f.async[id] {
			out[id] = nid
		}
	}
	return out, nil
}

func (f *fakeAli) ensureDir(parent, name string) (string, error) {
	for _, it := range f.drive[parent] {
		if it.Name == name && it.isDir() {
			return it.FileID, nil
		}
	}
	nid := f.id()
	f.drive[parent] = append(f.drive[parent], aliFile{FileID: nid, Name: name, Type: "folder"})
	return nid, nil
}

func (f *fakeAli) listDir(parent string) ([]aliFile, error) { return f.drive[parent], nil }

func (f *fakeAli) downloadURL(id string) (aliDownload, error) {
	return aliDownload{URL: "http://x/" + id}, nil
}

func (f *fakeAli) deleteFile(id string) error { f.deleted = append(f.deleted, id); return nil }

type fake115 struct {
	dirs    map[string]string // cid → 「父cid/名字」
	files   []string          // 「目录cid/名字」
	have    map[string]bool
	miss    map[string]bool
	flaky   map[string]int // 前几次报错
	removed []string
	n       int
}

func (f *fake115) side() side115 {
	return side115{
		mkdir: func(parent, name string) (string, error) {
			f.n++
			cid := fmt.Sprintf("c%d", f.n)
			f.dirs[cid] = f.dirs[parent] + "/" + name
			return cid, nil
		},
		rmdir: func(cid string) error { f.removed = append(f.removed, cid); return nil },
		rapid: func(dir, name string, size int64, sha1 string, digest func(string) (string, error)) (bool, error) {
			if f.flaky[name] > 0 {
				f.flaky[name]--
				return false, fmt.Errorf("115 忙")
			}
			if f.miss[name] {
				return false, nil
			}
			f.files = append(f.files, f.dirs[dir]+"/"+name)
			return true, nil
		},
		have: func(sha1 string) bool { return f.have[sha1] },
	}
}

func newFakeRun(ali *fakeAli, x *fake115) (*ali2115Run, *[]string) {
	recorded := []string{}
	return &ali2115Run{
		jobID: 7, p: aliJobParams{ShareID: "S1"}, ali: ali, x: x.side(), staging: "stage", mediaOnly: true,
		record:     func(title, wrapper string) uint { recorded = append(recorded, wrapper); return 1 },
		stop:       func() bool { return false },
		set:        func(string, int, int, string) {},
		shareToken: func(string, string) (string, error) { return "tok", nil },
		shareName:  func(string) string { return "分享名" },
	}, &recorded
}

func TestAli2115RunFolderShare(t *testing.T) {
	old := ali2115Retry
	ali2115Retry = 0
	defer func() { ali2115Retry = old }()
	ali := &fakeAli{
		share: map[string][]aliFile{
			"root": {{FileID: "d0", Name: "三体 (2023)", Type: "folder"}},
			"d0":   {{FileID: "d1", Name: "Season 1", Type: "folder"}, {FileID: "p", Name: "poster.jpg", Type: "file"}},
			"d1": {
				{FileID: "e1", Name: "E01.mkv", Type: "file", Size: 100},
				{FileID: "e2", Name: "E02.mkv", Type: "file", Size: 100},
				{FileID: "e3", Name: "E03.mkv", Type: "file", Size: 100},
				{FileID: "e4", Name: "E04.mkv", Type: "file", Size: 100},
				{FileID: "s1", Name: "E01.chs.ass", Type: "file", Size: 3},
			},
		},
		drive:  map[string][]aliFile{"root": {{FileID: "old", Name: aliTempRoot, Type: "folder"}}, "old": {{FileID: "stale", Name: "job3", Type: "folder"}}},
		async:  map[string]bool{"e4": true},
		noHash: map[string]bool{},
	}
	x := &fake115{dirs: map[string]string{"stage": "转存"}, have: map[string]bool{"H-E02.mkv": true}, miss: map[string]bool{"E03.mkv": true}, flaky: map[string]int{"E01.chs.ass": 1}}
	r, recorded := newFakeRun(ali, x)
	res, canceled, err := r.run()
	if err != nil || canceled {
		t.Fatal(err, canceled)
	}
	if res.Title != "三体 (2023)" || res.Wrapper != "三体 (2023) ·秒传7" {
		t.Fatalf("包装目录名不对: %+v", res)
	}
	if res.OK != 3 || res.Have != 1 || res.Miss != 1 || res.Failed != 0 || res.Skipped != 1 || res.Total != 6 {
		t.Fatalf("计数不对: %+v", res)
	}
	want := []string{"转存/三体 (2023) ·秒传7/Season 1/E01.mkv", "转存/三体 (2023) ·秒传7/Season 1/E04.mkv", "转存/三体 (2023) ·秒传7/Season 1/E01.chs.ass"}
	if strings.Join(sortStrs(x.files), "|") != strings.Join(sortStrs(want), "|") {
		t.Fatalf("115 上的文件不对:\n%v", x.files)
	}
	if len(res.Missed) != 1 || res.Missed[0] != "Season 1/E03.mkv" {
		t.Fatalf("未命中清单: %v", res.Missed)
	}
	// 上次残留的 job3 与这次的临时目录都删了；包装目录留着
	if !containsStr(ali.deleted, "stale") || len(ali.deleted) != 2 {
		t.Fatalf("临时目录没收拾: %v", ali.deleted)
	}
	if len(x.removed) != 0 {
		t.Fatal("有文件进来了，包装目录不该删")
	}
	if len(*recorded) != 1 || (*recorded)[0] != res.Wrapper {
		t.Fatalf("来源链接应按包装目录名登记: %v", *recorded)
	}
	if subWrapperHeld("c1") {
		t.Fatal("任务结束包装目录应放行")
	}
}

func TestAli2115RunNothingLanded(t *testing.T) {
	ali := &fakeAli{
		share:  map[string][]aliFile{"root": {{FileID: "m", Name: "电影.mkv", Type: "file", Size: 9}, {FileID: "n", Name: "说明.txt", Type: "file", Size: 1}}},
		drive:  map[string][]aliFile{},
		noHash: map[string]bool{},
	}
	x := &fake115{dirs: map[string]string{"stage": "转存"}, miss: map[string]bool{"电影.mkv": true}}
	r, _ := newFakeRun(ali, x)
	res, _, err := r.run()
	if err != nil {
		t.Fatal(err)
	}
	// 分享根不止一项：包装目录用分享名
	if res.Title != "分享名" || res.Miss != 1 || res.Skipped != 1 {
		t.Fatalf("%+v", res)
	}
	if len(x.removed) != 1 || res.Wrapper != "" {
		t.Fatalf("一个都没进来应收掉空包装目录: %v %+v", x.removed, res)
	}
}

func TestAli2115RunNoMedia(t *testing.T) {
	ali := &fakeAli{share: map[string][]aliFile{"root": {{FileID: "n", Name: "说明.txt", Type: "file"}}}, drive: map[string][]aliFile{}}
	x := &fake115{dirs: map[string]string{}}
	r, _ := newFakeRun(ali, x)
	if _, _, err := r.run(); err == nil || !strings.Contains(err.Error(), "没有视频") {
		t.Fatalf("没有视频应报错: %v", err)
	}
	if x.n != 0 {
		t.Fatal("没东西传时不该在 115 上建目录")
	}
}

func TestAli2115RunNoHash(t *testing.T) {
	ali := &fakeAli{
		share:  map[string][]aliFile{"root": {{FileID: "m", Name: "a.mkv", Type: "file"}, {FileID: "k", Name: "b.mkv", Type: "file"}}},
		drive:  map[string][]aliFile{},
		noHash: map[string]bool{"a.mkv": true},
	}
	old := ali2115SaveWait
	ali2115SaveWait = 0
	defer func() { ali2115SaveWait = old }()
	x := &fake115{dirs: map[string]string{"stage": "转存"}}
	r, _ := newFakeRun(ali, x)
	res, _, err := r.run()
	if err != nil || res.OK != 1 || res.Failed != 1 {
		t.Fatalf("没 SHA1 的记出错、别的照传: %+v %v", res, err)
	}
}

func sha1Upper(b []byte) string {
	h := sha1.New()
	h.Write(b)
	return strings.ToUpper(hex.EncodeToString(h.Sum(nil)))
}

func sortStrs(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
