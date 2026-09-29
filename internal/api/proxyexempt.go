package api

import (
	"context"
	"net"
	"net/url"
	"sync"
	"time"
)

// 直链限流对 Emby 服务器放行（proxyRateAllow）。
//
// 只认本站 Emby 设置里那一个服务器地址解析出的 IP，不对整个内网放行：挂在反代后面的部署
// （SetTrustedProxies(nil)，见 AGENTS §6.4）里公网请求看到的都是反代的内网 IP，整网放行等于关掉限流。
//
// 服务器地址是 localhost / 127.0.0.1 时，Emby 和本站在同一台机器上，但 Emby 请求 STRM 用的是
// STRM 里写的地址（常见是本机局域网 IP），来源 IP 就是本机网卡地址而不是回环 —— 所以回环时连本机
// 所有网卡地址一起放行。代价是同机的反代也会被放行，这种组合下没法区分两者。

const proxyExemptTTL = time.Minute

var proxyExempt struct {
	mu  sync.Mutex
	ips map[string]bool
	at  time.Time
}

func proxyIsEmbyServer(ip string) bool {
	client := net.ParseIP(ip)
	if client == nil {
		return false
	}
	return proxyEmbyIPs()[client.String()]
}

// proxyEmbyIPs 每个 /d/ 请求都要查，整份缓存一分钟：配置改了、容器 IP 变了一分钟内跟上，
// 又不至于每次起播都读库、解析 DNS
func proxyEmbyIPs() map[string]bool {
	proxyExempt.mu.Lock()
	defer proxyExempt.mu.Unlock()
	if proxyExempt.ips != nil && time.Since(proxyExempt.at) < proxyExemptTTL {
		return proxyExempt.ips
	}
	server := ""
	if cfg, ok := loadEmbyRefreshCfg(); ok {
		server = cfg.ServerURL
	}
	ips := embyServerIPs(server, lookupHostIPs, localIPs)
	proxyExempt.ips, proxyExempt.at = ips, time.Now()
	return ips
}

// embyServerIPs 纯函数，解析与本机地址注入，便于测试
func embyServerIPs(serverURL string, lookup func(string) []net.IP, local func() []net.IP) map[string]bool {
	out := map[string]bool{}
	if serverURL == "" {
		return out
	}
	u, err := url.Parse(serverURL)
	if err != nil || u.Hostname() == "" {
		return out
	}
	host := u.Hostname()
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		ips = lookup(host)
	}
	loop := false
	for _, ip := range ips {
		out[ip.String()] = true
		if ip.IsLoopback() {
			loop = true
		}
	}
	if loop {
		out["127.0.0.1"], out["::1"] = true, true
		for _, ip := range local() {
			out[ip.String()] = true
		}
	}
	return out
}

func lookupHostIPs(host string) []net.IP {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	return ips
}

func localIPs() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var ips []net.IP
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			ips = append(ips, n.IP)
		}
	}
	return ips
}
