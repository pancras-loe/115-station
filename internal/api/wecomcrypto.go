package api

// ==================== 企业微信回调加解密（官方 WXBizMsgCrypt 协议，纯标准库） ====================
//
// 协议：AES-256-CBC（key = Base64Decode(EncodingAESKey + "=")），明文结构：
//   16字节随机 + 4字节网络序消息长度 + 明文XML + 收到的 corpid
// 签名：sha1(sort(token, timestamp, nonce, 加密串))

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// wecomAESKey 企业微信会话密钥（43 位 EncodingAESKey 解码为 32 字节 AES key）
type wecomAESKey []byte

func newWecomAESKey(encodingKey string) (wecomAESKey, error) {
	if len(encodingKey) != 43 {
		return nil, fmt.Errorf("EncodingAESKey 长度应为 43，实际 %d", len(encodingKey))
	}
	key, err := base64.StdEncoding.DecodeString(encodingKey + "=")
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("AES key 解码后应为 32 字节，实际 %d", len(key))
	}
	return key, nil
}

// wecomSign 回调签名：sha1(字典序排序后的 token/timestamp/nonce/encrypt 拼接)
func wecomSign(token, timestamp, nonce, encrypt string) string {
	parts := []string{token, timestamp, nonce, encrypt}
	sort.Strings(parts)
	h := sha1.Sum([]byte(strings.Join(parts, "")))
	return hex.EncodeToString(h[:])
}

// wecomDecrypt 解密回调密文，返回明文 XML（去掉 PKCS7 与尾部 corpid 校验由调用方按需）
func (k wecomAESKey) wecomDecrypt(b64Ciphertext string) ([]byte, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(b64Ciphertext)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < 32 || len(ciphertext)%16 != 0 {
		return nil, errors.New("密文长度非法")
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	// 协议固定 AES-CBC，IV = key 前 16 字节
	plain := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, k[:16]).CryptBlocks(plain, ciphertext)
	// 去 PKCS7
	pad := int(plain[len(plain)-1])
	if pad < 1 || pad > 32 || pad > len(plain) {
		return nil, errors.New("PKCS7 填充非法")
	}
	plain = plain[:len(plain)-pad]
	// 明文结构：16随机 + 4长度 + XML + corpid
	if len(plain) < 20 {
		return nil, errors.New("明文过短")
	}
	msgLen := int(binary.BigEndian.Uint32(plain[16:20]))
	if 20+msgLen > len(plain) {
		return nil, errors.New("消息长度字段越界")
	}
	return plain[20 : 20+msgLen], nil
}

var wecomCorpIDCache string
