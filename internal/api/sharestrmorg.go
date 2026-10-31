package api

// 分享 STRM 的「本地整理」布局（Layout = organize，SHARE-STRM-PLAN.md §5.3）：
// 只在本地整理 —— 识别、重命名模板、分类全照整理那一套算，算出来的位置直接写 STRM，网盘一侧什么都不做
// （文件在别人的分享里，也做不了）。
//
// 一个分享按一部片认（最常见的形态：一部电影、一部剧的一季或几季），识别跟整理一个目录条目同样的步骤：
// 挑样本视频 → 替换规则 → 文件名缺的由目录补 → TMDB。认出后把编号存在 ShareSource 上，之后的检查按编号取、
// 不再按名字搜（名字搜每次可能漂到别的条目上，本地文件就被搬来搬去）。
// 位置算法直接用重新整理的 planRedoLayoutOpt（纯函数）：季集、特别篇、字幕跟视频改名、NFO 分级都同一口径。
// 认不出（片名提取不出、TMDB 没有、同名几部分不出）时按原样放在分享目录下，页面上写原因、可以手动指定。

import (
	"errors"
	"fmt"
	"strings"

	"115-station/internal/model"
)

// shareOrgDeps 本地整理要用的东西（测试换成假的）
type shareOrgDeps struct {
	recognize func(*ParsedName) (*TmdbMedia, error)
	byID      func(id int, isTV bool) (*TmdbMedia, error)
	classify  func(*TmdbMedia) string // 分类目录（库内相对路径，含电影 / 剧集那一层）
	rules     []ReplaceRule
}

// recognizeShareEntries 按整理一个目录条目的办法认这个分享是哪一部。
// 返回 media 为 nil 时 note 写为什么没认出；err 只用于 TMDB 暂时连不上（不能当成「没认出」改动本地）
func recognizeShareEntries(entries []shareEntry, title string, rules []ReplaceRule, recognize func(*ParsedName) (*TmdbMedia, error)) (*TmdbMedia, string, error) {
	var videos []remoteFile
	for _, e := range entries {
		if !e.IsDir && classifyFile(e.Name) == FileTypeVideo {
			videos = append(videos, remoteFile{Fid: e.ID, Name: e.Name, Path: e.Dir, Size: e.Size})
		}
	}
	if len(videos) == 0 {
		return nil, "分享里没有视频", nil
	}
	main := pickMainVideo(videos, rules)
	name := main.Name
	if len(rules) > 0 {
		name = applyReplaceRules(name, rules)
	}
	// 分享标题当最外层的一级目录：单文件分享的标题就是文件名，多个顶层条目时常是第一个条目的名字
	extra := []string{}
	if t := strings.TrimSpace(title); t != "" && t != main.Name {
		extra = append(extra, t)
	}
	dirNames := pathDirs(main.Path, extra...)
	dirParses := parseDirs(dirNames, rules)
	parsed, titleFrom := mergePathContext(parseFileName(name), dirParses, dirNames)
	parsed.Source, parsed.Context = main.Name, dirNames
	if parsed.Title == "" && parsed.TmdbID == 0 {
		return nil, "文件名与目录名都提取不出片名", nil
	}
	media, err := recognize(parsed)
	if err == nil && media == nil && titleFrom == "" {
		// 文件名的片名可能是残缺的：换最近一级能用的目录名再试一次（同整理）
		for _, d := range dirParses {
			if !usableTitle(d.Title) || titleKey(d.Title) == titleKey(parsed.Title) {
				continue
			}
			retry := *parsed
			retry.Title, retry.Year, retry.TmdbID, retry.TmdbKind = d.Title, d.Year, 0, ""
			retry.IsTV = retry.IsTV || d.IsTV
			media, err = recognize(&retry)
			break
		}
	}
	if err != nil {
		return nil, "", err
	}
	if media == nil {
		return nil, fmt.Sprintf("TMDB 没找到「%s」", parsed.Title), nil
	}
	if len(media.Twins) > 0 {
		// 同名几部分不出（凡人修仙传 2020 动画 / 2025 真人剧那种）：猜错了 Emby 里就是另一部片，不猜
		return nil, fmt.Sprintf("「%s」有同名的几部，分不出是哪一部，请手动指定", media.Title), nil
	}
	return media, "", nil
}

