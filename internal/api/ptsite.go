package api

// ==================== PT 站签到：站点与预置目录 ====================
//
// 2026-10-26，设计见 docs/115-station-notes/PT-SIGNIN-PLAN.md。
// 和 115 没有关系：不发 115 请求、不进任务队列、不拿 taskMu。
//
// 预置目录只是「填表的便利」：域名、显示名、走哪个处理器、要什么凭据。
// 目录里没有的站填地址加，走通用处理器（NexusPHP 的 attendance.php）。
// 各站做法读自 MoviePilot 官方插件「站点自动签到」plugins.v3/autosignin/sites/*.py（GPL-3.0，只读、Go 重写）。

import (
	"fmt"
	"net/url"
	"strings"

	"115-station/internal/model"
)

// 处理器名
const (
	ptHandNexus    = "nexus"    // 通用：GET attendance.php
	ptHandPttime   = "pttime"   // PT时间
	ptHandAgsv     = "agsvpt"   // AGSVPT：有「立即签到」按钮才 POST
	ptHandBtschool = "btschool" // 学校：index.php?action=addbonus
	ptHandHaidan   = "haidan"   // 海胆之家：signin.php 后读 index.php
	ptHandHdarea   = "hdarea"   // 高清视界：POST sign_in.php
	ptHandTTG      = "ttg"      // 听听歌：首页取 token，POST signed.php
	ptHandRousi    = "rousi"    // Rousi Pro：JSON 接口 + API Token
	ptHandKeep     = "keep"     // 没有签到、只能保号（馒头 / 朱雀，第二期）
	ptHandRe0      = "re0"      // 影巢 = RE0，签到在 re0checkin.go
)

// 签到方式
const (
	ptModeSignin = "signin"
	ptModeKeep   = "keep" // 只保号：每天访问一次首页确认登录
)

// ptPreset 预置站点
type ptPreset struct {
	Key     string   `json:"key"`
	Name    string   `json:"name"`
	Domains []string `json:"domains"` // 第一个是默认地址；其余是别名（站点换过域名、截图和 MP 写的不一样）
	Handler string   `json:"handler"`
	Cred    string   `json:"cred"`           // cookie / token
	Note    string   `json:"note,omitempty"` // 界面上的一句说明
	// Unavailable 第一期做不了：目录里显示、选不了（第二期 / 已在别处处理）
	Unavailable bool `json:"unavailable,omitempty"`
}

// ptPresets 截图里的站（用户反馈 2026-10-26）。MP 里有、截图里没有的专用站第一期不做，有人要再加
var ptPresets = []ptPreset{
	{Key: "zmpt", Name: "织梦", Domains: []string{"zmpt.cc"}, Handler: ptHandNexus},
	{Key: "crabpt", Name: "蟹黄堡", Domains: []string{"crabpt.vip"}, Handler: ptHandNexus},
	{Key: "hdkyl", Name: "麒麟", Domains: []string{"www.hdkyl.in"}, Handler: ptHandNexus},
	{Key: "cyanbug", Name: "大青虫", Domains: []string{"cyanbug.net"}, Handler: ptHandNexus},
	{Key: "carpt", Name: "CARPT", Domains: []string{"carpt.net"}, Handler: ptHandNexus},
	{Key: "ubits", Name: "UBits", Domains: []string{"ubits.club"}, Handler: ptHandNexus},
	{Key: "hhanclub", Name: "憨憨", Domains: []string{"hhanclub.top"}, Handler: ptHandNexus},
	{Key: "hddolby", Name: "高清杜比", Domains: []string{"www.hddolby.com"}, Handler: ptHandNexus},
	{Key: "soulvoice", Name: "聆音", Domains: []string{"pt.soulvoice.club"}, Handler: ptHandNexus},
	{Key: "keepfrds", Name: "朋友", Domains: []string{"pt.keepfrds.com"}, Handler: ptHandNexus},
	{Key: "htpt", Name: "海棠", Domains: []string{"www.htpt.cc"}, Handler: ptHandNexus},
	{Key: "audiences", Name: "观众", Domains: []string{"audiences.me"}, Handler: ptHandNexus},
	{Key: "ourbits", Name: "我堡", Domains: []string{"ourbits.club"}, Handler: ptHandNexus},
	{Key: "xloli", Name: "萝莉", Domains: []string{"mua.xloli.cc"}, Handler: ptHandNexus},
	{Key: "nicept", Name: "老师", Domains: []string{"www.nicept.net"}, Handler: ptHandNexus},
	{Key: "oshen", Name: "OshenPT", Domains: []string{"www.oshen.win"}, Handler: ptHandNexus},
	{Key: "qingwa", Name: "青蛙", Domains: []string{"www.qingwapt.com"}, Handler: ptHandNexus},
	{Key: "ultrahd", Name: "ultrahd", Domains: []string{"ultrahd.net"}, Handler: ptHandNexus},
	{Key: "hdfans", Name: "红豆饭", Domains: []string{"hdfans.org"}, Handler: ptHandNexus},
	{Key: "hdtime", Name: "高清时间", Domains: []string{"hdtime.org"}, Handler: ptHandNexus},
	{Key: "hdvideo", Name: "HDVIDEO", Domains: []string{"hdvideo.one"}, Handler: ptHandNexus},
	{Key: "ptchina", Name: "铂金学院", Domains: []string{"ptchina.org"}, Handler: ptHandNexus},
	{Key: "wintersakura", Name: "冬樱", Domains: []string{"wintersakura.net"}, Handler: ptHandNexus},

	{Key: "pttime", Name: "PT时间", Domains: []string{"www.pttime.org"}, Handler: ptHandPttime},
	{Key: "agsvpt", Name: "AGSVPT", Domains: []string{"www.agsvpt.com", "pt.agsvpt.cn"}, Handler: ptHandAgsv},
	{Key: "btschool", Name: "学校", Domains: []string{"pt.btschool.club"}, Handler: ptHandBtschool},
	{Key: "haidan", Name: "海胆之家", Domains: []string{"www.haidan.video", "www.haidan.cc"}, Handler: ptHandHaidan},
	{Key: "hdarea", Name: "高清视界", Domains: []string{"hdarea.club"}, Handler: ptHandHdarea},
	{Key: "ttg", Name: "听听歌", Domains: []string{"totheglory.im"}, Handler: ptHandTTG},
	{Key: "rousi", Name: "Rousi Pro", Domains: []string{"rousi.pro"}, Handler: ptHandRousi, Cred: "token",
		Note: "填站点「控制面板 → API」里生成的 API Token"},

	{Key: "mteam", Name: "馒头", Domains: []string{"kp.m-team.cc"}, Handler: ptHandKeep, Unavailable: true,
		Note: "没有签到，只能刷访问时间保号，第二期做"},
	{Key: "zhuque", Name: "朱雀", Domains: []string{"zhuque.in"}, Handler: ptHandKeep, Unavailable: true,
		Note: "没有签到（每日是释放角色技能领灵石），第二期做"},
	{Key: "hdhive", Name: "影巢", Domains: []string{"hdhive.com", "re0.me"}, Handler: ptHandRe0, Unavailable: true,
		Note: "就是 RE0，签到在「发现与订阅 → 资源来源 → RE0」"},
}

