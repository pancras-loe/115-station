package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// ==================== 工作区目录：互斥校验 + 一键创建 ====================
//
// 五个目录分存在三个 setting 里：full.cid（媒体库）、share.folder（转存）、
// org-basic.pending/existing/redundant（待整理/已存在/冗余）。
// 它们之间只要有一对互相包含，后果都落在网盘内容上：
// 工作区在媒体库里 → 增量把待整理素材当库内变更整棵回退遍历（2026-09-23 现场：
// 一轮列 296 次目录）；媒体库在工作区里 → 整理把库内内容当素材搬走。
// 运行时各处虽有排除/熔断兜底，但错配本身应该在保存那一刻就挡住。

// wsSlot 一个工作区目录槽位
type wsSlot struct {
	key   string // library / share / pending / existing / redundant
	label string
	cid   string
}

// wsRootName 一键创建的顶层目录，建在网盘根下
const wsRootName = "StrmStation"

// wsSubDirs 一键创建时每个槽位用的子目录名（媒体库不在此列，它必须由用户自己选）
var wsSubDirs = map[string]string{
	"share":     "转存",
	"pending":   "待整理",
	"existing":  "已存在",
	"redundant": "冗余",
}

// wsSettingOf 槽位存在哪个 setting 的哪个字段
var wsSettingOf = map[string][2]string{
	"library":   {"full", "cid"},
	"share":     {"share", "folder"},
	"pending":   {"org-basic", "pending"},
	"existing":  {"org-basic", "existing"},
	"redundant": {"org-basic", "redundant"},
}

// wsSlotOrder 固定顺序，报错文案与测试都依赖它稳定
var wsSlotOrder = []struct{ key, label string }{
	{"library", "115 媒体库目录"},
	{"share", "转存目录"},
	{"pending", "待整理目录"},
	{"existing", "已存在目录"},
	{"redundant", "冗余目录"},
}

// jsonStrField 从一段 setting JSON 里取字符串字段；解析不了按空处理
func jsonStrField(raw, field string) string {
	var m map[string]interface{}
	if raw == "" || json.Unmarshal([]byte(raw), &m) != nil {
		return ""
	}
	s, _ := m[field].(string)
	return strings.TrimSpace(s)
}

// workspaceSlots 按 setting 原文拼出五个槽位。getter 是「key → JSON 原文」
func workspaceSlots(getter func(key string) string) []wsSlot {
	out := make([]wsSlot, 0, len(wsSlotOrder))
	for _, s := range wsSlotOrder {
		loc := wsSettingOf[s.key]
		out = append(out, wsSlot{key: s.key, label: s.label, cid: jsonStrField(getter(loc[0]), loc[1])})
	}
	return out
}

// wsContains a 是否等于 b 或位于 b 之内（绝对路径比较，"/" 是网盘根）
func wsContains(b, a string) bool {
	a, b = strings.TrimSuffix(a, "/"), strings.TrimSuffix(b, "/")
	if b == "" {
		return true // 网盘根包含一切
	}
	return a == b || strings.HasPrefix(a+"/", b+"/")
}

