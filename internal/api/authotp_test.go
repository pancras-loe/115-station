package api

import (
	"encoding/base32"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"115-station/internal/config"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// RFC 6238 附录 B 的 SHA1 测试向量（取 8 位结果的末 6 位）
func TestTotpCodeRFC6238(t *testing.T) {
	key := []byte("12345678901234567890")
	cases := map[int64]string{
		59:          "287082",
		1111111109:  "081804",
		1111111111:  "050471",
		1234567890:  "005924",
		2000000000:  "279037",
		20000000000: "353130",
	}
	for ts, want := range cases {
		if got := totpCode(key, ts/totpPeriod); got != want {
			t.Errorf("T=%d: got %s want %s", ts, got, want)
		}
	}
}

func TestTotpMatchWindow(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	now := time.Unix(1111111111, 0)
	cur := now.Unix() / totpPeriod
	key, _ := decodeOtpSecret(secret)
	for _, d := range []int64{-1, 0, 1} {
		if _, ok := totpMatch(secret, totpCode(key, cur+d), now); !ok {
			t.Errorf("偏移 %d 步应通过", d)
		}
	}
	for _, d := range []int64{-2, 2} {
		if _, ok := totpMatch(secret, totpCode(key, cur+d), now); ok {
			t.Errorf("偏移 %d 步不应通过", d)
		}
	}
	// 用户照着验证器手抄的密钥常带空格、小写
	spaced := strings.ToLower(secret[:4] + " " + secret[4:])
	if _, ok := totpMatch(spaced, totpCode(key, cur), now); !ok {
		t.Error("带空格小写的密钥应能解析")
	}
}

func otpTestHandler(t *testing.T) (*Handler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	cfg := &config.Config{ConfigDir: dir, DataDir: dir, JWTSecret: "test-secret", TokenExpire: 8 * 24 * time.Hour}
	if err := cfg.SaveAuth("admin", "pass"); err != nil {
		t.Fatal(err)
	}
	h := &Handler{Config: cfg}
	r := gin.New()
	r.POST("/login", h.Login)
	otpMu.Lock()
	otpLastStep, otpPending = 0, ""
	otpMu.Unlock()
	loginGuardMu.Lock()
	loginGuard = map[string]loginGuardEntry{}
	loginGuardMu.Unlock()
	return h, r
}

func postLogin(r *gin.Engine, body string) (int, map[string]any) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestLoginWithOtp(t *testing.T) {
	h, r := otpTestHandler(t)

	// 没开二步验证：账号密码直接拿令牌，有效期 8 天
	code, out := postLogin(r, `{"username":"admin","password":"pass"}`)
	if code != 200 || out["token"] == nil {
		t.Fatalf("未开启时应直接登录: %d %v", code, out)
	}
	claims := jwt.MapClaims{}
	if _, err := jwt.ParseWithClaims(out["token"].(string), claims, func(*jwt.Token) (any, error) { return []byte("test-secret"), nil }); err != nil {
		t.Fatal(err)
	}
	if d := int64(claims["exp"].(float64)) - time.Now().Unix(); d < 8*86400-60 || d > 8*86400+60 {
		t.Errorf("令牌有效期应为 8 天，实际 %d 秒", d)
	}

	secret, _ := newOtpSecret()
	if err := h.Config.SetAuthOtp(secret); err != nil {
		t.Fatal(err)
	}

	// 密码错：照旧报密码错，不透露开没开二步验证
	if code, out = postLogin(r, `{"username":"admin","password":"bad"}`); code != 401 || out["otp_required"] != nil {
		t.Fatalf("密码错应 401 且不带 otp_required: %d %v", code, out)
	}
	// 密码对、没带验证码：要验证码，不给令牌
	code, out = postLogin(r, `{"username":"admin","password":"pass"}`)
	if code != 200 || out["otp_required"] != true || out["token"] != nil {
		t.Fatalf("应要求验证码: %d %v", code, out)
	}
	// 验证码错
	if code, _ = postLogin(r, `{"username":"admin","password":"pass","otp":"000000x"}`); code != 401 {
		t.Fatalf("验证码错应 401: %d", code)
	}
	key, _ := decodeOtpSecret(secret)
	otp := totpCode(key, time.Now().Unix()/totpPeriod)
	code, out = postLogin(r, `{"username":"admin","password":"pass","otp":"`+otp+`"}`)
	if code != 200 || out["token"] == nil {
		t.Fatalf("验证码对应登录成功: %d %v", code, out)
	}
	// 同一个验证码不能再用
	if code, _ = postLogin(r, `{"username":"admin","password":"pass","otp":"`+otp+`"}`); code != 401 {
		t.Fatalf("重放的验证码应被拒: %d", code)
	}
}

// 环境变量同步密码（SaveAuth）不能把二步验证顺手关掉
func TestSaveAuthKeepsOtp(t *testing.T) {
	cfg := &config.Config{ConfigDir: t.TempDir()}
	if err := cfg.SaveAuth("admin", "a"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetAuthOtp("ABCDEFGH"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SaveAuth("admin", "b"); err != nil {
		t.Fatal(err)
	}
	auth, _ := cfg.LoadAuth()
	if auth.OtpSecret != "ABCDEFGH" || !cfg.VerifyAuth("admin", "b") {
		t.Fatalf("改密码后应保留二步验证: %+v", auth)
	}
}
