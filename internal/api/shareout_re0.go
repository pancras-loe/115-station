package api

// RE0 发布分享（shareout.go 调）。两条通道，和查资源 / 解锁一样：已授权 OpenAPI 的优先，否则网页登录。
//
//   - OpenAPI：POST /api/open/shares（scope write，re0Scope 早就申请了）。字段与枚举照官方文档，
//     枚举值另见 p115strmhelper `helper/hdhive/open/client.py`。
//   - 网页：站点详情页「分享资源」弹窗提交的是普通 REST —— POST /api/customer/resources（2026-10-17 读站点前端
//     static/chunks/8539-*.js 得到的，不是 Server Action）。字段和 OpenAPI 基本一样，只是影视关联用站内 id（movie_id / tv_id），
//     不收 TMDB 编号。登录态 JWT 在 Cookie 里，站点的请求客户端会把 Cookie `csrf_access_token` 放进 X-CSRF-TOKEN 请求头，
//     自己 fetch 时要照做。站内 id 从 /tmdb/{类型}/{编号} 跳转后的地址里取（/movie/{id}、/tv/{id}）。
//   - 离线链接：「分享链接」一栏可以填 ed2k / 磁力，几条换行（维护者在网页上确认过；OpenAPI 文档的网盘类型里也有 magnet、ed2k）。
//     url 照填、不带访问码。**还没真发过一次**，站点对多条链接的校验第一次看日志 [RE0网页]。

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod"
)

// re0ShareReady RE0 现在能不能发分享（空 = 能），界面提示用
func re0ShareReady(cfg *re0Cfg) string {
	switch re0Channel(cfg) {
	case "openapi":
		if !cfg.grantedScopes()["write"] {
			return "OpenAPI 授权里没有 write 权限，请重新授权一次"
		}
		return ""
	case "web":
		return ""
	}
	return "还没配置 RE0（网页登录或 OpenAPI）"
}

// re0ShareBody 两条通道共用的请求体（字段名照 OpenAPI）；网页通道把 tmdb_id + media_type 换成站内 id
func re0ShareBody(pl shareOutPayload, cfg shareOutSiteCfg) map[string]any {
	body := map[string]any{
		"url":       pl.URL,
		"title":     shareOutHeadline(pl),
		"hide_link": true,
	}
	if r := shareOutRemark(pl); r != "" {
		body["remark"] = r
	}
	if pl.Code != "" {
		body["access_code"] = pl.Code
	}
	if s := humanSize(pl.SizeBytes); s != "" {
		body["share_size"] = s
	}
	if r := re0ResolutionOf(pl.Info.Pix); r != "" {
		body["video_resolution"] = []string{r}
	}
	if s := re0SourceOf(pl.Info, pl.Ext); s != "" {
		body["source"] = []string{s}
	}
	if pl.HasSubs {
		body["subtitle_type"] = []string{"外挂"}
		if len(pl.SubLangs) > 0 {
			body["subtitle_language"] = pl.SubLangs
		}
	}
	if cfg.UnlockPoints > 0 {
		body["unlock_points"] = cfg.UnlockPoints
	}
	if cfg.Anonymous {
		body["is_anonymous"] = true
	}
	return body
}

// re0ResolutionOf 分辨率 → RE0 的枚举（480P / 720P / 1080P / 2K / 4K / 8K），认不出不填
func re0ResolutionOf(pix string) string {
	switch strings.ToLower(pix) {
	case "4320p":
		return "8K"
	case "2160p":
		return "4K"
	case "1440p":
		return "2K"
	case "1080p":
		return "1080P"
	case "720p":
		return "720P"
	case "480p", "576p":
		return "480P"
	}
	return ""
}

// re0SourceOf 片源 → RE0 的枚举。认不出就不填，别瞎填：填错了会被当成虚标
func re0SourceOf(ri ResourceInfo, ext string) string {
	t := strings.ToUpper(ri.Type)
	switch {
	case ext == "ISO":
		return "蓝光原盘/ISO"
	case t == "REMUX" || strings.Contains(strings.ToUpper(ri.VideoEncode), "REMUX"):
		return "蓝光原盘/REMUX"
	case t == "BLURAY":
		return "BDRip/BluRayEncode"
	case t == "WEB-DL" || t == "WEBRIP":
		return "WEB-DL/WEBRip"
	case t == "HDTV":
		return "HDTV/HDTVRip"
	}
	return ""
}

