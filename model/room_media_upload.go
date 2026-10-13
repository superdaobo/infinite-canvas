package model

import "time"

// RoomMediaUploadStatus 是房间媒体分片上传会话的状态机（协作 v1，C04-03）。
type RoomMediaUploadStatus string

const (
	// RoomMediaUploadStatusPending 表示会话可继续接收分片。
	RoomMediaUploadStatusPending RoomMediaUploadStatus = "pending"
	// RoomMediaUploadStatusCompleting 表示完成请求正在合并分片，此时拒绝新分片。
	RoomMediaUploadStatusCompleting RoomMediaUploadStatus = "completing"
	// RoomMediaUploadStatusCompleted 表示合并成功且媒体已登记，重复完成请求幂等返回同一媒体。
	RoomMediaUploadStatusCompleted RoomMediaUploadStatus = "completed"
	// RoomMediaUploadStatusAborted 表示会话被主动取消。
	RoomMediaUploadStatusAborted RoomMediaUploadStatus = "aborted"
)

// RoomMediaUpload 是房间媒体分片上传会话（协作 v1，C04-03）。
//
// 单个大文件按服务端指定的固定分片大小切分后逐个上传，单次请求体远小于 Cloudflare
// 100MB 请求体上限，因此整份文件可以远大于单请求路径的上限。分片以「会话 ID + 序号」
// 落盘到私有临时目录，完成时服务端按序合并、校验总大小与 SHA-256 摘要后，复用单请求
// 路径的落位逻辑登记为 RoomMedia（内容寻址、按房间去重）。会话过期后由清理任务回收。
type RoomMediaUpload struct {
	ID         string                `json:"id" gorm:"primaryKey;size:64"`
	RoomID     string                `json:"roomId" gorm:"size:64;index:idx_collab_room_media_uploads_room,priority:1"`
	CreatedBy  string                `json:"createdBy" gorm:"size:64"`
	Filename   string                `json:"filename" gorm:"size:512"`
	MimeType   string                `json:"mimeType" gorm:"size:255"`
	TotalBytes int64                 `json:"totalBytes"`
	ChunkBytes int64                 `json:"chunkBytes"`
	ChunkCount int                   `json:"chunkCount"`
	Digest     string                `json:"-" gorm:"size:64"` // 期望的 SHA-256 十六进制摘要，绝不返回给客户端
	Status     RoomMediaUploadStatus `json:"status" gorm:"size:16"`
	MediaID    string                `json:"mediaId" gorm:"size:64"` // 完成后指向 RoomMedia.ID
	ExpiresAt  time.Time             `json:"expiresAt" gorm:"index:idx_collab_room_media_uploads_expiry"`
	CreatedAt  time.Time             `json:"createdAt"`
	UpdatedAt  time.Time             `json:"updatedAt"`
}

// TableName 使用 collab_ 前缀，避免与其他模块的表名冲突。
func (RoomMediaUpload) TableName() string { return "collab_room_media_uploads" }

// RoomMediaUploadView 是分片上传会话对外视图，带实时进度（已收分片数与字节数）。
type RoomMediaUploadView struct {
	ID             string                `json:"id"`
	RoomID         string                `json:"roomId"`
	Filename       string                `json:"filename"`
	MimeType       string                `json:"mimeType"`
	TotalBytes     int64                 `json:"totalBytes"`
	ChunkBytes     int64                 `json:"chunkBytes"`
	ChunkCount     int                   `json:"chunkCount"`
	ReceivedChunks int                   `json:"receivedChunks"`
	ReceivedBytes  int64                 `json:"receivedBytes"`
	Status         RoomMediaUploadStatus `json:"status"`
	MediaID        string                `json:"mediaId,omitempty"`
	ExpiresAt      time.Time             `json:"expiresAt"`
	CreatedAt      time.Time             `json:"createdAt"`
}
