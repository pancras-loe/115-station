package api

import (
	"net"
	"testing"
)

func TestEmbyServerIPs(t *testing.T) {
	lookup := func(host string) []net.IP {
		if host == "emby" {
			return []net.IP{net.ParseIP("172.18.0.5")}
		}
		if host == "localhost" {
			return []net.IP{net.ParseIP("127.0.0.1")}
		}
		return nil
	}
	local := func() []net.IP { return []net.IP{net.ParseIP("192.168.31.35")} }

	cases := []struct {
		name, server string
		allow, deny  []string
	}{
		{"局域网 IP", "http://192.168.31.35:8096", []string{"192.168.31.35", "::ffff:192.168.31.35"}, []string{"192.168.31.36", "127.0.0.1"}},
		{"容器名", "http://emby:8096/", []string{"172.18.0.5"}, []string{"192.168.31.35"}},
		// 回环：Emby 按 STRM 里的局域网地址回来，来源是本机网卡 IP
		{"localhost", "http://localhost:8096", []string{"127.0.0.1", "::1", "192.168.31.35"}, []string{"192.168.31.36"}},
		{"未配置", "", nil, []string{"192.168.31.35", "127.0.0.1"}},
		{"解析失败", "http://nohost:8096", nil, []string{"192.168.31.35"}},
	}
	for _, c := range cases {
		ips := embyServerIPs(c.server, lookup, local)
		for _, ip := range c.allow {
			if !ips[net.ParseIP(ip).String()] {
				t.Errorf("%s: %s 应放行", c.name, ip)
			}
		}
		for _, ip := range c.deny {
			if ips[net.ParseIP(ip).String()] {
				t.Errorf("%s: %s 不应放行", c.name, ip)
			}
		}
	}
}
