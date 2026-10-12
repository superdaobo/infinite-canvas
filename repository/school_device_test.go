package repository

import (
	"testing"
	"time"

	"github.com/tigerowo/infinite-canvas/model"
)

// 学校设备持久化测试（C03-03 / #19）：令牌摘要唯一、启停/轮换/账号指纹更新与列表。
// 复用 #18 的文件型 SQLite 测试库，只操作 school_devices 表。

func resetSchoolDevices(t *testing.T) {
	t.Helper()
	db := schoolQueueTestDB(t)
	if err := db.Exec("DELETE FROM school_devices").Error; err != nil {
		t.Fatalf("清空 school_devices 失败: %v", err)
	}
}

func newTestDeviceRecord(id, tokenHash, accountRef string) model.SchoolDevice {
	now := time.Now().UTC()
	return model.SchoolDevice{
		ID:               id,
		Name:             id,
		TokenHash:        tokenHash,
		OwnerScope:       model.SchoolDeviceOwnerScopeSystem,
		OwnerID:          model.SchoolDeviceOwnerID,
		SchoolAccountRef: accountRef,
		Enabled:          true,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
}

func TestSchoolDevicePersistenceLifecycle(t *testing.T) {
	resetSchoolDevices(t)

	device := newTestDeviceRecord("sdev_a", "hash-a", "acct-a")
	created, err := InsertSchoolDevice(device)
	if err != nil || !created {
		t.Fatalf("插入设备失败: created=%v err=%v", created, err)
	}
	// 反例：重复令牌摘要应命中唯一索引，返回 created=false 而非报错。
	if created, err := InsertSchoolDevice(newTestDeviceRecord("sdev_dup", "hash-a", "")); err != nil || created {
		t.Fatalf("重复令牌摘要应 created=false，实际 created=%v err=%v", created, err)
	}

	// 按摘要与 ID 查找。
	if got, ok, err := FindSchoolDeviceByTokenHash("hash-a"); err != nil || !ok || got.ID != "sdev_a" || got.SchoolAccountRef != "acct-a" {
		t.Fatalf("按摘要查找失败: %+v ok=%v err=%v", got, ok, err)
	}
	if _, ok, err := FindSchoolDeviceByTokenHash("missing"); err != nil || ok {
		t.Fatalf("未知摘要应 ok=false，实际 ok=%v err=%v", ok, err)
	}

	// 更新账号指纹。
	if changed, err := SetSchoolDeviceAccountRef("sdev_a", "acct-b"); err != nil || !changed {
		t.Fatalf("更新账号指纹失败: changed=%v err=%v", changed, err)
	}
	if got, _, _ := FindSchoolDeviceByID("sdev_a"); got.SchoolAccountRef != "acct-b" {
		t.Fatalf("账号指纹应更新为 acct-b，实际 %q", got.SchoolAccountRef)
	}

	// 轮换令牌摘要。
	rotatedAt := time.Now().UTC()
	if changed, err := RotateSchoolDeviceToken("sdev_a", "hash-b", rotatedAt); err != nil || !changed {
		t.Fatalf("轮换令牌失败: changed=%v err=%v", changed, err)
	}
	if _, ok, _ := FindSchoolDeviceByTokenHash("hash-a"); ok {
		t.Fatal("轮换后旧摘要不应再命中")
	}
	if got, ok, _ := FindSchoolDeviceByTokenHash("hash-b"); !ok || got.RotatedAt == nil {
		t.Fatalf("轮换后新摘要应命中且记录轮换时间: %+v ok=%v", got, ok)
	}

	// 吊销。
	if changed, err := SetSchoolDeviceEnabled("sdev_a", false); err != nil || !changed {
		t.Fatalf("吊销设备失败: changed=%v err=%v", changed, err)
	}
	if got, _, _ := FindSchoolDeviceByID("sdev_a"); got.Enabled {
		t.Fatal("吊销后设备应为 Enabled=false")
	}

	// 心跳与列表。
	if changed, err := TouchSchoolDeviceLastSeen("sdev_a", time.Now().UTC()); err != nil || !changed {
		t.Fatalf("更新心跳失败: changed=%v err=%v", changed, err)
	}
	if _, err := InsertSchoolDevice(newTestDeviceRecord("sdev_c", "hash-c", "")); err != nil {
		t.Fatalf("插入第二台设备失败: %v", err)
	}
	devices, err := ListSchoolDevices()
	if err != nil || len(devices) != 2 {
		t.Fatalf("设备列表应有 2 台，实际 %d err=%v", len(devices), err)
	}
}
