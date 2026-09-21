package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

var embyUserIDPattern = regexp.MustCompile(`(?i)(?:^|[,\s])UserId\s*=\s*"([^"]+)"`)

type embyPlaybackUserKey struct{}
type embyUserHint struct {
	id      string
	expires time.Time
}

var embyUserHints = struct {
	sync.Mutex
	values map[[32]byte]embyUserHint
}{values: make(map[[32]byte]embyUserHint)}

// 用户标识只是路由提示，绝不是授权结果：每次播放仍用同一个客户端 token
// 请求 /Users/{id} 和用户库条目。Emby 的正式用户接口不保证有 /Users/Me。
func embyPlaybackUserID(req *http.Request) string {
	if id, _ := req.Context().Value(embyPlaybackUserKey{}).(string); id != "" {
		return id
	}
	if id := queryFold(req.URL.Query(), "UserId"); id != "" {
		return id
	}
	for _, name := range []string{"Authorization", "X-Emby-Authorization"} {
		if m := embyUserIDPattern.FindStringSubmatch(req.Header.Get(name)); len(m) > 1 {
			return m[1]
		}
	}
	key := sha256.Sum256([]byte(embyClientToken(req)))
	embyUserHints.Lock()
	defer embyUserHints.Unlock()
	if hint, ok := embyUserHints.values[key]; ok && time.Now().Before(hint.expires) {
		return hint.id
	}
	return ""
}

func rememberEmbyUser(req *http.Request, id string) {
	token := embyClientToken(req)
	if token == "" || id == "" {
		return
	}
	embyUserHints.Lock()
	defer embyUserHints.Unlock()
	if len(embyUserHints.values) >= 4096 {
		clear(embyUserHints.values)
	}
	embyUserHints.values[sha256.Sum256([]byte(token))] = embyUserHint{id: id, expires: time.Now().Add(30 * time.Minute)}
}

// POST PlaybackInfo 的 UserId 常在 JSON 中。保留请求体原样供 Emby 校验，
// 只把用户标识带到随后生成的播放 URL，避免误用管理员身份查片。
func prepareEmbyPlaybackRequest(req *http.Request, routePath string) (*http.Request, error) {
	if req.Method != http.MethodPost || !playbackInfoPathRe.MatchString(routePath) || req.Body == nil {
		return req, nil
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, (1<<20)+1))
	if err != nil {
		return req, err
	}
	// 超出上限仍原样透传，但不解析用户提示，避免无限制读取客户端请求。
	if len(body) > 1<<20 {
		req.Body = &replayedEmbyBody{Reader: io.MultiReader(bytes.NewReader(body), req.Body), Closer: req.Body}
		return req, nil
	}
	req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(body))
	var payload struct {
		UserID string `json:"UserId"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.UserID != "" {
		req = req.WithContext(context.WithValue(req.Context(), embyPlaybackUserKey{}, payload.UserID))
	}
	return req, nil
}

type replayedEmbyBody struct {
	io.Reader
	io.Closer
}

func rememberEmbyResponseUser(req *http.Request) {
	p := embyResponsePath(req)
	if playbackInfoPathRe.MatchString(p) {
		rememberEmbyUser(req, embyPlaybackUserID(req))
		return
	}
	if itemDetailPathRe.MatchString(p) {
		parts := strings.Split(strings.Trim(p, "/"), "/")
		rememberEmbyUser(req, parts[1])
	}
}
