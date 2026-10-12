package model

import "time"

// RoomMediaKind 是房间级共享媒体的类别，只允许图片、视频、音频三类。
// 文本类内容不属于媒体，走节点 metadata，不进入本表。
type RoomMediaKind string

const (
	RoomMediaKindImage RoomMediaKind = "image"
	RoomMediaKindVideo RoomMediaKind = "video"
	RoomMediaKindAudio RoomMediaKind = "audio"
)

// Valid 判断是否为受支持的房间媒体类别。
func (k RoomMediaKind) Valid() bool {
	switch k {
	case RoomMediaKindImage, RoomMediaKindVideo, RoomMediaKindAudio:
		return true
	default:
		return false
	}
}

// RoomMedia 是房间级共享媒体索引（协作 v1，C04-01）。
//
// 内容以 SHA-256 摘要作为 ID，`room_id + id` 组成主键，同一房间重复上传相同
// 内容会命中同一条记录（按摘要去重）。字节实际存放在服务端私有内容寻址目录
// （`StoragePath` 相对 MediaDir），绝不登记为公开的 storage_objects，因此不会被
// `GET /api/files/:id` 这类匿名可读路径命中；读取必须走房间成员鉴权接口。
type RoomMedia struct {
	RoomID      string        `json:"roomId" gorm:"primaryKey;size:64;index:idx_collab_room_media_room_created,priority:1"`
	ID          string        `json:"id" gorm:"primaryKey;size:64"` // 内容 SHA-256 十六进制
	Kind        RoomMediaKind `json:"kind" gorm:"size:16"`
	Filename    string        `json:"filename" gorm:"size:512"`
	MimeType    string        `json:"mimeType" gorm:"size:255"`
	Bytes       int64         `json:"bytes"`
	StoragePath string        `json:"-" gorm:"size:255"` // 相对 MediaDir 的私有路径，绝不返回给客户端
	CreatedBy   string        `json:"createdBy" gorm:"size:64"`
	CreatedAt   time.Time     `json:"createdAt"`
	UpdatedAt   time.Time     `json:"updatedAt" gorm:"index:idx_collab_room_media_room_created,priority:2"`
}

// TableName 使用 collab_ 前缀，避免与其他模块的表名冲突。
func (RoomMedia) TableName() string { return "collab_room_media" }

// RoomMediaView 是房间媒体对外视图：不含服务端私有存储路径，只暴露可验证的媒体标识与授权访问地址。
type RoomMediaView struct {
	RoomID      string        `json:"roomId"`
	ID          string        `json:"id"`
	StorageKey  string        `json:"storageKey"` // 契约中的媒体引用：room:<roomId>:<mediaId>
	Kind        RoomMediaKind `json:"kind"`
	Filename    string        `json:"filename"`
	MimeType    string        `json:"mimeType"`
	Bytes       int64         `json:"bytes"`
	CreatedBy   string        `json:"createdBy"`
	CreatedAt   time.Time     `json:"createdAt"`
	PlayURL     string        `json:"playUrl"`     // 成员授权播放地址（支持 Range）
	DownloadURL string        `json:"downloadUrl"` // 成员授权下载地址（附件头）
}

// RoomMediaList 房间媒体列表结果。
type RoomMediaList struct {
	Items []RoomMediaView `json:"items"`
	Total int64           `json:"total"`
}
