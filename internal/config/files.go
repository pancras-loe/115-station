package config

import (
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

// ===== 路径辅助 =====

func (c *Config) AuthFile() string {
	return filepath.Join(c.ConfigDir, "auth.yaml")
}

func (c *Config) CookieFile() string {
	return filepath.Join(c.ConfigDir, "115-cookie.txt")
}

func (c *Config) SettingFile() string {
	return filepath.Join(c.ConfigDir, "setting.yaml")
}

// ===== 管理员账号（auth.yaml）=====
//
// 账号来源：环境变量 AUTH_USER / AUTH_PASSWORD（推荐，容器部署在 compose 里
// 写死）> 已有 auth.yaml（历史保留）> 启动时自动生成随机密码并打印日志。
// 网页注册功能已移除——首次账号一律来自环境变量或启动日志

type AuthConfig struct {
	Username     string `yaml:"username"`
	PasswordHash string `yaml:"password_hash"`
}

// IsAuthExists 检查是否已有管理员
func (c *Config) IsAuthExists() bool {
	_, err := os.Stat(c.AuthFile())
	return err == nil
}

// EnsureAdmin 保证存在可用管理员：环境变量优先（每次启动同步一次，改环境
// 变量即改密码）；否则沿用 auth.yaml；都没有则生成随机密码落盘并打印日志
func (c *Config) EnsureAdmin() error {
	envUser := strings.TrimSpace(os.Getenv("AUTH_USER"))
	envPass := os.Getenv("AUTH_PASSWORD")
	if envUser != "" && envPass != "" {
		// 环境变量即权威：与 auth.yaml 不一致时静默同步（改 env 即改密）
		if auth, err := c.LoadAuth(); err != nil || auth.Username != envUser ||
			!c.VerifyAuth(envUser, envPass) {
			if err := c.SaveAuth(envUser, envPass); err != nil {
				return err
			}
			log.Println("[账号] ✓ 管理员账号已按环境变量 AUTH_USER/AUTH_PASSWORD 设置")
		}
		return nil
	}
	if c.IsAuthExists() {
		return nil // 历史账号继续有效
	}
	// 首次部署且未配置环境变量：生成随机凭据（用户名 admin，密码随机 12 位）
	pass := randomPassword(12)
	if err := c.SaveAuth("admin", pass); err != nil {
		return err
	}
	log.Printf("════════════════════════════════════════════════")
	log.Printf("  首次启动：已生成管理员账号（请立即查看并记录）")
	log.Printf("  用户名：admin")
	log.Printf("  密码：%s", pass)
	log.Printf("  建议在 docker-compose 中配置 AUTH_USER / AUTH_PASSWORD 固定账号")
	log.Printf("════════════════════════════════════════════════")
	return nil
}

// randomPassword 生成无易混淆字符的随机密码
func randomPassword(n int) string {
	const charset = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	for i := range b {
		idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		b[i] = charset[idx.Int64()]
	}
	return string(b)
}

// LoadAuth 读取管理员配置
func (c *Config) LoadAuth() (*AuthConfig, error) {
	data, err := os.ReadFile(c.AuthFile())
	if err != nil {
		return nil, err
	}
	var auth AuthConfig
	if err := yaml.Unmarshal(data, &auth); err != nil {
		return nil, err
	}
	return &auth, nil
}

// SaveAuth 保存管理员配置（密码会被 bcrypt 哈希）
func (c *Config) SaveAuth(username, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	auth := AuthConfig{
		Username:     username,
		PasswordHash: string(hash),
	}
	data, err := yaml.Marshal(&auth)
	if err != nil {
		return err
	}
	return os.WriteFile(c.AuthFile(), data, 0600)
}

// UpdateAuthUsername 只改用户名，保留原密码哈希
func (c *Config) UpdateAuthUsername(newUsername string) error {
	auth, err := c.LoadAuth()
	if err != nil {
		return err
	}
	auth.Username = newUsername
	data, err := yaml.Marshal(&auth)
	if err != nil {
		return err
	}
	return os.WriteFile(c.AuthFile(), data, 0600)
}

// VerifyAuth 验证账号密码
func (c *Config) VerifyAuth(username, password string) bool {
	auth, err := c.LoadAuth()
	if err != nil {
		return false
	}
	if auth.Username != username {
		return false
	}
	// 兼容明文密码（旧版本迁移）
	if auth.PasswordHash == "" || !strings.HasPrefix(auth.PasswordHash, "$2") {
		return auth.PasswordHash == password
	}
	return bcrypt.CompareHashAndPassword([]byte(auth.PasswordHash), []byte(password)) == nil
}

// ResetAuth 删除管理员文件（重置密码，不丢其他数据）
func (c *Config) ResetAuth() error {
	return os.Remove(c.AuthFile())
}

// ===== 115 Cookie（115-cookie.txt）=====

// LoadCookie 读取 115 Cookie
func (c *Config) LoadCookie() (string, error) {
	data, err := os.ReadFile(c.CookieFile())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// SaveCookie 写入 115 Cookie
func (c *Config) SaveCookie(cookie string) error {
	return os.WriteFile(c.CookieFile(), []byte(cookie), 0600)
}

// Load115Device 读取 115 登录设备类型（115-device.txt）
func (c *Config) Load115Device() string {
	data, err := os.ReadFile(filepath.Join(c.ConfigDir, "115-device.txt"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// Save115Device 写入 115 登录设备类型
func (c *Config) Save115Device(device string) error {
	return os.WriteFile(filepath.Join(c.ConfigDir, "115-device.txt"), []byte(device), 0644)
}

// ===== 115 开放平台 Token（115-open-token.yaml）=====

// OpenToken 115 开放平台 OAuth 凭证
type OpenToken struct {
	AppID        string `yaml:"app_id"`
	AccessToken  string `yaml:"access_token"`
	RefreshToken string `yaml:"refresh_token"`
	ExpiresAt    int64  `yaml:"expires_at"` // unix 秒
}

// OpenTokenFile token 文件路径
func (c *Config) OpenTokenFile() string {
	return filepath.Join(c.ConfigDir, "115-open-token.yaml")
}

// LoadOpenToken 读取开放平台 token
func (c *Config) LoadOpenToken() (*OpenToken, error) {
	data, err := os.ReadFile(c.OpenTokenFile())
	if err != nil {
		return nil, err
	}
	var t OpenToken
	if err := yaml.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// SaveOpenToken 写入开放平台 token
func (c *Config) SaveOpenToken(t *OpenToken) error {
	data, err := yaml.Marshal(t)
	if err != nil {
		return err
	}
	return os.WriteFile(c.OpenTokenFile(), data, 0600)
}

// ClearOpenToken 清除开放平台 token（重新授权时用）
func (c *Config) ClearOpenToken() error {
	if err := os.Remove(c.OpenTokenFile()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ===== 基础配置（setting.yaml）=====

// SettingFile 通用键值配置（value 为 JSON 字符串，与前端兼容）
type SettingMap map[string]string

// LoadSettings 读取所有配置
func (c *Config) LoadSettings() (SettingMap, error) {
	data, err := os.ReadFile(c.SettingFile())
	if err != nil {
		if os.IsNotExist(err) {
			return SettingMap{}, nil
		}
		return nil, err
	}
	var s SettingMap
	if err := yaml.Unmarshal(data, &s); err != nil {
		// 文件损坏（半截写入/磁盘问题）：读侧全部回空、写侧禁止覆盖——
		// 只报一次，避免 GetSetting 热路径刷日志
		settingsCorruptLog.Do(func() {
			log.Printf("[配置] ⚠ setting.yaml 解析失败（%v），全部配置读取回空；已禁止覆盖保存防二次丢失，请从系统备份恢复后重启", err)
		})
		return nil, err
	}
	if s == nil {
		s = SettingMap{}
	}
	return s, nil
}

// settingsMu 配置文件读改写互斥：并发的 POST /config/setting 同时
// LoadSettings→改→WriteFile 会互相覆盖丢 key
var settingsMu sync.Mutex

// settingsCorruptLog 配置文件损坏告警只发一次（读热路径不刷屏）
var settingsCorruptLog sync.Once

// GetSetting 读取单个配置
func (c *Config) GetSetting(key string) string {
	s, err := c.LoadSettings()
	if err != nil {
		return ""
	}
	return s[key]
}

// SaveSetting 保存单个配置（保留其他配置）。临时文件+rename 原子落盘：
// 进程中途被杀/断电不会留下截断的 setting.yaml。
// 读失败（文件损坏等）绝不能拿空表覆盖——那会把其他所有配置一键清空，
// 此处直接中止保存并报错，原始文件保留待从备份恢复
func (c *Config) SaveSetting(key, value string) error {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	s, err := c.LoadSettings()
	if err != nil {
		return fmt.Errorf("读取现有配置失败，为防覆盖丢失已中止保存: %w", err)
	}
	s[key] = value
	data, err := yaml.Marshal(&s)
	if err != nil {
		return err
	}
	tmp := c.SettingFile() + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, c.SettingFile())
}

// EnsureConfigDir 确保配置目录存在
func (c *Config) EnsureConfigDir() error {
	return os.MkdirAll(c.ConfigDir, 0755)
}

// ConfigSummary 返回配置目录用于日志
func (c *Config) ConfigSummary() string {
	return fmt.Sprintf("配置目录: %s, 数据目录: %s", c.ConfigDir, c.DataDir)
}