// re0PublishShare 发到 RE0（shareOutPublishers 里注册）
func re0PublishShare(pl shareOutPayload, cfg shareOutSiteCfg) (string, int, error) {
	// 离线链接（ed2k / 磁力）RE0 也收，几条换行放在 url 里（网页「分享资源」弹窗同样的填法）
	if pl.Offline == "" && (!strings.Contains(pl.URL, "115") || pl.ShareCode == "") {
		return "", 0, shareOutSkipf("不是 115 分享链接")
	}
	if shareOutBadSource(pl.Info) {
		return "", 0, shareOutSkipf("枪版资源，RE0 不收")
	}
	rc := loadRe0Cfg()
	if why := re0ShareReady(rc); why != "" {
		return "", 0, shareOutSkipf("%s", why)
	}
	body := re0ShareBody(pl, cfg)
	if re0Channel(rc) == "openapi" {
		body["tmdb_id"] = fmt.Sprint(pl.TmdbID)
		body["media_type"] = pl.MediaType
		var out struct {
			Slug string `json:"slug"`
		}
		if err := re0Call(nil, rc, http.MethodPost, "/api/open/shares", nil, body, &out); err != nil {
			return "", 0, re0ShareErr(err)
		}
		return out.Slug, 0, nil
	}
	return re0WebPublishShare(pl, body)
}

// re0ShareErr 站点的错误里分出「不该再试」的：只有限流、5xx 这种过一会儿可能好的才重试，
// 其余（重复、链接无效、权限不够、参数不对）重试也一样被拒。重复不一定是 4xx，也可能是 200 + success:false
func re0ShareErr(err error) error {
	var e *re0Err
	if !errors.As(err, &e) {
		return err // 网络错误、要重新授权：等下一轮
	}
	code := strings.ToUpper(e.Code)
	if e.Status == http.StatusTooManyRequests || e.Status >= 500 || strings.Contains(code, "RATE_LIMIT") || code == "OPENAPI_COOLDOWN" {
		return err
	}
	return shareOutRefusal("RE0", firstNonEmpty(e.Description, e.Message, e.Code))
}

// re0RSCJS 对站内详情页要一次 RSC（服务端照样给，不用等前端渲染）
const re0RSCJS = `async (path) => {
  try { const r = await fetch(path, { headers: { RSC: '1' }, credentials: 'include' }); return await r.text() } catch (e) { return '' }
}`

var (
	re0SharePropsRe = regexp.MustCompile(`"movieId":(?:"?(\d*)"?),"tvId":(?:"?(\d*)"?)`)
	re0MovieNumRe   = regexp.MustCompile(`"movieId":(\d+)`)
	re0TvNumRe      = regexp.MustCompile(`"tvId":(\d+)`)
)

// re0StationNumOf 从详情页 RSC 里取分享按钮属性上的站内数字编号（{"movieId":273,"tvId":"",...}；剧集是 tvId）
func re0StationNumOf(rsc, kind string) string {
	// 先认分享按钮那组属性（movieId、tvId 挨着），页面别处以后多出 movieId 也不会取错；认不到再退回宽松匹配
	if m := re0SharePropsRe.FindStringSubmatch(rsc); m != nil {
		v := m[1]
		if kind == "tv" {
			v = m[2]
		}
		if v != "" && v != "0" {
			return v
		}
	}
	re := re0MovieNumRe
	if kind == "tv" {
		re = re0TvNumRe
	}
	if m := re.FindStringSubmatch(rsc); m != nil && m[1] != "0" {
		return m[1]
	}
	return ""
}

