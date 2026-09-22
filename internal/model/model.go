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
	Name       string `json:"name" gorm:"size:100;not null"`            // 库内相对目录，如 电影/华语电影、动漫番剧
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

// MediaLibrary 已整理的媒体记录（用于去重）
type MediaLibrary struct {
	ID            uint      `json:"id" gorm:"primaryKey"`
	TmdbID        int       `json:"tmdb_id" gorm:"index"`
	Title         string    `json:"title" gorm:"size:255"`
	OriginalTitle string    `json:"original_title" gorm:"size:255"`
	Year          string    `json:"year" gorm:"size:10"`
	MediaType     string    `json:"media_type" gorm:"size:20;index"` // movie, tv
	Category      string    `json:"category" gorm:"size:50;index"`   // 仪表盘按分类聚合高频查询
	TargetPath    string    `json:"target_path" gorm:"size:500"`
	OrigLanguage  string    `json:"original_language" gorm:"size:20"`
	OrigCountry   string    `json:"origin_country" gorm:"size:100"`
	PosterPath    string    `json:"poster_path" gorm:"size:255"` // TMDB 海报路径
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
	PickCode  string     `json:"pick_code" gorm:"size:64"`    // 事件自带；有则零遍历直推 STRM，不必重遍历目录
	FileCat   string     `json:"file_category" gorm:"size:4"` // "0"=目录 "1"=文件
	Size      int64      `json:"size"`
	EventTime int64      `json:"event_time"`                              // unix 秒
	Status    string     `json:"status" gorm:"size:20;default:'pending'"` // pending / applied
	CreatedAt time.Time  `json:"created_at"`
	AppliedAt *time.Time `json:"applied_at"`
}

