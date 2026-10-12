package model

import (
	"strings"
	"time"
)

// 学校受限执行设备的持久化模型（C03-03 / #19）。
//
// 设备令牌与用户 JWT 完全分离：服务端只保存令牌的 SHA-256（TokenHash），
// 明文仅在注册/轮换时返回一次。学校账号以不可逆指纹 SchoolAccountRef 记录，
// 绝不保存学校 Cookie、登录态或 Chrome Profile 内容。
//
// 学校设备是受控的系统级归属（OwnerScope=system），不允许任意用户注册为学校设备。

const (
	// SchoolDeviceOwnerScopeSystem 是学校设备的受控系统级归属（安全边界 R4 决策）。
	SchoolDeviceOwnerScopeSystem = "system"
	// SchoolDeviceOwnerID 是学校设备的系统归属 ID。
	SchoolDeviceOwnerID = "school"
	// SchoolDeviceTokenHexLength 是设备令牌明文长度（32 字节 = 64 位十六进制），
	// 与 Comfy Bridge 设备令牌先例一致。
	SchoolDeviceTokenHexLength = 64
)

// SchoolDevice 是学校受限执行设备的数据库记录，同时用作对外视图。
// TokenHash 带 `json:"-"`，任何序列化（响应、日志）都不会泄漏令牌摘要。
type SchoolDevice struct {
	ID         string `gorm:"primaryKey;size:64" json:"id"`
	Name       string `gorm:"size:128" json:"name"`
	TokenHash  string `gorm:"size:64;uniqueIndex:idx_school_devices_token" json:"-"`
	OwnerScope string `gorm:"size:32" json:"ownerScope"`
	OwnerID    string `gorm:"size:64" json:"ownerId"`
	// SchoolAccountRef 是设备侧学校账号的不可逆指纹；为空表示未绑定账号。
	// 绝不存储学校 Cookie、登录态或账号明文。
	SchoolAccountRef string `gorm:"size:128" json:"schoolAccountRef,omitempty"`
	// Enabled 为 false 表示令牌已被吊销，认证直接失败。
	Enabled    bool       `gorm:"not null;default:true" json:"enabled"`
	LastSeenAt *time.Time `gorm:"precision:6" json:"lastSeenAt,omitempty"`
	RotatedAt  *time.Time `gorm:"precision:6" json:"rotatedAt,omitempty"`
	CreatedAt  time.Time  `gorm:"precision:6" json:"createdAt"`
	UpdatedAt  time.Time  `gorm:"precision:6" json:"updatedAt"`
}

// TableName 使用 school_ 前缀，与协作房间表区分。
func (SchoolDevice) TableName() string { return "school_devices" }

// SchoolAccountMismatch 判断任务绑定的学校账号与设备当前账号是否冲突。
// 任一侧为空表示未绑定账号（兼容未启用账号绑定的设备），只有两侧都非空且不同才算冲突。
// 冲突时旧任务不允许在新账号下重新提交（绑定上游 / 恢复），但可以被显式结束。
func SchoolAccountMismatch(taskAccountRef, deviceAccountRef string) bool {
	taskRef := strings.TrimSpace(taskAccountRef)
	deviceRef := strings.TrimSpace(deviceAccountRef)
	return taskRef != "" && deviceRef != "" && taskRef != deviceRef
}