// checkWorkspaceSlots 校验「本次改动过的槽位」与其余槽位互不包含。
//
// 查询数压到最少：
//   - 没有槽位改动 → 零请求（org-basic 在「媒体补全」页签也会整存，不能每次都查）；
//   - cid 相同直接判冲突，不查接口；
//   - 改动过的槽位强制重查（刚选的目录，缓存里多半没有，有也不该信）；
//     没改动的走 PathCache，命中就不打接口。一次请求拿整条祖先链，
//     所以每个目录最多一次请求，五个槽位最多五次。
//   - 只比较「至少一方改动过」的那些对：两个没动过的旧槽位之间即使有问题，
//     也不该拦住这次无关的保存（它们会在用户改其中之一时被拦下）。
//
// resolve(cid, fresh) 返回网盘绝对路径。没改动的槽位解析失败时跳过它
// （旧目录可能已被删，不该因此存不了别的目录）；改动过的解析失败则拒绝保存。
func checkWorkspaceSlots(slots []wsSlot, changed map[string]bool, resolve func(cid string, fresh bool) (string, error)) error {
	var set []wsSlot
	anyChanged := false
	for _, s := range slots {
		if s.cid == "" {
			continue
		}
		set = append(set, s)
		if changed[s.key] {
			anyChanged = true
		}
	}
	if !anyChanged {
		return nil
	}
	involved := func(a, b wsSlot) bool { return changed[a.key] || changed[b.key] }

	for i := range set {
		for j := i + 1; j < len(set); j++ {
			if involved(set[i], set[j]) && set[i].cid == set[j].cid {
				return fmt.Errorf("%s 与 %s 选的是同一个目录，这几个目录必须各自独立、互不包含", set[i].label, set[j].label)
			}
		}
	}

	abs := map[string]string{}
	for _, s := range set {
		if s.cid == "0" {
			abs[s.key] = "/"
			continue
		}
		p, err := resolve(s.cid, changed[s.key])
		if err != nil {
			if !changed[s.key] {
				log.Printf("[目录校验] ○ %s（cid=%s）解析不出路径，本次跳过它: %v", s.label, s.cid, err)
				continue
			}
			if errors.Is(err, errDirGone) {
				return fmt.Errorf("%s 指向的目录已不存在（可能已删除或在回收站），请重新选择", s.label)
			}
			return fmt.Errorf("校验 %s 的位置失败：%v（请稍后重试）", s.label, err)
		}
		abs[s.key] = p
	}

	for i := range set {
		for j := range set {
			a, b := set[i], set[j]
			if i == j || !involved(a, b) {
				continue
			}
			pa, oka := abs[a.key]
			pb, okb := abs[b.key]
			if !oka || !okb {
				continue
			}
			if wsContains(pb, pa) {
				return fmt.Errorf("%s（%s）位于 %s（%s）之内，这几个目录必须互不包含", a.label, pa, b.label, pb)
			}
		}
	}
	return nil
}

// guardWorkspaceSetting SaveSetting 的前置校验：只管三个装着工作区目录的 key
func (h *Handler) guardWorkspaceSetting(key, value string) error {
	if key != "full" && key != "share" && key != "org-basic" {
		return nil
	}
	stored := workspaceSlots(h.getSettingValue)
	next := workspaceSlots(func(k string) string {
		if k == key {
			return value
		}
		return h.getSettingValue(k)
	})
	changed := map[string]bool{}
	for i := range next {
		if next[i].cid != stored[i].cid {
			changed[next[i].key] = true
		}
	}
	if len(changed) == 0 {
		return nil
	}
	cookie, err := h.get115Cookie()
	if err != nil || cookie == "" {
		// 祖先链只有 Cookie 通道能查；没 Cookie 时同样查不了的还有增量与整理的守卫，
		// 这里放行并留痕，不因为校验不了就不让配置
		log.Printf("[目录校验] ○ 没有可用的 115 Cookie，跳过目录互斥校验（%s）", key)
		return nil
	}
	return checkWorkspaceSlots(next, changed, func(cid string, fresh bool) (string, error) {
		if fresh {
			return resolveDirAbsFresh(cookie, cid)
		}
		return resolveDirAbs(cookie, cid)
	})
}

