package model

import "time"

// RoomMemberView 是成员列表项：成员关系 + 用户公开信息。
// 只暴露 ID、用户名、昵称、头像等公开字段，不带邮箱、算力、登录时间等敏感信息。
type RoomMemberView struct {
	UserID      string         `json:"userId"`
	Username    string         `json:"username"`
	DisplayName string         `json:"displayName"`
	AvatarURL   string         `json:"avatarUrl"`
	Role        RoomMemberRole `json:"role"`
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
}

// RoomMemberList 房间成员列表结果。
type RoomMemberList struct {
	Items []RoomMemberView `json:"items"`
	Total int64            `json:"total"`
}

// RoomInviteCreated 是创建邀请的响应：明文令牌仅此一次返回给 owner，
// 之后任何列表接口都不再返回令牌或哈希。
type RoomInviteCreated struct {
	RoomInvite
	Token string `json:"token"`
}

// RoomInviteList 房间邀请列表结果（不含令牌与哈希）。
type RoomInviteList struct {
	Items []RoomInvite `json:"items"`
	Total int64        `json:"total"`
}
