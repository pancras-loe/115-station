package api

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"strings"
	"testing"
)

// TestWecomSign 签名算法：sha1(sort(token,timestamp,nonce,encrypt))，
// 期望值由独立 Python 实现交叉计算得出（fedb827c...）
func TestWecomSign(t *testing.T) {
	got := wecomSign("qwe123", "1409659813", "1372623149", "RypEvHKD8QQKFhvQ6QgeBKiQKRWsD6i6w4QwZBRa3ZFk2vV3M")
	want := "fedb827cc177510218c9ef2a33f372b73693db4a"
	if got != want {
		t.Errorf("wecomSign = %s, want %s", got, want)
	}
}

// TestWecomRoundTrip 构造企业微信格式的密文，验证解密正确取出明文 XML
func TestWecomRoundTrip(t *testing.T) {
	// 43 位标准 EncodingAESKey
	// bytes(0..31) 的 Base64 去掉填充位——标准合法的 43 位 EncodingAESKey
	encKey := "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	key, err := newWecomAESKey(encKey)
	if err != nil {
		t.Fatalf("newWecomAESKey: %v", err)
	}

	plainXML, _ := xml.Marshal(map[string]string{})
	_ = plainXML
	msg := `<xml><ToUserName><![CDATA[ww34592c8b210b528f]]></ToUserName><FromUserName><![CDATA[daisy]]></FromUserName><MsgType><![CDATA[text]]></MsgType><Content><![CDATA[帮助]]></Content></xml>`

	// 手工加密成企业微信协议格式
	aesKey := []byte(key)
	plain := make([]byte, 0, 16+4+len(msg)+32)
	plain = append(plain, bytes.Repeat([]byte{0xAB}, 16)...)
	lenB := make([]byte, 4)
	binary.BigEndian.PutUint32(lenB, uint32(len(msg)))
	plain = append(plain, lenB...)
	plain = append(plain, []byte(msg)...)
	plain = append(plain, []byte("ww34592c8b210b528f")...)
	pad := 32 - len(plain)%32
	plain = append(plain, bytes.Repeat([]byte{byte(pad)}, pad)...)

	block, _ := aes.NewCipher(aesKey)
	ct := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, aesKey[:16]).CryptBlocks(ct, plain)
	b64 := base64.StdEncoding.EncodeToString(ct)

	got, err := key.wecomDecrypt(b64)
	if err != nil {
		t.Fatalf("wecomDecrypt: %v", err)
	}
	if string(got) != msg {
		t.Errorf("round trip 明文不符:\n got: %s\nwant: %s", got, msg)
	}

	// 解析出 Content
	var m struct {
		Content string `xml:"Content"`
	}
	if err := xml.Unmarshal(got, &m); err != nil {
		t.Fatalf("xml: %v", err)
	}
	if m.Content != "帮助" {
		t.Errorf("Content = %q, want 帮助", m.Content)
	}
}

// TestWecomKeyValidation 非法 key 长度拒绝
func TestWecomKeyValidation(t *testing.T) {
	if _, err := newWecomAESKey("short"); err == nil {
		t.Error("短 key 应被拒绝")
	}
	if _, err := newWecomAESKey(strings.Repeat("x", 50)); err == nil {
		t.Error("长 key 应被拒绝")
	}
}
