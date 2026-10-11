package repository

import (
	"strings"
	"time"

	"github.com/tigerowo/infinite-canvas/model"
	"gorm.io/gorm"
)

// ListRoomMembers 返回房间全部成员关系，owner 优先、再按加入时间排序。
func ListRoomMembers(roomID string) ([]model.RoomMember, error) {
	db, err := DB()
	if err != nil {
		return nil, err
	}
	var members []model.RoomMember
	err = db.Where("room_id = ?", strings.TrimSpace(roomID)).
		Order("CASE role WHEN 'owner' THEN 0 WHEN 'editor' THEN 1 ELSE 2 END, created_at ASC, user_id ASC").
		Find(&members).Error
	return members, err
}

// GetUsersByIDs 批量读取用户，返回以 ID 为键的映射，用于成员列表补全公开信息。
func GetUsersByIDs(ids []string) (map[string]model.User, error) {
	users := make(map[string]model.User, len(ids))
	if len(ids) == 0 {
		return users, nil
	}
	db, err := DB()
	if err != nil {
		return nil, err
	}
	var rows []model.User
	if err := db.Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, user := range rows {
		users[user.ID] = user
	}
	return users, nil
}

// UpdateRoomMemberRole 更新成员角色，返回是否命中了成员记录。
func UpdateRoomMemberRole(roomID, userID string, role model.RoomMemberRole) (bool, error) {
	db, err := DB()
	if err != nil {
		return false, err
	}
	result := db.Model(&model.RoomMember{}).
		Where("room_id = ? AND user_id = ?", strings.TrimSpace(roomID), strings.TrimSpace(userID)).
		Updates(map[string]any{"role": role, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// DeleteRoomMember 移除成员关系，返回是否删除了记录。移除后该用户不再视为房间成员。
func DeleteRoomMember(roomID, userID string) (bool, error) {
	db, err := DB()
	if err != nil {
		return false, err
	}
	result := db.Where("room_id = ? AND user_id = ?", strings.TrimSpace(roomID), strings.TrimSpace(userID)).
		Delete(&model.RoomMember{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// JoinRoomMemberIfAbsent 在成员不存在时写入邀请角色；已存在则保留原角色不变
// （不降级已有成员）。返回最终生效的成员关系。
func JoinRoomMemberIfAbsent(member model.RoomMember) (model.RoomMember, error) {
	db, err := DB()
	if err != nil {
		return model.RoomMember{}, err
	}
	var result model.RoomMember
	err = db.Transaction(func(tx *gorm.DB) error {
		var existing model.RoomMember
		findErr := tx.First(&existing, "room_id = ? AND user_id = ?", member.RoomID, member.UserID).Error
		if findErr == nil {
			result = existing
			return nil
		}
		if findErr != gorm.ErrRecordNotFound {
			return findErr
		}
		if createErr := tx.Create(&member).Error; createErr != nil {
			// 并发下可能已被其他请求插入，回读一次保证幂等。
			var again model.RoomMember
			if readErr := tx.First(&again, "room_id = ? AND user_id = ?", member.RoomID, member.UserID).Error; readErr == nil {
				result = again
				return nil
			}
			return createErr
		}
		result = member
		return nil
	})
	return result, err
}
