package config

// 自签名 HTTPS（管理后台可选启用）：
//
//	TLS_ENABLE=1        开启：管理端口改以 HTTPS 提供服务（地址不变，访问改 https://），
//	                    Go 自动启用 HTTP/2——浏览器把所有请求复用在一条连接上，
//                    明文 HTTP 跨境链路"按连接概率重置"的干扰模式基本失效
//	TLS_SAN=IP或域名    追加证书 SAN（逗号分隔；默认已含 127.0.0.1/localhost/
//	                    容器主机名/出口 IP，覆盖不了的特殊域名在这里补）
//
// 证书首次启动生成于配置目录（tls.crt/tls.key），重启复用——浏览器只需
// 一次"高级→继续前往"。SAN 需求变化（如换了出口 IP）自动重新生成。

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TLSEnabled 管理后台是否启用 HTTPS
func TLSEnabled() bool {
	return strings.TrimSpace(os.Getenv("TLS_ENABLE")) != ""
}

// TLSCertPaths 证书/私钥文件路径（配置目录下）
func (c *Config) TLSCertPaths() (cert, key string) {
	return filepath.Join(c.ConfigDir, "tls.crt"), filepath.Join(c.ConfigDir, "tls.key")
}

// EnsureTLSCert 确保证书存在且覆盖当前 SAN 需求，返回 cert/key 路径。
// 已有证书但缺新 SAN（出口 IP 变化/用户补了 TLS_SAN）时自动重新生成
func (c *Config) EnsureTLSCert() (string, string, error) {
	certPath, keyPath := c.TLSCertPaths()
	wanted := tlsWantedSANs()

	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err == nil && time.Now().Before(leaf.NotAfter) && tlsCoversSANs(leaf, wanted) {
			return certPath, keyPath, nil // 复用
		}
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("生成密钥失败: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", err
	}
	tpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "StrmHub"},
		NotBefore:    time.Now().Add(-time.Hour), // 容器时钟略偏也不至于"尚未生效"
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range wanted {
		if ip := net.ParseIP(h); ip != nil {
			tpl.IPAddresses = append(tpl.IPAddresses, ip)
		} else {
			tpl.DNSNames = append(tpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, &tpl, &tpl, &priv.PublicKey, priv)
	if err != nil {
		return "", "", fmt.Errorf("生成证书失败: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(c.ConfigDir, 0755); err != nil {
		return "", "", err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return "", "", err
	}
	return certPath, keyPath, nil
}

// tlsWantedSANs 证书应覆盖的主机名集合：环回 + 容器主机名 + 出口 IP +
// TLS_SAN 追加项，去重保序
func tlsWantedSANs() []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	add("127.0.0.1")
	add("localhost")
	if host, err := os.Hostname(); err == nil {
		add(host)
	}
	// 出口 IP：UDP 拨号不发包，仅让路由选路得到 NAT 后的公网出口
	if conn, err := net.Dial("udp", "223.5.5.5:53"); err == nil {
		if local, ok := conn.LocalAddr().(*net.UDPAddr); ok {
			add(local.IP.String())
		}
		conn.Close()
	}
	for _, s := range strings.Split(os.Getenv("TLS_SAN"), ",") {
		add(s)
	}
	return out
}

// tlsCoversSANs 证书的 SAN 是否完整覆盖需求列表
func tlsCoversSANs(leaf *x509.Certificate, wanted []string) bool {
	for _, w := range wanted {
		if !tlsHasSAN(leaf, w) {
			return false
		}
	}
	return true
}

func tlsHasSAN(leaf *x509.Certificate, host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		for _, have := range leaf.IPAddresses {
			if have.Equal(ip) {
				return true
			}
		}
		return false
	}
	return tlsHasDNS(leaf, host)
}

func tlsHasDNS(leaf *x509.Certificate, name string) bool {
	for _, d := range leaf.DNSNames {
		if strings.EqualFold(d, name) {
			return true
		}
	}
	return false
}
