package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

func stubRename115Post(t *testing.T, fn func(url.Values) ([]byte, error)) {
	t.Helper()
	old := rename115Post
	rename115Post = func(_ string, form url.Values, _ string, _ time.Duration) ([]byte, error) {
		return fn(form)
	}
	t.Cleanup(func() { rename115Post = old })
}

func rename115Success(form url.Values) []byte {
	data := map[string]string{}
	for key, values := range form {
		fid := strings.TrimSuffix(strings.TrimPrefix(key, "files_new_name["), "]")
		if len(values) > 0 {
			data[fid] = values[0]
		}
	}
	body, _ := json.Marshal(map[string]any{"state": true, "data": data})
	return body
}

func rename115Names(n int) map[string]string {
	names := make(map[string]string, n)
	for i := 0; i < n; i++ {
		fid := fmt.Sprintf("%020d", i+1)
		names[fid] = fmt.Sprintf("剧集.S01E%03d.mkv", i+1)
	}
	return names
}

// p115client 允许整批提交，服务端接受时不该擅自按未经证实的固定大小切包。
func TestRename115BatchKeepsAcceptedBatchWhole(t *testing.T) {
	names := rename115Names(223)
	var sizes []int
	stubRename115Post(t, func(form url.Values) ([]byte, error) {
		sizes = append(sizes, len(form))
		return rename115Success(form), nil
	})
	renamed, err := rename115Batch("cookie", names)
	if err != nil || len(renamed) != len(names) {
		t.Fatalf("整批成功结果不对: renamed=%d err=%v", len(renamed), err)
	}
	if len(sizes) != 1 || sizes[0] != 223 {
		t.Fatalf("服务端接受整批时不应主动切分: %v", sizes)
	}
}

// 只有服务端明确报参数错误才二分；这样不依赖猜测上限，也能适配真实隐藏阈值。
func TestRename115BatchSplitsOnlyAfterParamError(t *testing.T) {
	names := rename115Names(223)
	var sizes []int
	stubRename115Post(t, func(form url.Values) ([]byte, error) {
		sizes = append(sizes, len(form))
		if len(form) > 100 {
			return []byte(`{"state":false,"error":"参数错误。"}`), nil
		}
		return rename115Success(form), nil
	})
	renamed, err := rename115Batch("cookie", names)
	if err != nil || len(renamed) != len(names) {
		t.Fatalf("拆分后应全部成功: renamed=%d err=%v", len(renamed), err)
	}
	if len(sizes) < 3 || sizes[0] != 223 {
		t.Fatalf("应先尝试完整批次、被拒后再拆分: %v", sizes)
	}
}

// 单个坏名字不能拖累另外几百项；返回值必须精确告诉落盘层哪些 fid 已改名。
func TestRename115BatchPreservesPartialSuccess(t *testing.T) {
	names := map[string]string{
		"001": "正常1.mkv",
		"002": "正常2.mkv",
		"003": "坏参数.mkv",
		"004": "正常4.mkv",
	}
	stubRename115Post(t, func(form url.Values) ([]byte, error) {
		for key := range form {
			if strings.Contains(key, "003") {
				return []byte(`{"state":false,"error":"参数错误。"}`), nil
			}
		}
		return rename115Success(form), nil
	})
	renamed, err := rename115Batch("cookie", names)
	if err == nil || len(renamed) != 3 {
		t.Fatalf("应只留下一个失败项: renamed=%v err=%v", renamed, err)
	}
	if _, ok := renamed["003"]; ok {
		t.Fatal("被拒的文件被误报成改名成功")
	}
}

// 网络/鉴权类错误不能递归拆包，否则会在同一坏状态下成倍请求 115。
func TestRename115BatchDoesNotSplitTransportError(t *testing.T) {
	calls := 0
	stubRename115Post(t, func(url.Values) ([]byte, error) {
		calls++
		return nil, errors.New("network down")
	})
	_, err := rename115Batch("cookie", rename115Names(223))
	if err == nil || calls != 1 {
		t.Fatalf("传输错误不应拆分重试: calls=%d err=%v", calls, err)
	}
}

// 如果“参数错误”其实是接口整体异常，每个子批次都会失败；必须有总请求上限，
// 不能为了定位参数把一次失败放大成数百次 115 写请求。
func TestRename115BatchCapsAdaptiveRequests(t *testing.T) {
	calls := 0
	stubRename115Post(t, func(url.Values) ([]byte, error) {
		calls++
		return []byte(`{"state":false,"error":"参数错误。"}`), nil
	})
	_, err := rename115Batch("cookie", rename115Names(223))
	if err == nil || calls != rename115MaxAdaptiveRequests {
		t.Fatalf("参数错误拆分应在请求上限停止: calls=%d err=%v", calls, err)
	}
}
