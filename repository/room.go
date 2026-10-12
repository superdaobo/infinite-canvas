package repository

import (
	"strings"
	"time"

	"github.com/tigerowo/infinite-canvas/model"
	"gorm.io/gorm"
)

// roomRow 是房间分页查询的原始行，显式列出字段以避免 GORM 结构体嵌入扫描歧义。
type roomRow struct {
	ID        string
	OwnerID   string
	Title     string
	Revision  int64
	CreatedAt time.Time
	UpdatedAt time.Time
	Role      string
}

// CreateRoomWithOwner 在单个事务内创建房间并写入 owner 成员关系。
func CreateRoomWithOwner(room model.Room, owner model.RoomMember) error {
	db, err := DB()
	if err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&room).Error; err != nil {
			return err
		}
		return tx.Create(&owner).Error
	})
}

// FindRoom 按 ID 读取房间；不存在时返回 ok=false。
func FindRoom(roomID string) (model.Room, bool, error) {
	db, err := DB()
	if err != nil {
		return model.Room{}, false, err
	}
	var room model.Room
	err = db.First(&room, "id = ?", strings.TrimSpace(roomID)).Error
	if err == gorm.ErrRecordNotFound {
		return model.Room{}, false, nil
	}
	if err != nil {
		return model.Room{}, false, err
	}
	return room, true, nil
}

// FindRoomMember 读取用户在指定房间内的成员关系；不存在时返回 ok=false。
func FindRoomMember(roomID, userID string) (model.RoomMember, bool, error) {
	db, err := DB()
	if err != nil {
		return model.RoomMember{}, false, err
	}
	var member model.RoomMember
	err = db.First(
		&member,
		"room_id = ? AND user_id = ?",
		strings.TrimSpace(roomID),
		strings.TrimSpace(userID),
	).Error
	if err == gorm.ErrRecordNotFound {
		return model.RoomMember{}, false, nil
	}
	if err != nil {
		return model.RoomMember{}, false, err
	}
	return member, true, nil
}

// SaveRoomMember 写入或更新一条成员关系（供后续邀请/角色变更复用）。
func SaveRoomMember(member model.RoomMember) error {
	db, err := DB()
	if err != nil {
		return err
	}
	return db.Save(&member).Error
}

// ListRoomsByUser 分页返回用户通过成员关系可访问的房间，按更新时间倒序。
func ListRoomsByUser(userID string, offset, limit int) ([]model.RoomWithRole, int64, error) {
	db, err := DB()
	if err != nil {
		return nil, 0, err
	}
	userID = strings.TrimSpace(userID)

	var total int64
	if err := db.Table("collab_rooms").
		Joins("JOIN collab_room_members ON collab_room_members.room_id = collab_rooms.id").
		Where("collab_room_members.user_id = ?", userID).
		Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []roomRow
	if err := db.Table("collab_rooms").
		Select("collab_rooms.id, collab_rooms.owner_id, collab_rooms.title, collab_rooms.revision, collab_rooms.created_at, collab_rooms.updated_at, collab_room_members.role").
		Joins("JOIN collab_room_members ON collab_room_members.room_id = collab_rooms.id").
		Where("collab_room_members.user_id = ?", userID).
		Order("collab_rooms.updated_at DESC, collab_rooms.id DESC").
		Offset(offset).Limit(limit).
		Scan(&rows).Error; err != nil {
		return nil, 0, err
	}

	rooms := make([]model.RoomWithRole, 0, len(rows))
	for _, row := range rows {
		rooms = append(rooms, model.RoomWithRole{
			Room: model.Room{
				ID:        row.ID,
				OwnerID:   row.OwnerID,
				Title:     row.Title,
				Revision:  row.Revision,
				CreatedAt: row.CreatedAt,
				UpdatedAt: row.UpdatedAt,
			},
			Role: model.RoomMemberRole(row.Role),
		})
	}
	return rooms, total, nil
}

// DeleteRoomCascade 删除协作房间及其全部成员关系，只作用于协作房间表。
func DeleteRoomCascade(roomID string) error {
	db, err := DB()
	if err != nil {
		return err
	}
	roomID = strings.TrimSpace(roomID)
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("room_id = ?", roomID).Delete(&model.RoomMember{}).Error; err != nil {
			return err
		}
		if err := DeleteRoomInvitesByRoom(tx, roomID); err != nil {
			return err
		}
		if err := DeleteRoomMediaByRoom(tx, roomID); err != nil {
			return err
		}
		return tx.Where("id = ?", roomID).Delete(&model.Room{}).Error
	})
}