// re0BodyKeys 日志里列出提交了哪些字段（不打值：带着访问码）
func re0BodyKeys(body map[string]any) string {
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

var re0StationIDRe = regexp.MustCompile(`/(movie|tv)/([A-Za-z0-9_-]{6,})(?:[/?#]|$)`)

// re0WebSignedJS 在页面里调站点自己的接口（发布分享、读文件预览），**必须走站点的 signedFetch**（2026-10-18 现场：普通 fetch 带着 Cookie 与
// X-CSRF-TOKEN 照样 401）。站点的请求客户端（axios + 自定义 adapter，app layout chunk）只对一小串白名单路径直接 fetch，
// 其余 /api/ 一律 signedFetch：会话握手、请求签名、过期重握手都在里面，Go 这边重写不了，只能借它。
// 它在 webpack 模块里（导出名 signedFetch），运行时没暴露模块缓存，只有工厂表 .m：按源码里的「signedFetch:()=>」找到模块 id 再
// require——已经加载过的模块 require 直接返回同一份导出、不会重新执行；站点重新部署后 id 变了也照样找得到。
// 401 且没有错误码时照站点的做法先 /api/public/auth/refresh 刷新登录态再重试一次（访问令牌过期就是这种）。
// X-CSRF-TOKEN 照站点请求拦截器取 Cookie `csrf_access_token`。
// body 为 null 时发 GET（订阅读文件预览 /api/customer/resources/{slug}/file-list，re0webpreview.go），否则 POST JSON
const re0WebSignedJS = `async (path, body) => {
  let req
  try {
    const chunks = self.webpackChunk_N_E
    if (!chunks) return { status: 0, text: '页面里没有 webpack 运行时（站点可能改版）' }
    chunks.push([[Symbol('s115')], {}, (r) => { req = r }])
  } catch (e) { return { status: 0, text: '拿不到 webpack 运行时：' + e } }
  let sf
  for (const id of Object.keys((req && req.m) || {})) {
    let src = ''
    try { src = String(req.m[id]) } catch {}
    if (!src.includes('signedFetch:()=>')) continue
    try { const ex = req(id); if (ex && typeof ex.signedFetch === 'function') { sf = ex.signedFetch; break } } catch {}
  }
  if (!sf) return { status: 0, text: '页面里找不到站点的 signedFetch（站点可能改版）' }
  const get = body == null
  const headers = () => {
    const h = new Headers({ 'Accept': 'application/json, text/plain, */*' })
    if (!get) h.set('Content-Type', 'application/json')
    const m = document.cookie.match(/(?:^|;\s*)csrf_access_token=([^;]+)/)
    if (m) h.set('X-CSRF-TOKEN', decodeURIComponent(m[1]))
    return h
  }
  const send = () => sf(path, get ? { method: 'GET', credentials: 'include', headers: headers() }
    : { method: 'POST', credentials: 'include', headers: headers(), body: JSON.stringify(body) })
  try {
    let r = await send()
    if (r.status === 401) {
      let code = ''
      // 同站点拦截器：只有字符串错误码才算「别刷新」，{"code":401,"message":"请先登录"} 这种数字 code 照样刷新
      try {
        const j = await r.clone().json()
        code = typeof j.code === 'string' ? j.code : typeof j.error_code === 'string' ? j.error_code : ''
      } catch {}
      if (!code) {
        const rr = await sf('/api/public/auth/refresh', { method: 'POST', credentials: 'include', cache: 'no-store',
          headers: { 'Content-Type': 'application/json', 'x-skip-auth-refresh': 'true' } })
        if (rr.ok) r = await send()
      }
    }
    return { status: r.status, text: await r.text() }
  } catch (e) {
    return { status: 0, text: '站点签名请求出错：' + ((e && e.message) || e) }
  }
}`

// re0WebPublishShare 网页通道：打开 /tmdb/{类型}/{编号}，等它跳到站内详情页拿站内 id，再在页面里调发布接口
func re0WebPublishShare(pl shareOutPayload, body map[string]any) (string, int, error) {
	target := fmt.Sprintf("/tmdb/%s/%d", pl.MediaType, pl.TmdbID)
	var ref string
	err := re0Web("发布分享 "+pl.MediaType+"/"+fmt.Sprint(pl.TmdbID), target, 3*time.Minute, func(p *rod.Page) error {
		re0Step("找站内条目")
		var kind, id string
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if m := re0StationIDRe.FindStringSubmatch(re0URL(p)); m != nil {
				kind, id = m[1], m[2]
				break
			}
			if re0OnLogin(p) {
				return fmt.Errorf("登录态失效（被跳回登录页）")
			}
			time.Sleep(300 * time.Millisecond)
		}
		if id == "" {
			log.Printf("[RE0网页]   %s 没跳到站内详情页（停在 %s）", target, re0URL(p))
			return shareOutSkipf("RE0 上没有这部片的条目")
		}
		if kind != pl.MediaType {
			return shareOutSkipf("RE0 把这部片认成了%s", map[string]string{"movie": "电影", "tv": "剧集"}[kind])
		}
		// 地址里的是站内 uuid，发布接口要的 movie_id / tv_id 是站内**数字**编号（2026-10-18 现场：传 uuid 回「参数错误」）。
		// 数字编号只在详情页分享按钮的属性里（RSC：{"movieId":273,"tvId":"",...}）。/tmdb/... 到 /movie/... 是前端跳转，
		// 当前页面脚本里装的是跳转前那页，所以对详情页单独要一次 RSC
		re0Step("读站内编号")
		num := ""
		if r, err := p.Eval(re0RSCJS, "/"+kind+"/"+id); err == nil {
			num = re0StationNumOf(r.Value.Str(), kind)
		}
		if num == "" {
			log.Printf("[RE0网页]   /%s/%s 的页面数据里没找到分享按钮的站内编号（站点可能改版）", kind, id)
			return fmt.Errorf("RE0 详情页里读不到站内编号（页面可能改版）")
		}
		n, _ := strconv.Atoi(num)
		if kind == "tv" {
			body["tv_id"] = n
		} else {
			body["movie_id"] = n
		}
		// 同站点表单：先让站点解析一遍链接（check/resource），用它规范化过的链接与访问码。失败不影响发布
		re0Step("检查分享链接")
		if r, err := p.Eval(re0WebSignedJS, "/api/customer/check/resource", map[string]any{"url": body["url"]}); err == nil &&
			r.Value.Get("status").Int() == http.StatusOK {
			var chk struct {
				Data struct {
					Website    string `json:"website"`
					URL        string `json:"url"`
					BaseLink   string `json:"base_link"`
					AccessCode string `json:"access_code"`
				} `json:"data"`
			}
			if json.Unmarshal([]byte(r.Value.Get("text").Str()), &chk) == nil {
				switch {
				case pl.Offline != "":
					// 离线链接不拿站点规范化过的：几条换行的链接它可能只认第一条
					if chk.Data.Website != "" && !strings.EqualFold(chk.Data.Website, pl.Offline) {
						log.Printf("[RE0网页]   站点把这条离线链接认成 %s（本站认的是 %s），照原样提交", chk.Data.Website, pl.Offline)
					}
				case chk.Data.Website != "" && chk.Data.Website != "115":
					return shareOutSkipf("RE0 认出的网盘类型是 %s，不是 115", chk.Data.Website)
				case chk.Data.Website == "115":
					if u := firstNonEmpty(chk.Data.BaseLink, chk.Data.URL); u != "" {
						body["url"] = u
					}
					if chk.Data.AccessCode != "" {
						body["access_code"] = chk.Data.AccessCode
					}
				}
			}
		}
		re0Step("提交分享")
		res, err := p.Eval(re0WebSignedJS, "/api/customer/resources", body)
		if err != nil {
			return fmt.Errorf("页面里调用发布接口失败: %v", err)
		}
		status := res.Value.Get("status").Int()
		text := res.Value.Get("text").Str()
		var out struct {
			Success *bool  `json:"success"`
			Message string `json:"message"`
			Msg     string `json:"msg"`
			Code    string `json:"code"`
			ErrCode string `json:"error_code"`
			Data    struct {
				Slug string `json:"slug"`
				ID   any    `json:"id"`
			} `json:"data"`
		}
		_ = json.Unmarshal([]byte(text), &out)
		msg := firstNonEmpty(out.Message, out.Msg, out.ErrCode, out.Code)
		if status < 200 || status >= 300 || (out.Success != nil && !*out.Success) {
			// 站点原文进日志：「参数错误」这种笼统提示，具体是哪个字段常写在原文里
			log.Printf("[RE0网页]   发布接口 HTTP %d：%s（提交的字段 %s）", status, truncateStr(text, 300), re0BodyKeys(body))
		}
		switch {
		case status == http.StatusUnauthorized:
			// 刷新过一次还是 401：把站点给的错误码带上（session_user_mismatch / 签名问题 / 真过期，排查时分得开）
			return fmt.Errorf("RE0 登录态失效（HTTP 401）：%s", firstNonEmpty(msg, truncateStr(text, 80)))
		case status == 0:
			return fmt.Errorf("RE0 发布失败：%s", truncateStr(text, 120))
		case status >= 400 && status < 500 && status != http.StatusTooManyRequests:
			return shareOutRefusal("RE0", firstNonEmpty(msg, fmt.Sprintf("HTTP %d", status)))
		case status < 200 || status >= 300:
			return fmt.Errorf("RE0 发布失败（HTTP %d）：%s", status, firstNonEmpty(msg, truncateStr(text, 80)))
		case out.Success != nil && !*out.Success:
			return shareOutRefusal("RE0", firstNonEmpty(msg, "未说明原因"))
		}
		ref = out.Data.Slug
		if ref == "" && out.Data.ID != nil {
			ref = fmt.Sprint(out.Data.ID)
		}
		log.Printf("[RE0网页]   分享已发布（%s，站内 %s/%s）", firstNonEmpty(ref, "没返回编号"), kind, id)
		return nil
	})
	return ref, 0, err
}
