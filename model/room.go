package model

import "time"

// RoomMemberRole 是协作房间成员角色，与协作 v1 契约的 owner/editor/viewer 一致。
// admin 是系统角色而非房间成员角色，因此不在本枚举内。
type RoomMemberRole string

const (
	RoomRoleOwner  RoomMemberRole = "owner"
	RoomRoleEditor RoomMemberRole = "editor"
	RoomRoleViewer RoomMemberRole = "viewer"
)

// Valid 判断是否为契约内的房间角色。
func (r RoomMemberRole) Valid() bool {
	switch r {
	case RoomRoleOwner, RoomRoleEditor, RoomRoleViewer:
		return true
	default:
		return false
	}
}

// CanWrite 表示该角色是否可写画布：owner/editor 可写，viewer 只读。
func (r RoomMemberRole) CanWrite() bool {
	return r == RoomRoleOwner || r == RoomRoleEditor
}

// Room 是协作共享房间，与个人画布 CanvasProject 完全独立。
type Room struct {
	ID        string    `json:"id" gorm:"primaryKey;size:64"`
	OwnerID   string    `json:"ownerId" gorm:"size:64;index:idx_collab_rooms_owner_updated,priority:1"`
	Title     string    `json:"title" gorm:"size:512"`
	Revision  int64     `json:"revision"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt" gorm:"index:idx_collab_rooms_owner_updated,priority:2"`
}

// TableName 使用 collab_ 前缀，避免与其他模块的表名冲突。
func (Room) TableName() string { return "collab_rooms" }

// RoomWithRole 是房间及其对当前访问者的角色，用于列表与详情返回。
type RoomWithRole struct {
	Room
	Role RoomMemberRole `json:"role"`
}

// RoomList 房间分页结果。
type RoomList struct {
	Items []RoomWithRole `json:"items"`
	Total int64          `json:"total"`
}
