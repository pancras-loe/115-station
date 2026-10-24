package api

// 阿里云盘的设备会话签名（ali2115.go 用）。
//
// 阿里云盘网页 / App 接口要求请求头带 X-Device-Id 与 X-Signature：设备私钥（secp256k1）签
// 「appId:deviceId:userId:nonce」，公钥先经 users/v1/users/device/create_session 登记。
// 私钥直接取 sha256(user_id)，同一个账号每次都一样，不用存。做法对照 OpenList
// drivers/aliyundrive（util.go 的 sign / createSession、help.go 的公钥格式），只读，Go 重写。
//
// 没引第三方 secp256k1 库：标准库 crypto/elliptic 只支持 a=-3 的曲线，secp256k1 是 a=0。
// 签名只在建会话时算一次，用 big.Int 的仿射坐标点运算足够，不追求常数时间——
// 私钥本来就是由 user_id 公开推出来的，不是秘密。

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
)

// aliSignAppID 阿里云盘网页端签名用的 appId（OpenList 同款）
const aliSignAppID = "5dde4e1bdf9e4966b387ba58f4b3fdc3"

var (
	secpP, _  = new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEFFFFFC2F", 16)
	secpN, _  = new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141", 16)
	secpGx, _ = new(big.Int).SetString("79BE667EF9DCBBAC55A06295CE870B07029BFCDB2DCE28D959F2815B16F81798", 16)
	secpGy, _ = new(big.Int).SetString("483ADA7726A3C4655DA4FBFC0E1108A8FD17B448A68554199C47D08FFB10D4B8", 16)
)

// secpPoint 仿射坐标的点；inf 是无穷远点
type secpPoint struct {
	x, y *big.Int
	inf  bool
}

func secpG() secpPoint { return secpPoint{x: new(big.Int).Set(secpGx), y: new(big.Int).Set(secpGy)} }

func secpAdd(a, b secpPoint) secpPoint {
	if a.inf {
		return b
	}
	if b.inf {
		return a
	}
	p := secpP
	var lambda *big.Int
	if a.x.Cmp(b.x) == 0 {
		if new(big.Int).Mod(new(big.Int).Add(a.y, b.y), p).Sign() == 0 {
			return secpPoint{inf: true}
		}
		// 倍点：λ = 3x² / 2y（a = 0）
		num := new(big.Int).Mul(a.x, a.x)
		num.Mul(num, big.NewInt(3))
		den := new(big.Int).Lsh(a.y, 1)
		lambda = num.Mul(num, den.ModInverse(den.Mod(den, p), p))
	} else {
		num := new(big.Int).Sub(b.y, a.y)
		den := new(big.Int).Sub(b.x, a.x)
		den.Mod(den, p)
		lambda = num.Mul(num, den.ModInverse(den, p))
	}
	lambda.Mod(lambda, p)
	x := new(big.Int).Mul(lambda, lambda)
	x.Sub(x, a.x).Sub(x, b.x).Mod(x, p)
	y := new(big.Int).Sub(a.x, x)
	y.Mul(y, lambda).Sub(y, a.y).Mod(y, p)
	return secpPoint{x: x, y: y}
}

func secpMul(pt secpPoint, k *big.Int) secpPoint {
	acc := secpPoint{inf: true}
	for i := k.BitLen() - 1; i >= 0; i-- {
		acc = secpAdd(acc, acc)
		if k.Bit(i) == 1 {
			acc = secpAdd(acc, pt)
		}
	}
	return acc
}

func pad32(b []byte) []byte {
	if len(b) >= 32 {
		return b
	}
	return append(make([]byte, 32-len(b)), b...)
}

// aliDeviceKey 设备 id 与私钥：deviceId = hex(sha256(user_id))，私钥就是这 32 字节
func aliDeviceKey(userID string) (deviceID string, d *big.Int) {
	sum := sha256.Sum256([]byte(userID))
	deviceID = hex.EncodeToString(sum[:])
	d = new(big.Int).SetBytes(sum[:])
	d.Mod(d, secpN)
	return deviceID, d
}

// aliPubKeyHex 公钥 X||Y 各 32 字节的 hex（不带 04 前缀，create_session 的 pubKey 就要这个）
func aliPubKeyHex(d *big.Int) string {
	q := secpMul(secpG(), d)
	return hex.EncodeToString(append(pad32(q.x.Bytes()), pad32(q.y.Bytes())...))
}

// secpSign 对 32 字节摘要签名，返回 r||s||recid（65 字节），s 取低半区
func secpSign(d *big.Int, digest []byte) ([]byte, error) {
	z := new(big.Int).SetBytes(digest)
	for range 64 {
		k, err := rand.Int(rand.Reader, new(big.Int).Sub(secpN, big.NewInt(1)))
		if err != nil {
			return nil, err
		}
		k.Add(k, big.NewInt(1))
		R := secpMul(secpG(), k)
		r := new(big.Int).Mod(R.x, secpN)
		if r.Sign() == 0 {
			continue
		}
		s := new(big.Int).Mul(r, d)
		s.Add(s, z)
		s.Mul(s, new(big.Int).ModInverse(k, secpN)).Mod(s, secpN)
		if s.Sign() == 0 {
			continue
		}
		recid := byte(R.y.Bit(0))
		if R.x.Cmp(secpN) >= 0 {
			recid |= 2
		}
		if s.Cmp(new(big.Int).Rsh(secpN, 1)) > 0 {
			s.Sub(secpN, s)
			recid ^= 1
		}
		return append(append(pad32(r.Bytes()), pad32(s.Bytes())...), recid), nil
	}
	return nil, fmt.Errorf("签名失败")
}

// secpVerify 验签（只给测试用：确认点运算与签名对得上）
func secpVerify(q secpPoint, digest, sig []byte) bool {
	if len(sig) < 64 {
		return false
	}
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:64])
	if r.Sign() == 0 || s.Sign() == 0 || r.Cmp(secpN) >= 0 || s.Cmp(secpN) >= 0 {
		return false
	}
	z := new(big.Int).SetBytes(digest)
	w := new(big.Int).ModInverse(s, secpN)
	u1 := new(big.Int).Mul(z, w)
	u1.Mod(u1, secpN)
	u2 := new(big.Int).Mul(r, w)
	u2.Mod(u2, secpN)
	X := secpAdd(secpMul(secpG(), u1), secpMul(q, u2))
	if X.inf {
		return false
	}
	return new(big.Int).Mod(X.x, secpN).Cmp(r) == 0
}

// aliSignature 设备会话签名（X-Signature）：签「appId:deviceId:userId:0」
func aliSignature(userID string) (deviceID, signature string, err error) {
	deviceID, d := aliDeviceKey(userID)
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%s:%d", aliSignAppID, deviceID, userID, 0)))
	sig, err := secpSign(d, digest[:])
	if err != nil {
		return "", "", err
	}
	return deviceID, hex.EncodeToString(sig), nil
}
