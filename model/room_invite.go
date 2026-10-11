package model

import "time"

// RoomInvite 是房间邀请（分享链接）。邀请令牌明文只在创建时返回一次给 owner，
// 数据库只保存 SHA-256 哈希，避免邀请码落盘或出现在列表响应里。
// 邀请只能授予 viewer/editor，不能授予 owner（见安全边界 2.4）。
type RoomInvite struct {
	ID        string         `json:"id" gorm:"primaryKey;size:64"`
	RoomID    string         `json:"roomId" gorm:"size:64;index:idx_collab_room_invites_room,priority:1"`
	Role      RoomMemberRole `json:"role" gorm:"size:16"`
	TokenHash string         `json:"-" gorm:"size:64;uniqueIndex"`
	CreatedBy string         `json:"createdBy" gorm:"size:64"`
	ExpiresAt time.Time      `json:"expiresAt"`
	RevokedAt *time.Time     `json:"revokedAt,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
}

// TableName 使用 collab_ 前缀，避免与其他模块的表名冲突。
func (RoomInvite) TableName() string { return "collab_room_invites" }

// Active 判断邀请在给定时刻是否可用：未被撤销且未过期。
func (i RoomInvite) Active(now time.Time) bool {
	return i.RevokedAt == nil && now.Before(i.ExpiresAt)
}

// Inviteable 判断该角色是否允许被邀请授予。只有 editor/viewer 可被邀请，
// owner 权限不得通过分享链接授予。
func (r RoomMemberRole) Inviteable() bool {
	return r == RoomRoleEditor || r == RoomRoleViewer
}
