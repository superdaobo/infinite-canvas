package repository

import (
	"strings"

	"github.com/tigerowo/infinite-canvas/model"
	"gorm.io/gorm"
)

// SaveRoomMedia 写入或更新一条房间媒体索引（按 room_id + id 幂等）。
func SaveRoomMedia(media model.RoomMedia) (model.RoomMedia, error) {
	db, err := DB()
	if err != nil {
		return model.RoomMedia{}, err
	}
	return media, db.Save(&media).Error
}

// FindRoomMedia 读取房间内指定媒体的索引；不存在时返回 ok=false。
func FindRoomMedia(roomID, mediaID string) (model.RoomMedia, bool, error) {
	db, err := DB()
	if err != nil {
		return model.RoomMedia{}, false, err
	}
	var media model.RoomMedia
	err = db.First(
		&media,
		"room_id = ? AND id = ?",
		strings.TrimSpace(roomID),
		strings.TrimSpace(mediaID),
	).Error
	if err == gorm.ErrRecordNotFound {
		return model.RoomMedia{}, false, nil
	}
	if err != nil {
		return model.RoomMedia{}, false, err
	}
	return media, true, nil
}

// IsRoomMediaID 判断某个 ID 是否已被任意房间登记为共享媒体。
// 用于在公开文件读取路径上拦截房间媒体 ID（R1 收敛，防御性校验）。
func IsRoomMediaID(mediaID string) (bool, error) {
	mediaID = strings.TrimSpace(mediaID)
	if mediaID == "" {
		return false, nil
	}
	db, err := DB()
	if err != nil {
		return false, err
	}
	var count int64
	if err := db.Model(&model.RoomMedia{}).Where("id = ?", mediaID).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// ListRoomMedia 返回房间全部媒体索引，按创建时间倒序。
func ListRoomMedia(roomID string) ([]model.RoomMedia, error) {
	db, err := DB()
	if err != nil {
		return nil, err
	}
	var items []model.RoomMedia
	err = db.Where("room_id = ?", strings.TrimSpace(roomID)).
		Order("created_at DESC, id DESC").
		Find(&items).Error
	return items, err
}

// DeleteRoomMedia 删除房间内指定媒体的索引，返回是否命中记录。
func DeleteRoomMedia(roomID, mediaID string) (bool, error) {
	db, err := DB()
	if err != nil {
		return false, err
	}
	result := db.Where("room_id = ? AND id = ?", strings.TrimSpace(roomID), strings.TrimSpace(mediaID)).
		Delete(&model.RoomMedia{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// CountRoomMediaByStoragePath 统计仍引用同一私有存储路径的媒体记录数，用于删除后的 blob 回收。
func CountRoomMediaByStoragePath(storagePath string) (int64, error) {
	db, err := DB()
	if err != nil {
		return 0, err
	}
	var count int64
	err = db.Model(&model.RoomMedia{}).Where("storage_path = ?", storagePath).Count(&count).Error
	return count, err
}

// DeleteRoomMediaByRoom 在事务内删除某房间的全部媒体索引，供房间级联删除复用。
func DeleteRoomMediaByRoom(tx *gorm.DB, roomID string) error {
	return tx.Where("room_id = ?", strings.TrimSpace(roomID)).Delete(&model.RoomMedia{}).Error
}
