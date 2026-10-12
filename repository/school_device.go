package repository

import (
	"errors"
	"strings"
	"time"

	"github.com/tigerowo/infinite-canvas/model"
	"gorm.io/gorm"
)

// 学校受限执行设备持久化（C03-03 / #19）。
//
// 设备令牌只保存 SHA-256（model.SchoolDevice.TokenHash），明文不落库；
// 设备启用/吊销、令牌轮换、学校账号指纹更新都在本层完成。所有查询按设备 ID 精确匹配，
// 不提供按用户列举他人设备的入口。

// InsertSchoolDevice 插入一条学校设备记录；设备 ID 冲突时返回 created=false。
func InsertSchoolDevice(device model.SchoolDevice) (bool, error) {
	db, err := DB()
	if err != nil {
		return false, err
	}
	if err := db.Create(&device).Error; err != nil {
		if isDuplicateKeyError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// FindSchoolDeviceByTokenHash 按令牌摘要读取设备；不存在时返回 ok=false。
func FindSchoolDeviceByTokenHash(tokenHash string) (model.SchoolDevice, bool, error) {
	db, err := DB()
	if err != nil {
		return model.SchoolDevice{}, false, err
	}
	var device model.SchoolDevice
	err = db.First(&device, "token_hash = ?", strings.TrimSpace(tokenHash)).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.SchoolDevice{}, false, nil
	}
	if err != nil {
		return model.SchoolDevice{}, false, err
	}
	return device, true, nil
}

// FindSchoolDeviceByID 按设备 ID 读取；不存在时返回 ok=false。
func FindSchoolDeviceByID(id string) (model.SchoolDevice, bool, error) {
	db, err := DB()
	if err != nil {
		return model.SchoolDevice{}, false, err
	}
	var device model.SchoolDevice
	err = db.First(&device, "id = ?", strings.TrimSpace(id)).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.SchoolDevice{}, false, nil
	}
	if err != nil {
		return model.SchoolDevice{}, false, err
	}
	return device, true, nil
}

// ListSchoolDevices 返回全部学校设备记录，按创建时间升序（运维/测试用）。
func ListSchoolDevices() ([]model.SchoolDevice, error) {
	db, err := DB()
	if err != nil {
		return nil, err
	}
	var devices []model.SchoolDevice
	if err := db.Order("created_at ASC, id ASC").Find(&devices).Error; err != nil {
		return nil, err
	}
	return devices, nil
}

// SetSchoolDeviceEnabled 启用/吊销设备令牌；返回是否命中记录。
func SetSchoolDeviceEnabled(id string, enabled bool) (bool, error) {
	db, err := DB()
	if err != nil {
		return false, err
	}
	result := db.Model(&model.SchoolDevice{}).
		Where("id = ?", strings.TrimSpace(id)).
		Updates(map[string]any{"enabled": enabled, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// RotateSchoolDeviceToken 替换设备令牌摘要并记录轮换时间；返回是否命中记录。
func RotateSchoolDeviceToken(id, tokenHash string, rotatedAt time.Time) (bool, error) {
	db, err := DB()
	if err != nil {
		return false, err
	}
	result := db.Model(&model.SchoolDevice{}).
		Where("id = ?", strings.TrimSpace(id)).
		Updates(map[string]any{
			"token_hash": strings.TrimSpace(tokenHash),
			"rotated_at": rotatedAt,
			"updated_at": rotatedAt,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// SetSchoolDeviceAccountRef 更新设备绑定的学校账号指纹；返回是否命中记录。
func SetSchoolDeviceAccountRef(id, accountRef string) (bool, error) {
	db, err := DB()
	if err != nil {
		return false, err
	}
	result := db.Model(&model.SchoolDevice{}).
		Where("id = ?", strings.TrimSpace(id)).
		Updates(map[string]any{
			"school_account_ref": strings.TrimSpace(accountRef),
			"updated_at":         time.Now().UTC(),
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// TouchSchoolDeviceLastSeen 更新设备最近心跳时间；返回是否命中记录。
func TouchSchoolDeviceLastSeen(id string, seenAt time.Time) (bool, error) {
	db, err := DB()
	if err != nil {
		return false, err
	}
	result := db.Model(&model.SchoolDevice{}).
		Where("id = ?", strings.TrimSpace(id)).
		Updates(map[string]any{"last_seen_at": seenAt, "updated_at": seenAt})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}