// planShareOrganize 已经过滤好的文件（planShareFiles 的结果）→ 按识别结果算出的本地位置。
// 算不出位置的文件（模板对它给不出名字）不建
func planShareOrganize(plans []shareFilePlan, media *TmdbMedia, category string, rules []ReplaceRule) ([]shareFilePlan, string, error) {
	byFid := map[string]shareFilePlan{}
	files := make([]orgRecordFile, 0, len(plans))
	var top []string
	for _, p := range plans {
		byFid[p.Entry.ID] = p
		kind := "video"
		if p.Kind == "asset" {
			kind = "meta"
			if classifyFile(p.Entry.Name) == FileTypeSubtitle {
				kind = "subtitle"
			}
		}
		files = append(files, orgRecordFile{Fid: p.Entry.ID, Name: p.Entry.Name, Dir: p.Entry.Dir, Kind: kind, Size: p.Entry.Size, Sha1: p.Entry.Sha1})
		if seg, _, _ := strings.Cut(p.Entry.Dir, "/"); seg != "" {
			top = append(top, seg)
		}
	}
	// 条目名：所有文件都在同一个顶层目录下时用它（「某剧.S02/」这种写在目录上的季号能用上）
	srcName := ""
	if len(top) == len(plans) && len(top) > 0 {
		srcName = top[0] + "/"
		for _, t := range top {
			if t != top[0] {
				srcName = ""
				break
			}
		}
	}
	layout, err := planRedoLayoutOpt(media, category, files, srcName, rules, nil, redoPlanOpts{})
	var dup *redoDupError
	if errors.As(err, &dup) {
		// 同一集几份（分享里粤语 / 国语各一份）：都留，按 #A #B 区分（orgdup.go 的写法），不替用户挑
		choice := map[string]string{}
		for _, g := range dup.groups {
			for i, f := range g.Files {
				if i < 26 {
					choice[f.Fid] = string(rune('A' + i))
				}
			}
		}
		layout, err = planRedoLayoutOpt(media, category, files, srcName, rules, nil, redoPlanOpts{choice: choice})
	}
	if err != nil {
		return nil, "", err
	}
	var out []shareFilePlan
	put := func(dir string, f orgRecordFile) {
		p, ok := byFid[f.Fid]
		if !ok {
			return
		}
		name := sanitizeName(f.Name)
		if p.Kind == "video" {
			name = strmNameOf(name)
		}
		rel, ok := shareRelUnder(dir, name)
		if !ok {
			return
		}
		p.Rel = rel
		out = append(out, p)
	}
	for dir, fs := range layout.groups {
		for _, f := range fs {
			put(dir, f)
		}
	}
	for _, f := range layout.metaFiles {
		put(layout.rootRel, f)
	}
	return out, layout.rootRel, nil
}

// organizePlans 本地整理：认片（有编号按编号取）→ 分类 → 算位置，并把认出的条目记在分享上。
// 认不出返回原来的计划（按原样放）；err 只在 TMDB 暂时连不上时返回 —— 这时什么都不动，下次再来
func (r *shareStrmRun) organizePlans(entries []shareEntry, title string, plans []shareFilePlan) ([]shareFilePlan, error) {
	src := r.src
	var media *TmdbMedia
	note := ""
	if src.TmdbID > 0 {
		m, err := r.org.byID(src.TmdbID, src.MediaType == "tv")
		if err != nil {
			return nil, fmt.Errorf("取 TMDB 条目失败：%w", err)
		}
		if m == nil {
			note = fmt.Sprintf("TMDB 上没有编号 %d 的%s", src.TmdbID, map[bool]string{true: "剧集", false: "电影"}[src.MediaType == "tv"])
		}
		media = m
	} else {
		m, n, err := recognizeShareEntries(entries, title, r.org.rules, r.org.recognize)
		if err != nil {
			return nil, fmt.Errorf("TMDB 暂时连不上：%w", err)
		}
		note = n
		if m != nil {
			// 搜索结果不带分类要看的详情（标签、演职员、制片国家），按编号再取一次
			if d, err := r.org.byID(m.TmdbID, m.MediaType == "tv"); err == nil && d != nil {
				m = d
			}
		}
		media = m
	}
	if media == nil {
		r.recordRecog(nil, "", note)
		plogShareStrm.Printf("○ %s：%s，按原样放在「%s」", firstNonEmpty(src.Title, src.ShareCode), note, src.LocalDir)
		return plans, nil
	}
	category := r.org.classify(media)
	org, titleDir, err := planShareOrganize(plans, media, category, r.org.rules)
	if err != nil {
		note = "按命名模板算位置失败：" + err.Error()
		r.recordRecog(media, "", note)
		plogShareStrm.Printf("○ %s：%s，按原样放", firstNonEmpty(src.Title, src.ShareCode), note)
		return plans, nil
	}
	r.recordRecog(media, categoryDir(media.MediaType, category), "")
	r.media, r.titleDir = media, titleDir
	r.db.Model(&model.ShareSource{}).Where("id = ?", r.src.ID).Update("title_dir", titleDir)
	return org, nil
}

// recordRecog 认出的条目记在分享上（页面上显示「认成了哪部」）。手动指定的编号不改
func (r *shareStrmRun) recordRecog(media *TmdbMedia, category, note string) {
	upd := map[string]any{"recog_note": truncateStr(note, 290), "category": truncateStr(category, 190)}
	if media != nil {
		upd["tmdb_id"], upd["media_type"] = media.TmdbID, media.MediaType
		label := media.Title
		if media.Year != "" {
			label += " (" + media.Year + ")"
		}
		upd["media_title"] = truncateStr(label, 290)
		r.src.TmdbID, r.src.MediaType = media.TmdbID, media.MediaType
	} else if !r.src.Manual {
		upd["media_title"] = ""
	}
	r.db.Model(&model.ShareSource{}).Where("id = ?", r.src.ID).Updates(upd)
}

// shareCategoryOf 本地整理的分类目录：选了分类方案按方案分，没选只分电影 / 剧集
func shareCategoryOf(scheme uint) func(*TmdbMedia) string {
	return func(m *TmdbMedia) string {
		if scheme == 0 {
			return ""
		}
		c, _ := classifyTargetIn(scheme, m)
		return c
	}
}
