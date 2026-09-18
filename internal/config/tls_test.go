package config

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestEnsureTLSCertGenerateAndReuse(t *testing.T) {
	dir := t.TempDir()
	c := &Config{ConfigDir: dir}

	cert1, key1, err := c.EnsureTLSCert()
	if err != nil {
		t.Fatalf("生成: %v", err)
	}
	for _, p := range []string{cert1, key1} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("文件缺失 %s: %v", p, err)
		}
	}
	// 内容是合法的证书+EC 私钥，SAN 覆盖环回
	pair, err := tls.LoadX509KeyPair(cert1, key1)
	if err != nil {
		t.Fatalf("加载: %v", err)
	}
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	if !tlsHasSAN(leaf, "127.0.0.1") {
		t.Error("SAN 缺 127.0.0.1")
	}
	if !tlsHasDNS(leaf, "localhost") {
		t.Error("SAN 缺 localhost")
	}
	if leaf.NotAfter.Before(time.Now().AddDate(9, 0, 0)) {
		t.Errorf("有效期过短: %v", leaf.NotAfter)
	}

	// 二次调用复用（mtime 不变）
	st1, _ := os.Stat(cert1)
	time.Sleep(1100 * time.Millisecond) // 保证 mtime 粒度可分辨
	if _, _, err := c.EnsureTLSCert(); err != nil {
		t.Fatalf("复用: %v", err)
	}
	st2, _ := os.Stat(cert1)
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Error("证书不应被重新生成")
	}

	// TLS_SAN 追加新主机 → 自动重新生成并覆盖
	t.Setenv("TLS_SAN", "media.example.com,10.9.8.7")
	cert3, _, err := c.EnsureTLSCert()
	if err != nil {
		t.Fatalf("重生: %v", err)
	}
	pair3, _ := tls.LoadX509KeyPair(cert3, filepath.Join(dir, "tls.key"))
	leaf3, _ := x509.ParseCertificate(pair3.Certificate[0])
	if !tlsHasDNS(leaf3, "media.example.com") {
		t.Error("追加 DNS 未生效")
	}
	if !tlsHasSAN(leaf3, "10.9.8.7") {
		t.Error("追加 IP 未生效")
	}
	if !tlsHasSAN(leaf3, "127.0.0.1") {
		t.Error("原有 SAN 丢失")
	}
	// 私钥权限（Windows 不映射 Unix 权限位，只在类 Unix 上校验）
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(dir, "tls.key"))
		if fi.Mode().Perm() != 0600 {
			t.Errorf("私钥权限 %v want 0600", fi.Mode().Perm())
		}
	}
}
