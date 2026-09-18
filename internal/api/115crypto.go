package api

// ==================== 115 App 接口加密（proapi android download 专用） ====================
//
// 移植自 openStrm frontend/src/lib/115crypto.ts（社区通用实现）：
// 请求载荷 = base64( RSA( 0x00*16 || xor_k2(reverse(xor_k1(data))) ) )
// 响应载荷 = 前 16 字节随机种子经 genKey 派生 12 字节密钥，xor 后 reverse 再 xor_k1

import (
	"bytes"
	"encoding/base64"
	"math/big"
)

var m115k1 = []byte{0x8d, 0xa5, 0xa5, 0x8d}
var m115k2 = []byte{0x78, 0x06, 0xad, 0x4c, 0x33, 0x86, 0x5d, 0x18, 0x4c, 0x01, 0x3f, 0x46}

var m115G_kts = []byte{
	0xf0, 0xe5, 0x69, 0xae, 0xbf, 0xdc, 0xbf, 0x8a,
	0x1a, 0x45, 0xe8, 0xbe, 0x7d, 0xa6, 0x73, 0xb8,
	0xde, 0x8f, 0xe7, 0xc4, 0x45, 0xda, 0x86, 0xc4,
	0x9b, 0x64, 0x8b, 0x14, 0x6a, 0xb4, 0xf1, 0xaa,
	0x38, 0x01, 0x35, 0x9e, 0x26, 0x69, 0x2c, 0x86,
	0x00, 0x6b, 0x4f, 0xa5, 0x36, 0x34, 0x62, 0xa6,
	0x2a, 0x96, 0x68, 0x18, 0xf2, 0x4a, 0xfd, 0xbd,
	0x6b, 0x97, 0x8f, 0x4d, 0x8f, 0x89, 0x13, 0xb7,
	0x6c, 0x8e, 0x93, 0xed, 0x0e, 0x0d, 0x48, 0x3e,
	0xd7, 0x2f, 0x88, 0xd8, 0xfe, 0xfe, 0x7e, 0x86,
	0x50, 0x95, 0x4f, 0xd1, 0xeb, 0x83, 0x26, 0x34,
	0xdb, 0x66, 0x7b, 0x9c, 0x7e, 0x9d, 0x7a, 0x81,
	0x32, 0xea, 0xb6, 0x33, 0xde, 0x3a, 0xa9, 0x59,
	0x34, 0x66, 0x3b, 0xaa, 0xba, 0x81, 0x60, 0x48,
	0xb9, 0xd5, 0x81, 0x9c, 0xf8, 0x6c, 0x84, 0x77,
	0xff, 0x54, 0x78, 0x26, 0x5f, 0xbe, 0xe8, 0x1e,
	0x36, 0x9f, 0x34, 0x80, 0x5c, 0x45, 0x2c, 0x9b,
	0x76, 0xd5, 0x1b, 0x8f, 0xcc, 0xc3, 0xb8, 0xf5,
}

var (
	m115Modulus = mustBigInt("8686980c0f5a24c4b9d43020cd2c22703ff3f450756529058b1cf88f09b8602136477198a6e2683149659bd122c33592fdb5ad47944ad1ea4d36c6b172aad6338c3bb6ac6227502d010993ac967d1aef00f0c8e038de2e4d3bc2ec368af2e9f10a6f1eda4f7262f136420c07c331b871bf139f74f3010e3c4fe57df3afb71683")
	m115Exp     = big.NewInt(0x10001)
)

func mustBigInt(hexStr string) *big.Int {
	n, ok := new(big.Int).SetString(hexStr, 16)
	if !ok {
		panic("invalid 115 modulus")
	}
	return n
}

// xor115 按 openStrm 的对齐方式异或：先处理 len(src)&3 个字节，再按密钥长度分块
func xor115(src, key []byte) []byte {
	out := make([]byte, len(src))
	i := len(src) & 3
	for j := 0; j < i && j < len(key); j++ {
		out[j] = src[j] ^ key[j]
	}
	for j := i; j < len(src); j += len(key) {
		end := j + len(key)
		if end > len(src) {
			end = len(src)
		}
		for x := j; x < end; x++ {
			out[x] = src[x] ^ key[x-j]
		}
	}
	return out
}

func reverseBytes(b []byte) {
	for l, r := 0, len(b)-1; l < r; l, r = l+1, r-1 {
		b[l], b[r] = b[r], b[l]
	}
}

// encrypt115 加密请求载荷，返回 base64
func encrypt115(data []byte) string {
	p := xor115(data, m115k1)
	reverseBytes(p)
	p = xor115(p, m115k2)
	xorText := make([]byte, 16+len(p))
	copy(xorText[16:], p)

	var out []byte
	for l := 0; l < len(xorText); l += 117 {
		r := l + 117
		if r > len(xorText) {
			r = len(xorText)
		}
		chunk := xorText[l:r]
		block := make([]byte, 128)
		for i := 1; i < 127-len(chunk); i++ {
			block[i] = 0x02
		}
		copy(block[128-len(chunk):], chunk)
		c := new(big.Int).Exp(new(big.Int).SetBytes(block), m115Exp, m115Modulus)
		cb := c.Bytes()
		b128 := make([]byte, 128)
		copy(b128[128-len(cb):], cb)
		out = append(out, b128...)
	}
	return base64.StdEncoding.EncodeToString(out)
}

// decrypt115 解密响应载荷
func decrypt115(cipherB64 string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(cipherB64)
	if err != nil {
		return nil, err
	}
	var data []byte
	for l := 0; l < len(raw); l += 128 {
		r := l + 128
		if r > len(raw) {
			r = len(raw)
		}
		p := new(big.Int).Exp(new(big.Int).SetBytes(raw[l:r]), m115Exp, m115Modulus)
		b := p.Bytes()
		idx := bytes.IndexByte(b, 0)
		if idx >= 0 {
			b = b[idx+1:]
		}
		data = append(data, b...)
	}
	if len(data) < 16 {
		return nil, errCryptoShort
	}
	keyL := genKey115(data[:16], 12)
	payload := xor115(data[16:], keyL)
	reverseBytes(payload)
	return xor115(payload, m115k1), nil
}

type cryptoErr string

func (e cryptoErr) Error() string { return string(e) }

const errCryptoShort = cryptoErr("响应密文过短")

// genKey115 由 16 字节随机种子派生 12 字节密钥
func genKey115(randKey []byte, skLen int) []byte {
	xorKey := make([]byte, skLen)
	length := skLen * (skLen - 1)
	index := 0
	for i := 0; i < skLen; i++ {
		x := (int(randKey[i]) + int(m115G_kts[index])) & 0xff
		xorKey[i] = m115G_kts[length] ^ byte(x)
		length -= skLen
		index += skLen
	}
	return xorKey
}