// SyncedFile 已同步到本地的文件台账（ strm 与附属文件），供 move/delete 事件精确定位
type SyncedFile struct {
	ID       uint   `json:"id" gorm:"primaryKey"`
	FileID   string `json:"file_id" gorm:"uniqueIndex;size:64;not null"` // 115 文件 id
	PickCode string `json:"pick_code" gorm:"size:64"`
	Sha1     string `json:"sha1" gorm:"index;size:40"`               // 文件 sha1（整理去重用）
	RelPath  string `json:"rel_path" gorm:"size:500;not null;index"` // 相对本地库根的路径（含文件名）
	Kind     string `json:"kind" gorm:"size:10;index"`               // video / asset
	Size     int64  `json:"size"`
	// OrphanAt 最近一次「完整」全量扫描中该文件在网盘上已不存在的时刻。
	// 非空即为失效 STRM 候选：本地 strm/附属还在，源文件没了（网页版手动删除、
	// 增量同步停机期间的变动等生活事件漏掉的情况）。只做标记不自动删除，
	// 由用户在同步页看过预览后手动触发清理
	OrphanAt *time.Time `json:"orphan_at" gorm:"index"`
	// VanishAt 仅保留旧数据库兼容。事件删除不再读写此标记，避免遗留候选扩大删除范围。
	VanishAt  *time.Time `json:"vanish_at" gorm:"index"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// DeepDeleteRecord 深度删除流水：一条 = 一次执行（一批文件）。
//
// 留痕不是为了好看：删除进的是 115 回收站，用户事后要还原时得知道
// 当时删的是哪几个 fid —— 台账行那时已经跟着删掉了，不留这张表就再也查不出来。
type DeepDeleteRecord struct {
	ID     uint   `json:"id" gorm:"primaryKey"`
	Reason string `json:"reason" gorm:"index;size:16"` // emby_webhook / manual_record（旧来源仅保留历史）
	Title  string `json:"title" gorm:"size:255"`       // 从 rel_path 推出的片名，给人看的
	// RelPaths / Fids 都是 JSON 数组。Fids 是回收站还原的凭据，别省
	RelPaths string `json:"rel_paths" gorm:"type:text"`
	Fids     string `json:"fids" gorm:"type:text"`

	VideoCnt  int       `json:"video_cnt"`
	AssetCnt  int       `json:"asset_cnt"`
	PanDirs   int       `json:"pan_dirs"`                    // 顺带清理掉的网盘空目录数
	Status    string    `json:"status" gorm:"index;size:16"` // done / rejected / failed（dry_run 仅为历史记录）
	Message   string    `json:"message" gorm:"size:500"`     // 被阈值拦下或失败时写原因
	CreatedAt time.Time `json:"created_at" gorm:"index"`
}

// DownloadLink 下载记录：一条 = 一次提交的磁力/ed2k/HTTP/FTP 离线下载，
// 或一次 115 分享转存。提交时落库，内容被整理入库后把识别结果（片名 / 年份 /
// TMDB id / 分类 / 落库目录）回写到同一行 —— 一条链接从「提交了什么」到
// 「最后成了哪部片」都在这一行上。
//
// ⚠️ 认领产物**不允许新增任何 115 请求**，只能用已经在手的数据：
//   - 离线任务：监视器本来就在 30 秒轮询任务列表，顺手摘 file_id（产物 fid）与任务名
//   - 分享转存：/share/snap 的返回里本来就有顶层条目名，转存后 115 保留原名
//
// 为什么不复用 OfflinePlay：那张表是「按需离线播放端点」的登记（主键是链接指纹、
// 不含分享链接、也没有产物信息），职责不同，混用会把两件事绑死。
type DownloadLink struct {
	ID   uint   `json:"id" gorm:"primaryKey"`
	Kind string `json:"kind" gorm:"index;size:16"` // magnet / ed2k / http / ftp / share
	URL  string `json:"url" gorm:"size:1000"`      // 原始链接（分享链接不含提取码）
	Hash string `json:"hash" gorm:"index;size:64"` // 磁力 btih / ed2k 文件 hash / 分享 share_code，与 115 任务列表对账用
	Name string `json:"name" gorm:"size:500"`      // 任务名 / 分享标题 / 链接文件名（提交时取得到多少算多少，回填时补全）

	TargetCid string `json:"target_cid" gorm:"size:64"` // 提交时指定的转存目录
	Source    string `json:"source" gorm:"size:32"`     // 提交来源：web / 机器人 / 影巢 / TG订阅 / 按需离线
	// Status 下载/转存侧的状态：submitted（已提交）/ downloading / done / failed。
	// 分享转存是同步完成的，登记即 done
	Status string `json:"status" gorm:"index;size:16"`
	Note   string `json:"note" gorm:"size:500"` // 失败原因或补充说明

	// ResultFids / ResultNames 产物在转存目录里的定位信息（JSON 数组），
	// 整理认领时用：fid 精确（离线任务的 file_id），名字兜底（分享转存只有名字）
	ResultFids  string `json:"result_fids" gorm:"type:text"`
	ResultNames string `json:"result_names" gorm:"type:text"`

	// ---- 整理结果（内容被整理入库后回写，纯本地 DB 操作）----
	OrganizeStatus string     `json:"organize_status" gorm:"index;size:16"` // ""（还没整理）/ success / exists / failed / unrecognized
	OrganizedAt    *time.Time `json:"organized_at"`
	RecordID       uint       `json:"record_id" gorm:"index"` // 对应的 OrganizeRecord.ID，前端跳整理记录用
	TmdbID         int        `json:"tmdb_id" gorm:"index"`
	Title          string     `json:"title" gorm:"size:255"`
	Year           string     `json:"year" gorm:"size:10"`
	MediaType      string     `json:"media_type" gorm:"size:20"`
	PosterPath     string     `json:"poster_path" gorm:"size:255"` // 列表直接出图，走 /tmdb/img 代理
	Category       string     `json:"category" gorm:"size:50"`
	TargetDir      string     `json:"target_dir" gorm:"size:500"` // 库内相对路径（不含库名）

	CreatedAt time.Time `json:"created_at" gorm:"index"`
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

// OrganizeRecord 整理记录：一条 = 一次整理动作处理的一个条目（一个待整理目录或一个散文件）。
// 与 MediaLibrary 的区别：MediaLibrary 是「一部影视一条」的去重快照（仪表盘用），
// 这里是「一次动作一条」的流水，失败与未识别同样留痕——识别错了要能回溯并重做。
type OrganizeRecord struct {
	ID         uint   `json:"id" gorm:"primaryKey"`
	BatchID    string `json:"batch_id" gorm:"index;size:32"` // 一轮整理的批次号
	Source     string `json:"source" gorm:"size:500"`        // 原目录名 / 原文件名
	SourceFid  string `json:"source_fid" gorm:"index;size:64"`
	SourceKind string `json:"source_kind" gorm:"size:8"` // dir / file

	Status  string `json:"status" gorm:"index;size:16"` // success / exists / failed / unrecognized
	Stage   string `json:"stage" gorm:"size:16"`        // recognize / move / strm / scrape：失败发生在哪一步
	Message string `json:"message" gorm:"size:500"`

	TmdbID     int    `json:"tmdb_id" gorm:"index"`
	Title      string `json:"title" gorm:"size:255"`
	Year       string `json:"year" gorm:"size:10"`
	MediaType  string `json:"media_type" gorm:"size:20"`
	PosterPath string `json:"poster_path" gorm:"size:255"` // 列表直接出图，走 /tmdb/img 代理
	Category   string `json:"category" gorm:"size:50"`
	TargetDir  string `json:"target_dir" gorm:"size:500"` // 库内相对路径（不含库名）
	TargetCid  string `json:"target_cid" gorm:"size:64"`

	// Files 是 []{fid,name,kind} 的 JSON。fid 在 115 上移动/改名后不变，
	// 所以这就是「重新整理」原地捞回文件所需的全部定位信息
	Files       string `json:"files" gorm:"type:text"`
	VideoCount  int    `json:"video_count"`
	TotalSize   int64  `json:"total_size"`
	StrmCreated int    `json:"strm_created"`
	ScrapeState string `json:"scrape_state" gorm:"size:16"` // done / failed / skipped
	ScrapeMsg   string `json:"scrape_msg" gorm:"size:255"`

	ManualTmdb bool      `json:"manual_tmdb"` // 用户手动指定过 TMDB 条目
	RedoCount  int       `json:"redo_count"`
	CreatedAt  time.Time `json:"created_at" gorm:"index"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// EventSuppress 整理自产事件抑制表：整理自己做的每一次 move/rename 都登记 fid，
// 生活事件绕一圈回来时命中就删除该行并跳过（命中即消费，pop 语义）。
// 落库而不是内存 map：进程重启后 pending 事件还会被重新消费，内存标记会丢。
type EventSuppress struct {
	ID       uint      `json:"id" gorm:"primaryKey"`
	FileID   string    `json:"file_id" gorm:"uniqueIndex;size:64"`
	Op       string    `json:"op" gorm:"size:16"` // move / rename
	ExpireAt time.Time `json:"expire_at" gorm:"index"`
}

// PathCache 115 目录 id → 网盘绝对路径。
//
// 生活事件只带 parent_id 不带路径，每条事件都要把 cid 还原成路径；
// move/rename 的【旧】路径更是只能从这里拿（事件里的字段全是新位置）。
// 一次祖先链请求就能把整条链的每一级都写进来，后续同目录的事件零请求。
//
// ⚠️ 只缓存目录。目录被改名/移动/删除后必须失效对应子树，
// 否则「已搬进冗余的目录」会被算成还在媒体库里（见 forgetPathsUnder）
type PathCache struct {
	FileID    string    `json:"file_id" gorm:"primaryKey;size:64"`
	ParentID  string    `json:"parent_id" gorm:"index;size:64"`
	Name      string    `json:"name" gorm:"size:500"`
	Path      string    `json:"path" gorm:"size:1000;index"` // 网盘绝对路径，/ 开头
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
		&MediaEnrich{},
		&MediaLibrary{},
		&SyncEvent{},
		&SyncedFile{},
		&OfflinePlay{},
		&DownloadLink{},
		&UploadMark{},
		&OrganizeRecord{},
		&EventSuppress{},
		&PathCache{},
		&DeepDeleteRecord{},
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

	// Name 就是库内目录名（相对媒体库根，多级用 / 分隔），写什么就落到哪 ——
	// 整理时不会再额外补一层「电影/」「剧集/」，所以这里必须带全
	defaults := []CategoryRule{
		// 电影分类
		{MediaType: "movie", Name: "电影/动画电影", GenreIds: "16", Priority: 1},
		{MediaType: "movie", Name: "电影/华语电影", OriginalLanguage: "zh,cn,bo,za", Priority: 2},
		{MediaType: "movie", Name: "电影/纪录片", GenreIds: "99", Priority: 3},
		{MediaType: "movie", Name: "电影/外语电影", IsDefault: true, Priority: 99},
		// 电视剧分类
		{MediaType: "tv", Name: "电视剧/国漫", GenreIds: "16", OriginCountry: "CN,TW,HK", Priority: 1},
		{MediaType: "tv", Name: "电视剧/日番", GenreIds: "16", OriginCountry: "JP", Priority: 2},
		{MediaType: "tv", Name: "电视剧/纪录片", GenreIds: "99", Priority: 3},
		{MediaType: "tv", Name: "电视剧/综艺", GenreIds: "10764,10767", Priority: 4},
		{MediaType: "tv", Name: "电视剧/国产剧", OriginCountry: "CN,TW,HK", Priority: 5},
		{MediaType: "tv", Name: "电视剧/欧美剧", OriginCountry: "US,FR,GB,DE,ES,IT,NL,PT,RU,UK", Priority: 6},
		{MediaType: "tv", Name: "电视剧/日韩剧", OriginCountry: "JP,KP,KR,TH,IN,SG", Priority: 7},
		{MediaType: "tv", Name: "电视剧/未分类", IsDefault: true, Priority: 99},
	}
	return db.Create(&defaults).Error
}

// DefaultWashYAML 默认洗版策略（首次部署播种进 ScrapeRule.wash_config）。
// 引擎只认库里存的这份 YAML：用户改过就按用户的，清空就是不洗版——
// 代码里不再留任何隐式兜底，否则「我明明清空了还在洗」无从解释
const DefaultWashYAML = `# 洗版模式：coexist共存 / skip跳过 / replace替换 / max_size最大 / min_size最小
# scope：all=全局只留一个最优 / group=按分辨率分组各留一个（如1080p/2160p各一）
# old_version_target：旧版去向 redundant冗余 / existing已存在 / delete移入115回收站（默认冗余）
# replace 不配置 priority_level 时新替旧；有优先级时新版更优才替换，平局不换
# 优先级字段说明：
#   resource_pix: 分辨率（2160p, 1080p, 720p）
#   resource_type: 资源质量（BluRay, WEB-DL, HDTV）
#   video_encode: 视频编码（H265, x264, HEVC, AV1）
#   audio_encode: 音频编码（TrueHD, Atmos, DTS-HD, AAC）
#   resource_effect: 特效（DV.HDR, HDR10+, 排除用!前缀如!DV）
#   resource_team: 发布组（WiKi, TnT, FRDS）
电影洗版策略:
  mode: replace
  media_type: movie
  priority_level:
  - resource_team: "WiKi"
    resource_effect: "!DV.HDR,!DV"
  - resource_pix: "2160p"
    resource_type: "BluRay"
    resource_effect: "!DV.HDR,!DV"
  - resource_pix: "1080p"
    resource_type: "BluRay"

剧集洗版策略:
  mode: replace
  media_type: tv
  priority_level:
  - resource_pix: "2160p"
    resource_effect: "!DV.HDR,!DV"
  - resource_pix: "1080p"
`

// InitDefaultWashConfig 首次部署播种默认洗版策略。
//
// 播种的是引擎真正读的 ScrapeRule(type=wash_config)——此前播的是 WashRule 表，
// 而那张表没有任何代码再读，于是全新部署的洗版恒等于关闭，界面上却因为
// 前端拿默认 YAML 兜底显示而看着像已经配好了。
//
// 只在「这一行压根不存在」时播种：行一旦存在（哪怕 Config 被清空）就不再碰，
// 用户清空即表示不洗版
func InitDefaultWashConfig(db *gorm.DB) error {
	var count int64
	if err := db.Model(&ScrapeRule{}).Where("type = ?", "wash_config").Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return db.Create(&ScrapeRule{Type: "wash_config", Enabled: true, Config: DefaultWashYAML}).Error
}
