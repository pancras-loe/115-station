package api

import (
	"errors"
	"strings"
	"testing"
)

// /share/snap 的几种返回，字段取自 2026-10-31 对真分享的实测（sharestrm_live_test.go）
func TestParseShareSnap(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		items   int
		title   string
		gone    bool
		errHas  string
		wantErr bool
	}{
		{
			name:  "正常分享",
			body:  `{"state":true,"data":{"count":1,"list":[{"cid":"1","n":"S01"}],"shareinfo":{"share_title":"剧","share_state":1,"forbid_reason":"","expire_time":-1}}}`,
			items: 1, title: "剧",
		},
		{
			name:  "share_state 是字符串 1",
			body:  `{"state":true,"data":{"list":[],"shareinfo":{"share_title":"剧","share_state":"1"}}}`,
			title: "剧",
		},
		{
			name:  "没有 share_state 字段按正常",
			body:  `{"state":true,"data":{"list":[{"fid":"2","n":"a.mkv","sha":"X"}],"shareinfo":{"share_title":"片"}}}`,
			items: 1, title: "片",
		},
		{
			// 实测：过期分享 state=true、文件照样列得出来
			name: "已过期",
			body: `{"state":true,"errno":0,"data":{"count":1,"list":[{"cid":"3","n":"凡人"}],"shareinfo":{"share_title":"凡人修仙传","share_state":7,"forbid_reason":"链接已过期","expire_time":1788093509}}}`,
			gone: true, errHas: "分享已过期", wantErr: true,
		},
		{
			name: "已取消",
			body: `{"state":true,"data":{"list":[{"cid":"3","n":"x"}],"shareinfo":{"share_title":"x","share_state":4,"forbid_reason":""}}}`,
			gone: true, errHas: "分享已取消", wantErr: true,
		},
		{
			name: "没见过的状态",
			body: `{"state":true,"data":{"list":[],"shareinfo":{"share_state":9,"forbid_reason":"分享已被屏蔽"}}}`,
			gone: true, errHas: "状态 9", wantErr: true,
		},
		{
			name: "提取码错",
			body: `{"state":false,"errno":4100008,"error":"访问码错误","data":{"count":0}}`,
			errHas: "访问码错误", wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			list, title, err := parseShareSnap([]byte(c.body))
			if c.wantErr {
				if err == nil {
					t.Fatalf("应报错，得到 %d 条", len(list))
				}
				if !strings.Contains(err.Error(), c.errHas) {
					t.Fatalf("错误 %q 里应有 %q", err, c.errHas)
				}
				if errors.Is(err, errShareGone) != c.gone {
					t.Fatalf("errShareGone 判定 = %v，应为 %v（%v）", !c.gone, c.gone, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(list) != c.items || title != c.title {
				t.Fatalf("得到 %d 条 / %q，应为 %d 条 / %q", len(list), title, c.items, c.title)
			}
		})
	}
}

// 失效分享在 shareWalk 里第一层就报错，订阅据此不再重试
func TestShareWalkGone(t *testing.T) {
	_, _, _, err := shareWalkWith(func(cid string) ([]shareEntry, string, error) {
		_, _, err := parseShareSnap([]byte(`{"state":true,"data":{"list":[{"cid":"3","n":"x"}],"shareinfo":{"share_state":7,"forbid_reason":"链接已过期"}}}`))
		return nil, "", err
	}, 10)
	if !errors.Is(err, errShareGone) {
		t.Fatalf("应是 errShareGone，得到 %v", err)
	}
}
