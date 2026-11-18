package api

import (
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"

	"115-station/internal/model"
)

// fullLibraryRef 固定入队时选中的库及其目录映射，避免排队期间改配置后扫到别处。
type fullLibraryRef struct {
	ID      uint   `json:"id"`
	Name    string `json:"name"`
	Account uint   `json:"account"`
	Cid     string `json:"cid"`
	Local   string `json:"local"`
}

func (r fullLibraryRef) matches(l model.Library) bool {
	return r.ID == l.ID && r.Account == l.Account && r.Cid == l.Cid && r.Local == normLibLocal(l.Local)
}

func selectFullLibraries(libs []model.Library, ids []uint) ([]fullLibraryRef, string, error) {
	if len(ids) == 0 {
		return nil, "", fmt.Errorf("至少选择一个媒体库")
	}
	wanted := make(map[uint]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	selected := make([]fullLibraryRef, 0, len(wanted))
	for _, l := range libs {
		if wanted[l.ID] {
			selected = append(selected, fullLibraryRef{l.ID, l.Name, l.Account, l.Cid, normLibLocal(l.Local)})
		}
	}
	if len(selected) != len(wanted) {
		return nil, "", fmt.Errorf("所选媒体库已不存在，请刷新列表后重新选择")
	}

	// 去重范围包含目录映射。改过目录后重新选择，旧任务的重试不能覆盖它。
	scope := slices.Clone(selected)
	for i := range scope {
		scope[i].Name = ""
	}
	slices.SortFunc(scope, func(a, b fullLibraryRef) int { return cmp.Compare(a.ID, b.ID) })
	key, _ := json.Marshal(scope)
	return selected, fmt.Sprintf("%x", sha256.Sum256(key)), nil
}

// fullScopeCoverage 主号游标与附加账号补同步时间各自要求覆盖完整。
// 补同步时间是所有附加账号共用的，所以只覆盖其中一个账号时不能更新时间。
func fullScopeCoverage(libs []model.Library, covered map[uint]fullLibraryRef) (main, attached bool) {
	var mainTotal, mainCovered, attachedTotal, attachedCovered int
	for _, l := range libs {
		r, ok := covered[l.ID]
		match := ok && r.matches(l)
		if l.Account == acctMain {
			mainTotal++
			if match {
				mainCovered++
			}
		} else {
			attachedTotal++
			if match {
				attachedCovered++
			}
		}
	}
	return mainTotal > 0 && mainCovered == mainTotal, attachedTotal > 0 && attachedCovered == attachedTotal
}