// mergeSettingFields 把若干字段合并写进一个 setting 的 JSON，其余字段原样保留
func (h *Handler) mergeSettingFields(key string, fields map[string]string) error {
	m := map[string]interface{}{}
	if raw := h.settingValueRaw(key); raw != "" {
		_ = json.Unmarshal([]byte(raw), &m)
		if m == nil {
			m = map[string]interface{}{}
		}
	}
	for k, v := range fields {
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return h.Config.SaveSetting(key, string(b))
}

// InitWorkspaceDirs 一键创建工作目录：网盘根下 /StrmStation/{转存,待整理,已存在,冗余}，
// 只补还没配置的槽位，已配置的一个都不动。同名目录已存在就直接复用（ensurePath 先查后建）。
// POST /organize/workspace/init
func (h *Handler) InitWorkspaceDirs(c *gin.Context) {
	slots := workspaceSlots(h.getSettingValue)
	lib := slots[0]
	if lib.cid == "" || lib.cid == "0" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请先选择并保存 115 媒体库目录"})
		return
	}
	var missing []wsSlot
	for _, s := range slots[1:] {
		if s.cid == "" {
			missing = append(missing, s)
		}
	}
	if len(missing) == 0 {
		c.JSON(http.StatusOK, gin.H{"message": "工作目录都已配置，无需创建", "created": []gin.H{}})
		return
	}
	ops, err := h.newPan115Ops()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "115 账号不可用: " + err.Error()})
		return
	}
	cookie, _ := h.get115Cookie()

	// 先核媒体库不在 /StrmStation 里，再动网盘：建完才发现冲突就白建了
	rootAbs := "/" + wsRootName
	if cookie != "" {
		libAbs, err := resolveDirAbsFresh(cookie, lib.cid)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "解析媒体库目录失败: " + err.Error()})
			return
		}
		if wsContains(rootAbs, libAbs) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("媒体库目录（%s）就在 %s 里，一键创建会与它重叠，请手动配置工作目录", libAbs, rootAbs)})
			return
		}
	}

	rootCid, err := ops.ensurePath("0", wsRootName)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "创建 " + rootAbs + " 失败: " + err.Error()})
		return
	}
	created := map[string]string{} // slot key → cid
	paths := map[string]string{}   // slot key → 绝对路径
	for _, s := range missing {
		name := wsSubDirs[s.key]
		cid, err := ops.ensurePath(rootCid, name)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("创建 %s/%s 失败: %v", rootAbs, name, err)})
			return
		}
		created[s.key] = cid
		paths[s.key] = rootAbs + "/" + name
	}

	// 已配置的旧槽位里要是有一个正好是 /StrmStation 或它的上层，新目录就落进去了。
	// 新目录的路径已知，不再为它们打接口
	next := make([]wsSlot, len(slots))
	copy(next, slots)
	changed := map[string]bool{}
	for i := range next {
		if cid, ok := created[next[i].key]; ok {
			next[i].cid = cid
			changed[next[i].key] = true
		}
	}
	if cookie != "" {
		byCid := map[string]string{}
		for k, cid := range created {
			byCid[cid] = paths[k]
		}
		err := checkWorkspaceSlots(next, changed, func(cid string, fresh bool) (string, error) {
			if p, ok := byCid[cid]; ok {
				return p, nil
			}
			return resolveDirAbs(cookie, cid)
		})
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "目录已建好但未写入配置：" + err.Error()})
			return
		}
	}

	orgFields := map[string]string{}
	for _, k := range []string{"pending", "existing", "redundant"} {
		if cid, ok := created[k]; ok {
			orgFields[k] = cid
			orgFields[k+"_path"] = paths[k]
		}
	}
	if len(orgFields) > 0 {
		if err := h.mergeSettingFields("org-basic", orgFields); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "保存整理目录配置失败: " + err.Error()})
			return
		}
	}
	if cid, ok := created["share"]; ok {
		if err := h.mergeSettingFields("share", map[string]string{"folder": cid, "folder_path": paths["share"]}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "保存转存目录配置失败: " + err.Error()})
			return
		}
	}

	out := make([]gin.H, 0, len(missing))
	for _, s := range missing {
		out = append(out, gin.H{"key": s.key, "label": s.label, "cid": created[s.key], "path": paths[s.key]})
		log.Printf("[目录] ✓ 已创建并配置%s: %s", s.label, paths[s.key])
	}
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("已在 %s 下创建 %d 个工作目录", rootAbs, len(out)), "created": out})
}