func init() {
	for i := range ptPresets {
		if ptPresets[i].Cred == "" {
			ptPresets[i].Cred = "cookie"
		}
	}
}

// ptPresetOf 按 key 找预置站
func ptPresetOf(key string) *ptPreset {
	for i := range ptPresets {
		if ptPresets[i].Key == key {
			return &ptPresets[i]
		}
	}
	return nil
}

// ptHostOf 地址的主机名（小写、不带端口）
func ptHostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// ptHostMatch 主机名是不是这个域名（或它的子域；www 前缀两边都忽略）
func ptHostMatch(host, domain string) bool {
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	domain = strings.TrimPrefix(strings.ToLower(domain), "www.")
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// ptPresetByHost 按地址认预置站（自定义站填了预置站的地址时也走专用流程）
func ptPresetByHost(raw string) *ptPreset {
	host := ptHostOf(raw)
	if host == "" {
		return nil
	}
	for i := range ptPresets {
		for _, d := range ptPresets[i].Domains {
			if ptHostMatch(host, d) {
				return &ptPresets[i]
			}
		}
	}
	return nil
}

// ptHandlerOf 这个站走哪个处理器：先认 Preset，没有再按地址认，都认不出走通用
func ptHandlerOf(s *model.PtSite) string {
	if p := ptPresetOf(s.Preset); p != nil {
		return p.Handler
	}
	if p := ptPresetByHost(s.BaseURL); p != nil {
		return p.Handler
	}
	return ptHandNexus
}

// normPtBaseURL 规范化站点地址：补 https、只留到根「/」；本身写着 attendance.php 的保留原样（MP 同款）
func normPtBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("站点地址不能为空")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("站点地址要写成 https://站点/ 这样")
	}
	if strings.Contains(strings.ToLower(u.Path), "attendance.php") {
		u.RawQuery, u.Fragment = "", ""
		return u.String(), nil
	}
	return u.Scheme + "://" + u.Host + "/", nil
}

// ptRootOf 站点根地址（去掉 attendance.php 之类的路径）
func ptRootOf(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return base
	}
	return u.Scheme + "://" + u.Host + "/"
}

// ptJoin 站点根 + 相对路径
func ptJoin(base, path string) string {
	return ptRootOf(base) + strings.TrimPrefix(path, "/")
}

// ptCookieOK 粗看贴进来的 Cookie 像不像：至少一对 k=v。全错的（贴了整段请求头、贴了 JSON）早点说
func ptCookieOK(c string) bool {
	c = strings.TrimSpace(c)
	if c == "" || strings.ContainsAny(c, "\n\r{") {
		return false
	}
	for _, kv := range strings.Split(c, ";") {
		if i := strings.IndexByte(kv, '='); i > 0 {
			return true
		}
	}
	return false
}

// normPtCookie 去掉常见的多余前缀与换行（从开发者工具复制时常带上「Cookie: 」）
func normPtCookie(c string) string {
	c = strings.TrimSpace(c)
	if len(c) > 7 && strings.EqualFold(c[:7], "cookie:") {
		c = strings.TrimSpace(c[7:])
	}
	return strings.Join(strings.Fields(strings.ReplaceAll(strings.ReplaceAll(c, "\r", " "), "\n", " ")), " ")
}
