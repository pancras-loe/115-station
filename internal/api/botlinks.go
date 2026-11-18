package api

import (
	"fmt"
	"regexp"
	"strings"
)

type botDownloadLink struct {
	kind string
	url  string
	code string
}

var reBotDownloadLink = regexp.MustCompile(reBot115ShareURL.String() +
	`|(?i)magnet:\?[^\s"'<>]+|ed2k://\|file\|[^|\r\n]+\|\d+\|[0-9a-f]{32}\|(?:[^|/\s][^|\s]*\|)*/?`)

// botDownloadLinks 从粘贴或转发的消息里按顺序摘链接。普通 HTTP/FTP
// 只在整条消息是一个链接时提交，避免顺手下载转发文案里的详情页。
func botDownloadLinks(text string) []botDownloadLink {
	text = strings.TrimSpace(text)
	matches := reBotDownloadLink.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		kind := classifyLink(text)
		switch {
		case kind == "http" && isAliShareLink(text):
			return []botDownloadLink{{kind: "ali", url: text}}
		case (kind == "http" || kind == "ftp") && len(strings.Fields(text)) == 1:
			return []botDownloadLink{{kind: kind, url: text}}
		}
		return nil
	}

	links := make([]botDownloadLink, 0, len(matches))
	seen := make(map[string]int, len(matches))
	for i, loc := range matches {
		link := botDownloadLink{url: text[loc[0]:loc[1]]}
		link.kind = classifyLink(link.url)
		switch link.kind {
		case "share":
			end := len(text)
			if i+1 < len(matches) {
				end = matches[i+1][0]
			}
			if len(matches) == 1 {
				_, link.code = splitShareLink(text)
			} else {
				link.code = botBatchShareCode(link.url, text[loc[1]:end])
			}
		case "magnet":
			link.url = strings.TrimRight(link.url, ".,;:!?)]}，。；：！？）」』】")
			if !strings.Contains(strings.ToLower(link.url), "xt=") {
				continue
			}
		}
		key := link.kind + ":" + linkHashOf(link.url)
		if at, ok := seen[key]; ok {
			if links[at].code == "" {
				links[at].code = link.code
			}
			continue
		}
		seen[key] = len(links)
		links = append(links, link)
	}
	return links
}

// 批量时，提取码只看本链接及它后面、下一个链接之前的文案。
// 无标签的提取码仅在紧接链接的同一行时采用，避免把下一段的标题当成提取码。
func botBatchShareCode(link, tail string) string {
	if m := reSharePass.FindStringSubmatch(link); m != nil {
		return m[1]
	}
	if m := reBotShareCode.FindStringSubmatch(tail); m != nil {
		return m[1]
	}
	line, _, _ := strings.Cut(strings.TrimLeft(tail, ")]）"), "\n")
	code := strings.Trim(strings.TrimSpace(line), "，,")
	if len(code) >= 3 && len(code) <= 12 && isShareCode(code) {
		return code
	}
	return ""
}

func (h *Handler) botHandleDownload(user, text string, reply func(...string)) {
	links := botDownloadLinks(text)
	if len(links) == 0 {
		reply("✗ 无法识别链接类型，支持 115 分享 / 磁力 / ed2k / HTTP / 阿里云盘分享。")
		return
	}
	h.botDownload(user, links, reply)
}

func (h *Handler) botDownload(user string, links []botDownloadLink, reply func(...string)) {
	action := loadShareStrmCfg().linkAction()
	if action == "ask" {
		var shares, other []botDownloadLink
		for _, link := range links {
			if link.kind == "share" {
				shares = append(shares, link)
			} else {
				other = append(other, link)
			}
		}
		if len(shares) > 0 {
			shareBotRemember(user, shares)
			reply(fmt.Sprintf("收到 %d 条 115 分享，要怎么处理？", len(shares)),
				"回复「转存」— 转存这些分享并整理入库",
				"回复「STRM」— 为这些分享生成 STRM")
			links = other
		}
	}
	h.botRunDownloads(links, action, reply)
}

func (h *Handler) botRunDownloads(links []botDownloadLink, action string, reply func(...string)) {
	if len(links) == 0 {
		return
	}
	batch := len(links) > 1
	if batch {
		reply(fmt.Sprintf("⏳ 已识别 %d 条链接，正在逐条提交…", len(links)))
	} else if links[0].kind == "share" && action != "strm" {
		reply("⏳ 开始转存 115 分享…", extractShareCode(links[0].url))
	}

	// 115 的提交可能较慢，整批放到后台依次执行，让 TG 的指令 worker 继续接收消息。
	go func() {
		var results []string
		for i, link := range links {
			label := fmt.Sprintf("%d. %s", i+1, truncateStr(firstNonEmpty(downloadLinkName(link.url), link.url), 70))
			if link.kind == "share" {
				label = fmt.Sprintf("%d. 115 分享 %s", i+1, extractShareCode(link.url))
			}
			linkReply := reply
			if batch {
				linkReply = func(lines ...string) {
					reply(append([]string{label}, lines...)...)
				}
			}

			var result string
			switch {
			case link.kind == "ali":
				h.botAli2115(link.url, linkReply)
				continue
			case link.kind == "share" && action == "strm":
				h.botShareStrm(link, linkReply)
				continue
			case link.kind == "share":
				result = h.botShareTransfer(link)
			default:
				result = "✓ 已提交离线下载"
				if err := h.submitOfflineLink(link.url, "机器人"); err != nil {
					result = "✗ 提交失败: " + err.Error()
				} else if !batch {
					result += "\n下载完成后自动整理入库并通知。"
				}
			}
			if batch {
				results = append(results, label+"\n"+result)
			} else {
				reply(result)
			}
		}
		if len(results) > 0 {
			reply(append([]string{"链接处理结果"}, results...)...)
		}
	}()
}
