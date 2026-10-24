package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// ==================== 转存到哪个媒体库（2026-10-20，MULTI-ACCOUNT-V2-PLAN.md §10.2，V2 阶段 5） ====================
//
// 媒体库表（V2）里一个库一个转存/待整理文件夹，转存（找资源、链接转存、离线、机器人、订阅）就是「放进哪个库的转存文件夹」：
//   - 只有一个要整理的库：直接放进它；
//   - 多个：用设置里的「默认转存库」（setting transfer_target）；没设 → 每次都问（网页弹选择、提示可以设默认；
//     机器人回一句去设默认），后端报 errXferNeedLib。
//
// 提交时选的库沿用原来「转存到」那个字段（resSubmitReq.Subdir / Subscription.Subdir），值写成 lib:<库 id>，
// 前端、订阅表都不用加字段；老的转存子目录 cid 在 V2 下认不出，按没选处理。
//
// 别的账号的库：转存目标写成 a{账号}:{cid}（acctLibKey），分享转存与离线提交按它换成那个账号的 Cookie。
// 订阅只能进主号的库（包装目录、等秒传、删离线任务、查配额都还只认主号，见 subTargetLib）。

const xferTargetKey = "transfer_target"

// errXferNeedLib 多个库、没设默认、这次也没选
var errXferNeedLib = errors.New("有多个媒体库，没设默认转存库：选一个转存到哪个库（可以在「发现与订阅 → 设置 → 转存目录」设默认，以后就不用每次选）")

// xferLibValue 「转存到」里一个库的值
func xferLibValue(id uint) string { return fmt.Sprintf("lib:%d", id) }

// parseXferLibValue lib:<id> → id；不是这种写法返回 0
func parseXferLibValue(v string) uint {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "lib:") {
		return 0
	}
	n, err := strconv.ParseUint(v[4:], 10, 64)
	if err != nil {
		return 0
	}
	return uint(n)
}

// xferDefaultLibID 设置里的默认转存库（0 = 没设）
func (h *Handler) xferDefaultLibID() uint {
	var v struct {
		Lib uint `json:"lib"`
	}
	_ = json.Unmarshal([]byte(h.getSettingValue(xferTargetKey)), &v)
	return v.Lib
}

// xferLib 这次转存放进哪个库。pick 是用户这次选的（lib:<id>，空 = 没选）
func (h *Handler) xferLib(pick string, libs []model.Library) (model.Library, error) {
	org := organizeLibs(libs)
	if len(org) == 0 {
		return model.Library{}, errors.New("还没有库设转存/待整理文件夹：到「账号与媒体库 → 媒体库」给一个库设上")
	}
	if id := parseXferLibValue(pick); id != 0 {
		for _, l := range org {
			if l.ID == id {
				return l, nil
			}
		}
		return model.Library{}, errors.New("选的媒体库不在了（或已不再整理），刷新后重选")
	}
	if len(org) == 1 {
		return org[0], nil
	}
	if id := h.xferDefaultLibID(); id != 0 {
		for _, l := range org {
			if l.ID == id {
				return l, nil
			}
		}
	}
	return model.Library{}, errXferNeedLib
}

// xferTargetOf 一个库的转存目标：主号是转存文件夹 cid，别的账号是 a{账号}:{cid}
func xferTargetOf(l model.Library) string {
	if l.Account != acctMain {
		return acctLibKey(l.Account, l.StagingCid)
	}
	return l.StagingCid
}

// splitXferTarget 转存目标 → 账号与目录 cid（不是 a{账号}:{cid} 的就是主号）
func splitXferTarget(t string) (uint, string) {
	if acct, cid, ok := splitAcctLibKey(t); ok {
		return acct, cid
	}
	return acctMain, t
}

// defaultXferTarget 核心函数（分享转存、离线提交）没拿到目标时用：V2 按默认库，老口径是共用的转存目录
func (h *Handler) defaultXferTarget() (string, error) {
	if libs := loadLibraries(); len(libs) > 0 {
		l, err := h.xferLib("", libs)
		if err != nil {
			return "", err
		}
		return xferTargetOf(l), nil
	}
	return h.shareFolderCid(), nil
}

// subTargetLib 订阅转存进哪个库（V2）：订阅自己选的 → 订阅设置里的 → 默认转存库 → 主号第一个要整理的库。
// 只能是主号的库：订阅的包装目录、等秒传、删离线任务、查配额都还只走主号
func (h *Handler) subTargetLib(pick, cfgPick string, libs []model.Library) (model.Library, error) {
	for _, p := range []string{pick, cfgPick} {
		if id := parseXferLibValue(p); id != 0 {
			for _, l := range organizeLibs(libs) {
				if l.ID == id && l.Account == acctMain {
					return l, nil
				}
			}
		}
	}
	if id := h.xferDefaultLibID(); id != 0 {
		for _, l := range organizeLibs(libs) {
			if l.ID == id && l.Account == acctMain {
				return l, nil
			}
		}
	}
	for _, l := range organizeLibs(libs) {
		if l.Account == acctMain {
			return l, nil
		}
	}
	return model.Library{}, errors.New("主账号还没有库设转存/待整理文件夹，订阅没地方放：到「账号与媒体库 → 媒体库」设一个")
}

// ---- 接口 ----

// xferLibView 「转存到」下拉里的一项
type xferLibView struct {
	Value       string `json:"value"` // lib:<id>
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	Account     uint   `json:"account"`
	AccountName string `json:"account_name,omitempty"`
	StagingPath string `json:"staging_path"`
}

// xferTargetsNow 能转存进的库与默认
func (h *Handler) xferTargetsNow() gin.H {
	libs := loadLibraries()
	acctName := map[uint]string{}
	for _, a := range accountOptions() {
		acctName[a.ID] = a.Name
	}
	out := []xferLibView{}
	multi := false
	for _, l := range organizeLibs(libs) {
		if l.Account != acctMain {
			multi = true
		}
		out = append(out, xferLibView{Value: xferLibValue(l.ID), ID: l.ID, Name: l.Name, Account: l.Account,
			AccountName: acctName[l.Account], StagingPath: firstNonEmpty(l.StagingPath, "cid "+l.StagingCid)})
	}
	def := h.xferDefaultLibID()
	valid := false
	for _, v := range out {
		if v.ID == def {
			valid = true
		}
	}
	if !valid {
		def = 0
	}
	return gin.H{"v2": len(libs) > 0, "libs": out, "default": def, "multi_account": multi,
		"need_pick": len(out) > 1 && def == 0}
}

// GetXferTargets GET /transfer/targets
func (h *Handler) GetXferTargets(c *gin.Context) {
	c.JSON(http.StatusOK, h.xferTargetsNow())
}

// SaveXferDefault PUT /transfer/targets/default {lib} —— 0 = 不设（多个库时每次都问）
func (h *Handler) SaveXferDefault(c *gin.Context) {
	var req struct {
		Lib uint `json:"lib"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if req.Lib != 0 {
		ok := false
		for _, l := range organizeLibs(loadLibraries()) {
			if l.ID == req.Lib {
				ok = true
			}
		}
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "这个库没设转存/待整理文件夹，不能当默认转存库"})
			return
		}
	}
	b, _ := json.Marshal(map[string]uint{"lib": req.Lib})
	if err := h.Config.SaveSetting(xferTargetKey, string(b)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[转存] ✓ 默认转存库：%s", map[bool]string{true: "不设（每次都问）", false: fmt.Sprintf("库 %d", req.Lib)}[req.Lib == 0])
	c.JSON(http.StatusOK, h.xferTargetsNow())
}
