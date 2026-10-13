package repository

import (
	"strings"
	"time"

	"github.com/tigerowo/infinite-canvas/model"
	"gorm.io/gorm"
)

// SaveRoomMediaUpload 写入或更新一条分片上传会话（按 id 幂等）。
func SaveRoomMediaUpload(upload model.RoomMediaUpload) (model.RoomMediaUpload, error) {
	db, err := DB()
	if err != nil {
		return model.RoomMediaUpload{}, err
	}
	return upload, db.Save(&upload).Error
}

// FindRoomMediaUpload 读取指定上传会话；不存在时返回 ok=false。
func FindRoomMediaUpload(uploadID string) (model.RoomMediaUpload, bool, error) {
	db, err := DB()
	if err != nil {
		return model.RoomMediaUpload{}, false, err
	}
	var upload model.RoomMediaUpload
	err = db.First(&upload, "id = ?", strings.TrimSpace(uploadID)).Error
	if err == gorm.ErrRecordNotFound {
		return model.RoomMediaUpload{}, false, nil
	}
	if err != nil {
		return model.RoomMediaUpload{}, false, err
	}
	return upload, true, nil
}

// CompareAndSetRoomMediaUploadStatus 原子更新会话状态：仅当当前状态等于 from 时才改为 to，
// 返回是否命中。用于并发完成请求的抢占（只有一个请求能进入合并）以及失败后的状态回退。
func CompareAndSetRoomMediaUploadStatus(uploadID string, from, to model.RoomMediaUploadStatus, mediaID string) (bool, error) {
	db, err := DB()
	if err != nil {
		return false, err
	}
	result := db.Model(&model.RoomMediaUpload{}).
		Where("id = ? AND status = ?", strings.TrimSpace(uploadID), from).
		Updates(map[string]any{"status": to, "media_id": mediaID, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// DeleteRoomMediaUpload 删除一条上传会话，返回是否命中记录。
func DeleteRoomMediaUpload(uploadID string) (bool, error) {
	db, err := DB()
	if err != nil {
		return false, err
	}
	result := db.Where("id = ?", strings.TrimSpace(uploadID)).Delete(&model.RoomMediaUpload{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// ListExpiredRoomMediaUploads 返回在 before 之前过期的上传会话，供清理任务回收临时分片。
func ListExpiredRoomMediaUploads(before time.Time) ([]model.RoomMediaUpload, error) {
	db, err := DB()
	if err != nil {
		return nil, err
	}
	var items []model.RoomMediaUpload
	err = db.Where("expires_at < ?", before).Find(&items).Error
	return items, err
}

// DeleteRoomMediaUploadsByRoom 在事务内删除某房间的全部分片上传会话，供房间级联删除复用。
func DeleteRoomMediaUploadsByRoom(tx *gorm.DB, roomID string) error {
	return tx.Where("room_id = ?", strings.TrimSpace(roomID)).Delete(&model.RoomMediaUpload{}).Error
}
