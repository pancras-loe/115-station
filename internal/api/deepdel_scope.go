package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"115-station/internal/model"
)

// 参考 qmediasync internal/controllers/emby.go 的媒体类型白名单。
// Folder 也会在移除媒体库时发出，不能据此把整个目录展开成源文件删除。
func deepDelMediaType(payload map[string]interface{}) (string, error) {
	item, _ := payload["Item"].(map[string]interface{})
	kind, _ := item["Type"].(string)
	switch kind {
	case "Movie", "Episode", "Series", "Season":
		return kind, nil
	default:
		return "", fmt.Errorf("事件类型 %q 不是可联动删除的影视条目", kind)
	}
}

var deepDelSeasonDir = regexp.MustCompile(`(?i)^(season[ ._-]*|s)\d+$`)

// deepDelDriveLetter 只有盘符开头的绝对路径才算越界。
// 冒号本身是合法文件名字符，“美国队长.Captain America: The First Avenger…”
// 这种带冒号的片名就是（洗版验证里它被误判成路径越界拦下了）
var deepDelDriveLetter = regexp.MustCompile(`^[A-Za-z]:`)

// 目录前缀仅对已确认的剧/季生效。没有标题或季布局证据时宁可拒绝，
// 不能因为载荷写了 Series 就把分类目录当成整剧。
func (h *Handler) checkDeepDelScope(kind string, rels []string) error {
	for _, rel := range rels {
		if path.Clean(rel) != rel || strings.Contains(rel, "\\") || deepDelDriveLetter.MatchString(rel) || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "../") || !strings.Contains(rel, "/") {
			return fmt.Errorf("事件路径越界或指向根: %s", rel)
		}
		if strings.EqualFold(path.Ext(rel), ".strm") {
			continue
		}
		if kind != "Series" && kind != "Season" {
			return fmt.Errorf("%s 事件必须精确定位 STRM 文件: %s", kind, rel)
		}
		series := rel
		if kind == "Season" {
			if !deepDelSeasonDir.MatchString(path.Base(rel)) {
				return fmt.Errorf("无法确认季目录: %s", rel)
			}
			series = path.Dir(rel)
		}
		var rows []model.SyncedFile
		if err := h.DB.Where(`rel_path LIKE ? ESCAPE '\'`, likeEscape(series)+"/%").Find(&rows).Error; err != nil {
			return err
		}
		// 前缀下台账一行都没有：这个前缀什么也展开不出来，不存在扩大删除的风险。
		// 常见于神医 deep.delete 与原生 library.deleted 成对到达，前者已删完并清了台账，
		// 后者再来时若当成「缺证据」拦下，就会对一次成功的删除报一条误导性的拦截通知。
		// 放行后由 deepDelEventRows 以「未命中同步台账」静默跳过
		if len(rows) == 0 {
			continue
		}
		_, _, tmdb := parseTitleDir(path.Base(series))
		evidence, videos := tmdb > 0, 0
		for _, row := range rows {
			if row.RelPath == series+"/tvshow.nfo" {
				evidence = true
			}
			if row.Kind != "video" {
				continue
			}
			videos++
			sub := strings.TrimPrefix(row.RelPath, series+"/")
			parts := strings.Split(sub, "/")
			if len(parts) > 2 || (len(parts) == 2 && !deepDelSeasonDir.MatchString(parts[0])) {
				return fmt.Errorf("目录包含非剧/季布局，拒绝扩大删除范围: %s", rel)
			}
			if len(parts) == 2 {
				evidence = true
			}
		}
		if !evidence || videos == 0 {
			return fmt.Errorf("台账缺少剧/季目录证据: %s", rel)
		}
	}
	return nil
}

// 执行前读取当前真实库目录；移除库之后到达的子项事件同样必须被拦住。
// 不使用缓存或在查询失败时降级到路径猜测，否则旧库边界会继续授权删除。
func (h *Handler) checkDeepDelLibraries(rels []string) error {
	var cfg embyRefreshCfg
	if err := json.Unmarshal([]byte(h.getSettingValue("emby")), &cfg); err != nil || strings.TrimSpace(cfg.ServerURL) == "" {
		return fmt.Errorf("无法核验 Emby 媒体库，请配置 Emby 地址与 API 密钥")
	}
	resp, err := embyRequest(http.MethodGet, strings.TrimRight(cfg.ServerURL, "/"), cfg.APIKey, "/Library/VirtualFolders", nil, nil)
	if err != nil {
		return fmt.Errorf("无法核验 Emby 媒体库连接")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("无法核验 Emby 媒体库: HTTP %d", resp.StatusCode)
	}
	var libs []struct {
		Locations []string `json:"Locations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&libs); err != nil {
		return fmt.Errorf("Emby 媒体库响应无法解析")
	}
	var roots []string
	for _, lib := range libs {
		for _, location := range lib.Locations {
			if strings.TrimSpace(location) == "" {
				continue
			}
			roots = append(roots, filepath.Clean(h.mapFromEmbyPath(location)))
		}
	}
	for _, rel := range rels {
		full := filepath.Join(h.orphanLocalRoot(), filepath.FromSlash(rel))
		matched := false
		for _, root := range roots {
			// 同时配置父库和子库时，不能通过父库授权删除整个子库。
			ancestor, e := filepath.Rel(full, root)
			if e == nil && (ancestor == "." || deepDelBelow(ancestor)) {
				return fmt.Errorf("事件路径指向媒体库根或其祖先: %s", rel)
			}
			sub, e := filepath.Rel(root, full)
			if e != nil || !deepDelBelow(sub) {
				continue
			}
			if _, e := os.ReadDir(root); e != nil {
				return fmt.Errorf("Emby 媒体库目录不可访问: %s", root)
			}
			matched = true
		}
		if !matched {
			return fmt.Errorf("事件路径不属于当前 Emby 媒体库（可能已移除）: %s", rel)
		}
	}
	return nil
}

func deepDelBelow(rel string) bool {
	return rel != "." && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
