package api

import (
	"encoding/hex"
	"testing"
)

// 与 openStrm frontend/src/lib/115crypto.ts（Node 运行）交叉验证的向量
// ENC_B64：同一 pick_code 请求载荷，Go 加密必须与 JS 逐字节一致（加密确定性，无随机成分）
// FAKE_B64 / DECRaw_HEX：无真实私钥时构造的伪密文，Go 解密须与 JS 解密同输入同输出
//（输出本身无业务含义，仅验证 xor/reverse/genKey/RSA/strip 流程逐位一致）
const (
	testENCB64    = "B4mkwjI12a1NBPsVwUn6Hik7RTWISZHDvSNeaE4+Vn6p/mTciLzMixrKBx3eeiz65V3vtQhSMjPPb8zIQq7aVu5PJP5lokN79yhJbwaH4BSqVuVTo2whOLGJrp1mRizf+lqiCVu8WQPefur7CDUsxUTH78EF81WXUCBaAR3LUQ4="
	testFAKEB64   = "KM+KLf2Cwe8HnDiESYeWQWrdiIIwXYMIFERMp6IEt6wpyW41y4fOAACcTPMlq/eU8ocJeQBOEtE9JqZw5eCD0+O3221u0a2c/w7/QHSqPS+D0+XwdUhYRtyPT1OTPQ6iPlY1MLY12pjwJ2pWNyBuW2nPFA034EamAnc6VRgwhyE="
	testDECRawHex = "d69a5f6fb5a12c8c9854408941cb321ba55d286f039d568b428db85f9071e4d0bba10a2f3985e6ebbc9b39e2b76fe589ca2af09fca3f07cbc774e663eb2711c321c78c45541b26e2887d9f7e0172e818abbc2c4876eae12686117f04f5d0a5bb15502fbd2d457fa1e51974d3546e6d2b"
)

func TestEncrypt115MatchesJS(t *testing.T) {
	got := encrypt115([]byte(`{"pick_code":"csixkd69sf547dzqx"}`))
	if got != testENCB64 {
		t.Fatalf("encrypt115 与 openStrm JS 输出不一致:\n got  = %s\n want = %s", got, testENCB64)
	}
}

func TestDecrypt115MatchesJS(t *testing.T) {
	raw, err := decrypt115(testFAKEB64)
	if err != nil {
		t.Fatalf("decrypt115 出错: %v", err)
	}
	if got := hex.EncodeToString(raw); got != testDECRawHex {
		t.Fatalf("decrypt115 与 openStrm JS 输出不一致:\n got  = %s\n want = %s", got, testDECRawHex)
	}
}

func TestGenKey115(t *testing.T) {
	seed := make([]byte, 16)
	for i := range seed {
		seed[i] = byte((i*7 + 13) & 0xff)
	}
	key := genKey115(seed, 12)
	want := []byte{0xa1, 0x6e, 0x0c, 0x7a, 0xb8, 0x68, 0x81, 0x03, 0x51, 0x9d, 0x2f, 0x46} // Node 运行 openStrm genKey 的输出
	for i := range want {
		if key[i] != want[i] {
			t.Fatalf("genKey115 mismatch at %d: got %02x want %02x", i, key[i], want[i])
		}
	}
}
