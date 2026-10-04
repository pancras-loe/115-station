package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/skip2/go-qrcode"
)

// ==================== 登录二步验证（TOTP） ====================
//
// 形态对齐 MoviePilot（app/api/endpoints/mfa.py + app/chain/user.py 的 _verify_mfa）：
// 先验密码，密码对了才告诉前端「需要验证码」，避免对未登录的人暴露账号开没开二步验证；
// 绑定时先生成密钥、用户用验证器扫码后回填一次验证码才算开启；关闭要再输一次密码。
// 与它不同的两处：
//   - 待绑定的密钥留在服务端（MoviePilot 是把 otpauth URI 交给前端、验证时再传回来），
//     这样开启时写进 auth.yaml 的一定是服务端自己生成的那个；
//   - 记住上一次通过的时间步，同一个验证码不能用第二次（RFC 6238 §5.2 的建议）。
// 算法是 RFC 6238 默认参数：HMAC-SHA1、30 秒、6 位，Google / Microsoft Authenticator 都认。

const (
	totpPeriod = 30
	totpDigits = 6
	totpIssuer = "115-Station"
	// 待绑定密钥的有效期：扫码 + 回填足够了，过期要重新生成
	otpPendingTTL = 10 * time.Minute
)

var (
	otpMu        sync.Mutex
	otpPending   string // 生成了、还没验证通过的密钥（只有一个管理员，存一份即可）
	otpPendingAt time.Time
	otpLastStep  int64 // 最近一次通过验证的时间步，防重放
)

// totpCode 计算某个时间步的验证码（RFC 4226 动态截断）
func totpCode(key []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, bin%1000000)
}

func decodeOtpSecret(secret string) ([]byte, error) {
	s := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	return base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.TrimRight(s, "="))
}

// totpMatch 在前后各一个时间步（±30 秒）内找匹配的验证码，返回命中的时间步。
// 容一步是给手机与服务器的时钟差留余地，MoviePilot 用的 pyotp 默认不容，
// 实际部署在 NAS 上时钟漂个十几秒很常见
func totpMatch(secret, code string, now time.Time) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return 0, false
	}
	key, err := decodeOtpSecret(secret)
	if err != nil || len(key) == 0 {
		return 0, false
	}
	cur := now.Unix() / totpPeriod
	for _, st := range []int64{cur, cur - 1, cur + 1} {
		if subtle.ConstantTimeCompare([]byte(totpCode(key, st)), []byte(code)) == 1 {
			return st, true
		}
	}
	return 0, false
}

// otpCheck 校验验证码并防重放：同一时间步（及更早）的验证码通过一次后不再接受
func otpCheck(secret, code string) bool {
	st, ok := totpMatch(secret, code, time.Now())
	if !ok {
		return false
	}
	otpMu.Lock()
	defer otpMu.Unlock()
	if st <= otpLastStep {
		return false
	}
	otpLastStep = st
	return true
}

func newOtpSecret() (string, error) {
	buf := make([]byte, 20) // 160 位，RFC 4226 推荐长度
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf), nil
}

func otpURI(username, secret string) string {
	label := url.PathEscape(totpIssuer + ":" + username)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", totpIssuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(totpDigits))
	q.Set("period", fmt.Sprint(totpPeriod))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// OtpStatus GET /auth/otp —— 当前是否开启二步验证
func (h *Handler) OtpStatus(c *gin.Context) {
	auth, err := h.Config.LoadAuth()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取账号配置失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":      auth.OtpSecret != "",
		"token_expire": int64(h.tokenExpire() / time.Minute), // 分钟，界面上说明登录多久有效
	})
}

// OtpGenerate POST /auth/otp/generate —— 生成待绑定密钥与二维码，验证通过前不生效
func (h *Handler) OtpGenerate(c *gin.Context) {
	auth, err := h.Config.LoadAuth()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取账号配置失败"})
		return
	}
	if auth.OtpSecret != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "二步验证已开启，如需更换请先关闭"})
		return
	}
	secret, err := newOtpSecret()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成密钥失败"})
		return
	}
	uri := otpURI(auth.Username, secret)
	png, err := qrcode.Encode(uri, qrcode.Medium, 256)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成二维码失败"})
		return
	}
	otpMu.Lock()
	otpPending, otpPendingAt = secret, time.Now()
	otpMu.Unlock()
	c.JSON(http.StatusOK, gin.H{
		"secret": secret,
		"uri":    uri,
		"qr":     "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
	})
}

// OtpEnable POST /auth/otp/enable {code} —— 用验证器上的验证码确认绑定
func (h *Handler) OtpEnable(c *gin.Context) {
	var req struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	otpMu.Lock()
	secret, at := otpPending, otpPendingAt
	otpMu.Unlock()
	if secret == "" || time.Since(at) > otpPendingTTL {
		c.JSON(http.StatusBadRequest, gin.H{"error": "二维码已过期，请重新生成"})
		return
	}
	if !otpCheck(secret, req.Code) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "验证码不正确，请核对手机时间后重试"})
		return
	}
	if err := h.Config.SetAuthOtp(secret); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败：" + err.Error()})
		return
	}
	otpMu.Lock()
	otpPending = ""
	otpMu.Unlock()
	log.Println("[账号] ✓ 已开启登录二步验证")
	c.JSON(http.StatusOK, gin.H{"message": "二步验证已开启"})
}

// OtpDisable POST /auth/otp/disable {password} —— 关闭要再输一次密码（同 MoviePilot），
// 只凭一个被盗的登录令牌关不掉
func (h *Handler) OtpDisable(c *gin.Context) {
	var req struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	auth, err := h.Config.LoadAuth()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取账号配置失败"})
		return
	}
	// 与登录共用防爆破计数：否则这里就成了一个不限次数的密码猜测接口
	ip := c.ClientIP()
	if remain := loginGuardCheck(ip); remain > 0 {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": fmt.Sprintf("失败次数过多，已锁定，请 %s 后再试", remain.Truncate(time.Second))})
		return
	}
	if !h.Config.VerifyAuth(auth.Username, req.Password) {
		loginGuardFail(ip)
		c.JSON(http.StatusBadRequest, gin.H{"error": "密码错误"})
		return
	}
	loginGuardPass(ip)
	if err := h.Config.SetAuthOtp(""); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败：" + err.Error()})
		return
	}
	log.Println("[账号] ○ 已关闭登录二步验证")
	c.JSON(http.StatusOK, gin.H{"message": "二步验证已关闭"})
}
