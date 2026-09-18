package config

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"path/filepath"
	"strconv"
)

type Config struct {
	Port       int    // 管理后台端口
	ProxyPort  int    // 302代理端口
	PortalPort int    // 观影门户端口（6688；6665-6669 在浏览器不安全端口黑名单内不可用）
	DataDir    string // 数据目录
	ConfigDir  string // 配置目录
	JWTSecret  string // JWT密钥
}

func Load() *Config {
	port := getEnvInt("PORT", 6060)
	proxyPort := getEnvInt("PROXY_PORT", 6086)
	portalPort := getEnvInt("PORTAL_PORT", 6688)
	return &Config{
		Port:       port,
		ProxyPort:  proxyPort,
		PortalPort: portalPort,
		DataDir:    getEnv("DATA_DIR", "/data"),
		ConfigDir:  getEnv("CONFIG_DIR", "/config"),
		JWTSecret:  loadOrCreateJWTSecret(getEnv("CONFIG_DIR", "/config")),
	}
}

// loadOrCreateJWTSecret JWT 密钥：环境变量 > 配置目录里持久化的随机密钥。
// 不再用硬编码默认值——公网部署下固定默认密钥等于任何人都能伪造管理 token；
// 随机密钥首次启动生成一次并落盘，重启后 token 依然有效
func loadOrCreateJWTSecret(configDir string) string {
	if v := os.Getenv("JWT_SECRET"); v != "" {
		return v
	}
	path := filepath.Join(configDir, "jwt.key")
	if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
		return string(b)
	}
	// 生成 64 位十六进制随机密钥
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		log.Fatalf("生成 JWT 密钥失败: %v", err)
	}
	secret := hex.EncodeToString(buf)
	if err := os.MkdirAll(configDir, 0700); err != nil {
		log.Fatalf("创建配置目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(secret), 0600); err != nil {
		log.Printf("○ JWT 密钥落盘失败（每次重启后已登录会话失效）: %v", err)
	}
	return secret
}

func (c *Config) PortStr() string {
	return strconv.Itoa(c.Port)
}

func (c *Config) ProxyPortStr() string {
	return strconv.Itoa(c.ProxyPort)
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}
