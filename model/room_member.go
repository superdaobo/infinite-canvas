package model

import "time"

// RoomMember 是房间成员关系，持久化用户对房间的访问权与角色。
type RoomMember struct {
	RoomID    string         `json:"roomId" gorm:"primaryKey;size:64"`
	UserID    string         `json:"userId" gorm:"primaryKey;size:64;index:idx_collab_room_members_user,priority:1"`
	Role      RoomMemberRole `json:"role" gorm:"size:16"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
}

// TableName 使用 collab_ 前缀，避免与其他模块的表名冲突。
func (RoomMember) TableName() string { return "collab_room_members" }
