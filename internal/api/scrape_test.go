package api

import (
	"reflect"
	"testing"

	"115-station/internal/model"
)

// 影片 NFO 必须与视频同名（Emby 自己刮削出来就是这个名字），
// 固定名 movie.nfo 只在台账查不到视频行时兜底
func TestMovieNFONames(t *testing.T) {
	cases := []struct {
		name string
		rows []model.SyncedFile
		want []string
	}{
		{
			name: "保留扩展名的 strm",
			rows: []model.SyncedFile{{RelPath: "影视/电影/海洋奇缘.2026/海洋奇缘.Moana.2026.2160p.mkv.strm"}},
			want: []string{"海洋奇缘.Moana.2026.2160p.mkv.nfo"},
		},
		{
			name: "不保留扩展名的 strm",
			rows: []model.SyncedFile{{RelPath: "影视/电影/某片.2020/某片.2020.1080p.strm"}},
			want: []string{"某片.2020.1080p.nfo"},
		},
		{
			name: "同一片目两个版本各写各的",
			rows: []model.SyncedFile{
				{RelPath: "影视/电影/某片.2020/某片.2020.2160p.mkv.strm"},
				{RelPath: "影视/电影/某片.2020/某片.2020.1080p.mkv.strm"},
			},
			want: []string{"某片.2020.2160p.mkv.nfo", "某片.2020.1080p.mkv.nfo"},
		},
		{
			name: "台账里没有视频行 → 兜底固定名",
			rows: nil,
			want: []string{"movie.nfo"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := movieNFONames(c.rows); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("movieNFONames = %v, want %v", got, c.want)
			}
		})
	}
}

// 兜底回传引擎此前只认三个固定名，与视频同名的影片 NFO 和逐集 NFO 一个都传不上去
func TestIsMetadataUploadFile(t *testing.T) {
	yes := []string{
		"poster.jpg", "fanart.jpg", "Banner.jpg",
		"movie.nfo", "tvshow.nfo", "season.nfo",
		"海洋奇缘.Moana.2026.2160p.mkv.nfo", "某剧.S01E01.1080p.mkv.NFO",
	}
	for _, n := range yes {
		if !isMetadataUploadFile(n) {
			t.Errorf("%s 应该回传", n)
		}
	}
	no := []string{"某片.mkv.strm", "随手截图.jpg", "logo.png", "说明.txt"}
	for _, n := range no {
		if isMetadataUploadFile(n) {
			t.Errorf("%s 不该回传", n)
		}
	}
}
