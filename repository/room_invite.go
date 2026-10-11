package repository

import (
	"strings"
	"time"

	"github.com/tigerowo/infinite-canvas/model"
	"gorm.io/gorm"
)

// CreateRoomInvite 写入一条邀请记录（只存令牌哈希）。
func CreateRoomInvite(invite model.RoomInvite) error {
	db, err := DB()
	if err != nil {
		return err
	}
	return db.Create(&invite).Error
}

// FindRoomInviteByHash 按令牌哈希查找邀请；不存在时返回 ok=false。
func FindRoomInviteByHash(tokenHash string) (model.RoomInvite, bool, error) {
	db, err := DB()
	if err != nil {
		return model.RoomInvite{}, false, err
	}
	var invite model.RoomInvite
	err = db.First(&invite, "token_hash = ?", strings.TrimSpace(tokenHash)).Error
	if err == gorm.ErrRecordNotFound {
		return model.RoomInvite{}, false, nil
	}
	if err != nil {
		return model.RoomInvite{}, false, err
	}
	return invite, true, nil
}

// FindRoomInvite 在指定房间内按邀请 ID 查找；不存在时返回 ok=false。
func FindRoomInvite(roomID, inviteID string) (model.RoomInvite, bool, error) {
	db, err := DB()
	if err != nil {
		return model.RoomInvite{}, false, err
	}
	var invite model.RoomInvite
	err = db.First(&invite, "room_id = ? AND id = ?", strings.TrimSpace(roomID), strings.TrimSpace(inviteID)).Error
	if err == gorm.ErrRecordNotFound {
		return model.RoomInvite{}, false, nil
	}
	if err != nil {
		return model.RoomInvite{}, false, err
	}
	return invite, true, nil
}

// ListRoomInvites 返回房间的全部邀请，按创建时间倒序（含已撤销/已过期）。
func ListRoomInvites(roomID string) ([]model.RoomInvite, error) {
	db, err := DB()
	if err != nil {
		return nil, err
	}
	var invites []model.RoomInvite
	err = db.Where("room_id = ?", strings.TrimSpace(roomID)).
		Order("created_at DESC, id DESC").Find(&invites).Error
	return invites, err
}

// RevokeRoomInvite 撤销一条邀请。返回 found 表示该邀请是否存在（已撤销视为存在，
// 重复撤销幂等成功）。撤销后令牌立即失效。
func RevokeRoomInvite(roomID, inviteID string, at time.Time) (bool, error) {
	db, err := DB()
	if err != nil {
		return false, err
	}
	roomID = strings.TrimSpace(roomID)
	inviteID = strings.TrimSpace(inviteID)
	result := db.Model(&model.RoomInvite{}).
		Where("room_id = ? AND id = ? AND revoked_at IS NULL", roomID, inviteID).
		Updates(map[string]any{"revoked_at": at, "updated_at": at})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected > 0 {
		return true, nil
	}
	var count int64
	if err := db.Model(&model.RoomInvite{}).
		Where("room_id = ? AND id = ?", roomID, inviteID).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// DeleteRoomInvitesByRoom 删除房间的全部邀请（供房间级联删除使用）。
func DeleteRoomInvitesByRoom(tx *gorm.DB, roomID string) error {
	return tx.Where("room_id = ?", roomID).Delete(&model.RoomInvite{}).Error
}
