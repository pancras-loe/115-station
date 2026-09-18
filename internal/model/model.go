package model

import (
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Admin struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	Username  string    `json:"username" gorm:"uniqueIndex;size:50;not null"`
	Password  string    `json:"-" gorm:"size:255;not null"`
	CreatedAt time.Time `json:"created_at"`
}

type Storage struct {
	ID         uint    `json:"id" gorm:"primaryKey"`
	Name       string  `json:"name" gorm:"size:100;not null"`
	Type       string  `json:"type" gorm:"size:50;not null"` // 115, openlist, webdav
	Cookie     string  `json:"-" gorm:"type:text"`
	CookiePath string  `json:"cookie_path" gorm:"size:255"` // Cookie 文件路径
	Device     string  `json:"device" gorm:"size:50"`       // 设备类型：ios/android
	Interval   float64 `json:"interval" gorm:"default:3"`   // API 请求间隔（秒）
	Status     string  `json:"status" gorm:"size:20;default:'offline'"`
	FileCount  int64   `json:"file_count" gorm:"default:0"`
	// 115 开放平台（OpenAPI）
	OpenapiEnabled bool      `json:"openapi_enabled" gorm:"default:false"`
	AppID          string    `json:"app_id" gorm:"size:100"`
	AppKey         string    `json:"app_key" gorm:"size:100"`
	AppSecret      string    `json:"-" gorm:"size:100"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type StrmFile struct {
	ID         uint      `json:"id" gorm:"primaryKey"`
	StorageID  uint      `json:"storage_id" gorm:"index"`
	RemotePath string    `json:"remote_path" gorm:"size:500;not null"`
	LocalPath  string    `json:"local_path" gorm:"size:500;not null"`
	StreamURL  string    `json:"stream_url" gorm:"size:500"`
	Status     string    `json:"status" gorm:"size:20;default:'active'"` // active, invalid
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type SyncTask struct {
	ID           uint       `json:"id" gorm:"primaryKey"`
	StorageID    uint       `json:"storage_id" gorm:"index"`
	Name         string     `json:"name" gorm:"size:100;not null"`
	RemotePath   string     `json:"remote_path" gorm:"size:500"`
	LocalPath    string     `json:"local_path" gorm:"size:500"`
	ArchivePath  string     `json:"archive_path" gorm:"size:500"`                   // 归档回写目录
	SyncMode     string     `json:"sync_mode" gorm:"size:20;default:'incremental'"` // full, incremental
	Cron         string     `json:"cron" gorm:"size:50"`
	IsFullSync   bool       `json:"is_full_sync"`
	SyncDelete   bool       `json:"sync_delete" gorm:"default:true"`
	RenameDetect bool       `json:"rename_detect" gorm:"default:true"`
	MetaStrategy string     `json:"meta_strategy" gorm:"size:20;default:'keep'"` // keep, delete, upload
	VideoExt     string     `json:"video_ext" gorm:"size:200;default:'.mp4,.mkv,.ts'"`
	MinVideoSize int64      `json:"min_video_size" gorm:"default:0"`
	ExcludeNames string     `json:"exclude_names" gorm:"size:200"`
	Concurrency  int        `json:"concurrency" gorm:"default:10"`
	Status       string     `json:"status" gorm:"size:20;default:'stopped'"` // running, stopped, completed
	LastSyncAt   *time.Time `json:"last_sync_at"`
	CreatedAt    time.Time  `json:"created_at"`
}

type TmdbConfig struct {
	ID            uint   `json:"id" gorm:"primaryKey"`
	ApiKey        string `json:"api_key" gorm:"size:255"`
	ApiUrl        string `json:"api_url" gorm:"size:255;default:'https://api.themoviedb.org'"`
	ImageApiUrl   string `json:"image_api_url" gorm:"size:255;default:'https://image.tmdb.org'"`
	Language      string `json:"language" gorm:"size:10;default:'zh-CN'"`
	ImageLanguage string `json:"image_language" gorm:"size:10;default:'zh-CN'"`
	EnableProxy   bool   `json:"enable_proxy"`
	ProxyUrl      string `json:"proxy_url" gorm:"size:255"`
}

// Setting 通用键值配置（STRM / 代理 / EMBY 等）
type Setting struct {
	ID    uint   `json:"id" gorm:"primaryKey"`
	Key   string `json:"key" gorm:"uniqueIndex;size:50;not null"`
	Value string `json:"value" gorm:"type:text"` // JSON 配置
}

type ScrapeRule struct {
	ID      uint   `json:"id" gorm:"primaryKey"`
	Type    string `json:"type" gorm:"size:50;not null"` // recognizer, rename_rule, metadata, cleanup, category
	Enabled bool   `json:"enabled" gorm:"default:true"`
	Config  string `json:"config" gorm:"type:text"` // JSON 配置
}

type CategoryRule struct {
	ID         uint   `json:"id" gorm:"primaryKey"`
	MediaType  string `json:"media_type" gorm:"size:20;not null;index"` // movie, tv
	Name       string `json:"name" gorm:"size:50;not null"`             // 华语电影, 动画电影
	Cid        string `json:"cid" gorm:"size:100"`                      // 115 文件夹 CID
	ArchiveDir string `json:"archive_dir" gorm:"size:200"`              // 归档子目录路径

	// 匹配维度（对齐 CMS 的 YAML 策略，逗号分隔多值）
	GenreIds         string `json:"genre_ids" gorm:"size:200"`         // TMDB 类型ID，如 "16,99"
	OriginalLanguage string `json:"original_language" gorm:"size:100"` // 原语言，如 "zh,cn,bo,za"
	OriginCountry    string `json:"origin_country" gorm:"size:100"`    // 原产地，如 "CN,TW,HK"
	Ext              string `json:"ext" gorm:"size:100"`               // 文件后缀，如 "iso"
	CustomRegex      string `json:"custom_regex" gorm:"size:500"`      // 自定义正则（匹配标题或原名）

	IsDefault bool `json:"is_default" gorm:"default:false"` // 是否兜底（未匹配任何分类时归入）
	Priority  int  `json:"priority" gorm:"default:0"`       // 优先级，从小到大，先匹配到先停止
}

// WashRule 洗版策略（对齐 CMS，处理重复资源）
type WashRule struct {
	ID               uint   `json:"id" gorm:"primaryKey"`
	Name             string `json:"name" gorm:"size:50"`                                   // 策略名，如 "电影洗版策略"
	Mode             string `json:"mode" gorm:"size:20;not null"`                          // coexist, skip, replace, max_size, min_size
	MediaType        string `json:"media_type" gorm:"size:20"`                             // movie, tv（空=匹配所有）
	Category         string `json:"category" gorm:"size:100"`                              // 匹配二级分类名，逗号分隔（空=所有）
	PriorityLevel    string `json:"priority_level" gorm:"type:text"`                       // JSON 数组，优先级规则
	OldVersionTarget string `json:"old_version_target" gorm:"size:20;default:'redundant'"` // 旧版去向: redundant/existing/delete
}

// MediaLibrary 已整理的媒体记录（用于去重）
type MediaLibrary struct {
	ID            uint      `json:"id" gorm:"primaryKey"`
	TmdbID        int       `json:"tmdb_id" gorm:"index"`
	Title         string    `json:"title" gorm:"size:255"`
	OriginalTitle string    `json:"original_title" gorm:"size:255"`
	Year          string    `json:"year" gorm:"size:10"`
	MediaType     string    `json:"media_type" gorm:"size:20;index"` // movie, tv
	Category      string    `json:"category" gorm:"size:50;index"`   // 仪表盘/门户按分类聚合高频查询
	TargetPath    string    `json:"target_path" gorm:"size:500"`
	OrigLanguage  string    `json:"original_language" gorm:"size:20"`
	OrigCountry   string    `json:"origin_country" gorm:"size:100"`
	PosterPath    string    `json:"poster_path" gorm:"size:255"`   // TMDB 海报路径（观影门户）
	BackdropPath  string    `json:"backdrop_path" gorm:"size:255"` // TMDB 背景图（门户详情页沉浸头图）
	Genres        string    `json:"genres" gorm:"size:500"`        // TMDB 类型名（逗号分隔，zh-CN）
	VoteAverage   float64   `json:"vote_average"`
	Overview      string    `json:"overview" gorm:"type:text"`
	CreatedAt     time.Time `json:"created_at"`
}

// UploadMark 已上传元数据文件的指纹（监控/兜底两引擎共用）。
// 持久化到 DB：重启后不再重复上传；Emby 重新刮削覆盖文件时
// mtime/size 变化，指纹不匹配即触发重传。
type UploadMark struct {
	ID      uint   `json:"id" gorm:"primaryKey"`
	Path    string `json:"path" gorm:"uniqueIndex;size:500"`
	ModTime int64  `json:"mod_time"` // unix nano
	Size    int64  `json:"size"`
}

// SyncEvent 115 生活事件落库（增量同步两阶段：先落库去重，再应用到本地）
type SyncEvent struct {
	ID        uint       `json:"id" gorm:"primaryKey"`
	EventID   string     `json:"event_id" gorm:"uniqueIndex;size:64;not null"` // 115 事件 id（单调递增，可作游标）
	Type      string     `json:"type" gorm:"size:40"`
	FileID    string     `json:"file_id" gorm:"index;size:64"`
	FileName  string     `json:"file_name" gorm:"size:500"`
	Cid       string     `json:"cid" gorm:"size:64"`
	Size      int64      `json:"size"`
	EventTime int64      `json:"event_time"`                              // unix 秒
	Status    string     `json:"status" gorm:"size:20;default:'pending'"` // pending / applied
	CreatedAt time.Time  `json:"created_at"`
	AppliedAt *time.Time `json:"applied_at"`
}

// SyncedFile 已同步到本地的文件台账（ strm 与附属文件），供 move/delete 事件精确定位
type SyncedFile struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	FileID    string    `json:"file_id" gorm:"uniqueIndex;size:64;not null"` // 115 文件 id
	PickCode  string    `json:"pick_code" gorm:"size:64"`
	Sha1      string    `json:"sha1" gorm:"index;size:40"`               // 文件 sha1（整理去重用）
	RelPath   string    `json:"rel_path" gorm:"size:500;not null;index"` // 相对本地库根的路径（含文件名）
	Kind      string    `json:"kind" gorm:"size:10;index"`               // video / asset
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// OfflinePlay 按需离线（边下边播）登记：ed2k/磁力链接 ↔ 播放端点 id。
// 入库（提交离线）时创建，STRM 占位内容指向 /ed2k/play/{id}；
// Emby 播放时端点查任务状态，没下过就提交 115 离线，完成后定位
// pickcode 存在本表，后续播放走 302 直连快路径
type OfflinePlay struct {
	ID        string    `json:"id" gorm:"primaryKey;size:64"` // 链接指纹（ed2k hash/btih/URL sha1）
	Link      string    `json:"link" gorm:"size:1000"`        // 原始 ed2k/magnet/http 链接
	Name      string    `json:"name" gorm:"size:500"`         // 文件名（ed2k 链接自带，http 取 URL base）
	Size      int64     `json:"size"`                         // 文件字节数（ed2k 链接自带，完成后按尺寸兜底定位）
	PickCode  string    `json:"pick_code" gorm:"size:64"`     // 下载完成并定位到的 115 pickcode（就绪后快速 302）
	Status    string    `json:"status" gorm:"size:20;index"`  // pending / downloading / ready / failed
	ErrorMsg  string    `json:"error_msg" gorm:"size:500"`    // 失败原因（115 拒绝等）
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PortalStat 观影门户播放统计（排行榜数据源）：每部影视一行，
// 周/月/年计数在跨期时自动清零重计（不用明细表，单行 upsert 无膨胀）
type PortalStat struct {
	ID         uint      `json:"id" gorm:"primaryKey"`
	Key        string    `json:"key" gorm:"uniqueIndex;size:500"` // 标题目录 key（台账 4 段路径）
	MediaType  string    `json:"media_type" gorm:"size:20"`
	Title      string    `json:"title" gorm:"size:255"`
	Views      int64     `json:"views"`      // 累计播放
	WeekViews  int64     `json:"week_views"` // 本周播放（ISO 周）
	WeekStart  string    `json:"week_start" gorm:"size:10"`
	MonthViews int64     `json:"month_views"`
	MonthStart string    `json:"month_start" gorm:"size:7"`
	YearViews  int64     `json:"year_views"`
	YearStart  string    `json:"year_start" gorm:"size:4"`
	LastAt     time.Time `json:"last_at"`
}

// MediaEnrich 补全任务：文件名缺分辨率/编码等信息，用 ffprobe 探测
// 115 直链头部后按模板重新命名（蜘蛛侠.2016.mkv → 规范名）
type MediaEnrich struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	FileID    string    `json:"file_id" gorm:"index;size:64"`          // 115 文件 id
	PickCode  string    `json:"pick_code" gorm:"size:64"`              // 直链探测用
	FileName  string    `json:"file_name" gorm:"size:255"`             // 当前文件名
	Status    string    `json:"status" gorm:"size:20;default:pending"` // pending/done/failed/skipped
	Message   string    `json:"message" gorm:"size:255"`               // 结果说明
	Attempts  int       `json:"attempts" gorm:"default:0"`             // 重试次数
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

var DB *gorm.DB

func InitDB(dbPath string) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, err
	}

	// 自动迁移
	if err := db.AutoMigrate(
		&Admin{},
		&Storage{},
		&StrmFile{},
		&SyncTask{},
		&TmdbConfig{},
		&Setting{},
		&ScrapeRule{},
		&CategoryRule{},
		&WashRule{},
		&MediaEnrich{},
		&PortalStat{},
		&MediaLibrary{},
		&SyncEvent{},
		&SyncedFile{},
		&OfflinePlay{},
		&UploadMark{},
	); err != nil {
		return nil, err
	}

	DB = db
	return db, nil
}

func IsInitialized(db *gorm.DB) (bool, error) {
	var count int64
	err := db.Model(&Admin{}).Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// ResetAdmin 删除管理员账号（保留所有其他配置），用于忘记密码时重置
func ResetAdmin(db *gorm.DB) error {
	return db.Where("1 = 1").Delete(&Admin{}).Error
}

// InitDefaultCategories 初始化 CMS 风格的默认二级分类（首次使用时调用）
func InitDefaultCategories(db *gorm.DB) error {
	var count int64
	if err := db.Model(&CategoryRule{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil // 已存在，跳过
	}

	defaults := []CategoryRule{
		// 电影分类
		{MediaType: "movie", Name: "动画电影", GenreIds: "16", Priority: 1},
		{MediaType: "movie", Name: "华语电影", OriginalLanguage: "zh,cn,bo,za", Priority: 2},
		{MediaType: "movie", Name: "纪录片", GenreIds: "99", Priority: 3},
		{MediaType: "movie", Name: "外语电影", IsDefault: true, Priority: 99},
		// 电视剧分类
		{MediaType: "tv", Name: "国漫", GenreIds: "16", OriginCountry: "CN,TW,HK", Priority: 1},
		{MediaType: "tv", Name: "日番", GenreIds: "16", OriginCountry: "JP", Priority: 2},
		{MediaType: "tv", Name: "纪录片", GenreIds: "99", Priority: 3},
		{MediaType: "tv", Name: "综艺", GenreIds: "10764,10767", Priority: 4},
		{MediaType: "tv", Name: "国产剧", OriginCountry: "CN,TW,HK", Priority: 5},
		{MediaType: "tv", Name: "欧美剧", OriginCountry: "US,FR,GB,DE,ES,IT,NL,PT,RU,UK", Priority: 6},
		{MediaType: "tv", Name: "日韩剧", OriginCountry: "JP,KP,KR,TH,IN,SG", Priority: 7},
		{MediaType: "tv", Name: "未分类", IsDefault: true, Priority: 99},
	}
	return db.Create(&defaults).Error
}

// InitDefaultWashRules 初始化默认洗版策略
func InitDefaultWashRules(db *gorm.DB) error {
	var count int64
	if err := db.Model(&WashRule{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	defaults := []WashRule{
		{
			Name:      "电影洗版策略",
			Mode:      "replace",
			MediaType: "movie",
			PriorityLevel: `[
				{"resource_team":"WiKi","resource_effect":"!DV.HDR,!DV"},
				{"resource_pix":"2160p","resource_type":"BluRay","resource_effect":"!DV.HDR,!DV"},
				{"resource_pix":"1080p","resource_type":"BluRay"},
				{"resource_pix":"2160p","resource_type":"WEB-DL","resource_effect":"!DV.HDR,!DV"}
			]`,
		},
		{
			Name:      "剧集洗版策略",
			Mode:      "replace",
			MediaType: "tv",
			PriorityLevel: `[
				{"resource_pix":"2160p","resource_effect":"!DV.HDR,!DV"},
				{"resource_pix":"1080p"}
			]`,
		},
	}
	return db.Create(&defaults).Error
}
